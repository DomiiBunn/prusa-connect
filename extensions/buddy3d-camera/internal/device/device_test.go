package device_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/prusa3d/buddy3d-camera/internal/camproto"
	"github.com/prusa3d/buddy3d-camera/internal/device"
	"github.com/prusa3d/buddy3d-camera/internal/store"
)

const fingerprint = "19aa6bb40cb11836dabbb2db33625305"

// core records the frames camera.accept was handed.
type core struct {
	mu     sync.Mutex
	frames map[string][]byte
}

func (c *core) Accept(_ context.Context, principal, camera string, frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if principal != "" {
		panic("a camera's frame was sent in somebody's name")
	}

	if c.frames == nil {
		c.frames = map[string][]byte{}
	}

	c.frames[camera] = bytes.Clone(frame)

	return nil
}

func (c *core) frame(camera string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.frames[camera]
}

func serve(t *testing.T) (*httptest.Server, *store.Store, *core, store.Camera) {
	t.Helper()

	cameras, err := store.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}

	paired, err := cameras.Pair("bed-cam", "printer-1", "")
	if err != nil {
		t.Fatal(err)
	}

	sink := &core{}
	service := device.New(cameras, sink, 30, slog.New(slog.DiscardHandler))
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)

	return server, cameras, sink, paired
}

func put(t *testing.T, server *httptest.Server, path, token, fp string, body []byte) int {
	t.Helper()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, server.URL+path, bytes.NewReader(body))
	req.Header.Set("Token", token)
	req.Header.Set("Fingerprint", fp)
	req.Header.Set("Content-Type", "image/jpg")
	req.Header.Set("User-Agent", "Buddy3D Camera")

	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()

	return res.StatusCode
}

// A picture from the paired camera reaches the core under the camera's name,
// and the fingerprint the first request bound is required after it.
func TestAPictureReachesTheCore(t *testing.T) {
	t.Parallel()

	server, cameras, sink, paired := serve(t)
	frame := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x01, 0xFF, 0xD9}

	if code := put(t, server, "/v1/camera-info", paired.Token, fingerprint,
		[]byte(`{"config":{"name":"Buddy3D Camera"},"device":{"model":"Buddy3D"}}`)); code != http.StatusOK {
		t.Fatalf("camera-info: %d", code)
	}

	if code := put(t, server, "/v1/snapshot", paired.Token, fingerprint, frame); code != http.StatusOK {
		t.Fatalf("snapshot: %d", code)
	}

	if !bytes.Equal(sink.frame("bed-cam"), frame) {
		t.Fatal("the core was not handed the picture")
	}

	if code := put(
		t,
		server,
		"/v1/snapshot",
		paired.Token,
		"another-fingerprint-0000",
		frame,
	); code != http.StatusForbidden {
		t.Fatalf("another fingerprint: %d", code)
	}

	if code := put(t, server, "/c/snapshot", "AAAAAAAAAAAAAAAAAAAA", fingerprint, frame); code != http.StatusForbidden {
		t.Fatalf("an unknown token: %d", code)
	}

	entry := cameras.List()[0]
	if !entry.Bound || entry.Model != "Buddy3D" || entry.LastFrame.IsZero() {
		t.Fatalf("the store holds %+v", entry)
	}
}

type socket struct {
	t    *testing.T
	conn *websocket.Conn
}

func dial(t *testing.T, server *httptest.Server) socket {
	t.Helper()

	url := "wss" + strings.TrimPrefix(server.URL, "https") + "/socket.io/?EIO=4&transport=websocket"

	conn, _, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.CloseNow() })

	s := socket{t: t, conn: conn}
	if got := s.read(); !strings.HasPrefix(got, "0{") {
		t.Fatalf("open: %q", got)
	}

	s.write(websocket.MessageText, []byte("40"))

	if got := s.read(); !strings.HasPrefix(got, "40{") {
		t.Fatalf("connect: %q", got)
	}

	return s
}

func (s socket) read() string {
	s.t.Helper()

	ctx, cancel := context.WithTimeout(s.t.Context(), 5*time.Second)
	defer cancel()

	_, b, err := s.conn.Read(ctx)
	if err != nil {
		s.t.Fatal(err)
	}

	return string(b)
}

func (s socket) write(typ websocket.MessageType, b []byte) {
	s.t.Helper()

	if err := s.conn.Write(s.t.Context(), typ, b); err != nil {
		s.t.Fatal(err)
	}
}

// emit sends a binary event the way the camera's WebSocket++ client does.
func (s socket) emit(ack, event string, payload []byte) {
	s.write(websocket.MessageText, []byte(`451-`+ack+`["`+event+`",{"_placeholder":true,"num":0}]`))
	s.write(websocket.MessageBinary, payload)
}

func auth(token string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, token)
	b = protowire.AppendTag(b, 2, protowire.BytesType)

	return protowire.AppendString(b, fingerprint)
}

// The camera authenticates, is told its interval and asked for its status, and
// what it reports is kept. A snapshot asked for on the page reaches it.
func TestTheSessionSetsTheIntervalAndKeepsTheStatusAndFeatures(t *testing.T) {
	t.Parallel()

	server, cameras, _, paired := serve(t)
	camera := dial(t, server)

	camera.emit("1", "camera_authentication", auth(paired.Token))

	if got := camera.read(); got != "431[0]" {
		t.Fatalf("auth ack: %q", got)
	}

	if got := camera.read(); got != `451-1["configuration",{"_placeholder":true,"num":0}]` {
		t.Fatalf("configuration: %q", got)
	}

	if got := camera.read(); !bytes.Equal([]byte(got), camproto.Configuration(30, "", "interval")) {
		t.Fatalf("configuration payload: %x", got)
	}

	if got := camera.read(); got != `451-2["trigger",{"_placeholder":true,"num":0}]` {
		t.Fatalf("trigger: %q", got)
	}

	_ = camera.read()

	if got := camera.read(); got != `451-3["trigger",{"_placeholder":true,"num":0}]` {
		t.Fatalf("features trigger: %q", got)
	}

	if got := camera.read(); !bytes.Equal([]byte(got), camproto.Trigger(camproto.TriggerFeatures, "features")) {
		t.Fatalf("features trigger payload: %x", got)
	}

	features := protowire.AppendTag(nil, 6, protowire.BytesType)
	features = protowire.AppendString(features, `["RotationZ","IrMode"]`)
	camera.emit("2", "features", features)

	if got := camera.read(); got != "432[0]" {
		t.Fatalf("features ack: %q", got)
	}

	var status []byte
	status = protowire.AppendTag(status, 3, protowire.BytesType)
	status = protowire.AppendBytes(
		status,
		protowire.AppendVarint(protowire.AppendTag(nil, 4, protowire.VarintType), 30),
	)
	camera.emit("3", "status", status)

	if got := camera.read(); got != "433[0]" {
		t.Fatalf("status ack: %q", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for cameras.List()[0].Status == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := cameras.List()[0].Status; got == nil || got.SnapshotInterval != 30 {
		t.Fatalf("the status kept is %+v", got)
	}

	if got := cameras.List()[0].Features; !slices.Equal(got, []string{"RotationZ", "IrMode"}) {
		t.Fatalf("the features kept are %q", got)
	}
}

// A camera with a token nobody paired is acked 1 and its session ends.
func TestAnUnknownTokenEndsTheSession(t *testing.T) {
	t.Parallel()

	server, _, _, _ := serve(t)
	camera := dial(t, server)

	camera.emit("1", "camera_authentication", auth("AAAAAAAAAAAAAAAAAAAA"))

	if got := camera.read(); got != "431[1]" {
		t.Fatalf("auth ack: %q", got)
	}
}

// A camera with an interval of its own is told that one when it connects,
// and told again when a person changes it.
func TestACameraIsToldItsOwnInterval(t *testing.T) {
	t.Parallel()

	cameras, err := store.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}

	paired, err := cameras.Pair("bed-cam", "printer-1", "")
	if err != nil {
		t.Fatal(err)
	}

	if err = cameras.SetSettings("bed-cam", store.Settings{SnapshotInterval: 10}); err != nil {
		t.Fatal(err)
	}

	service := device.New(cameras, &core{}, 30, slog.New(slog.DiscardHandler))
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)

	camera := dial(t, server)
	camera.emit("1", "camera_authentication", auth(paired.Token))
	_ = camera.read() // the ack

	told := func() []byte {
		t.Helper()

		for {
			header := camera.read()
			payload := camera.read()

			if strings.Contains(header, `"configuration"`) {
				return []byte(payload)
			}
		}
	}

	if got := told(); !bytes.Equal(got, camproto.Configuration(10, "", "interval")) {
		t.Fatalf("on connect the camera was told %x", got)
	}

	if err = cameras.SetSettings(
		"bed-cam",
		store.Settings{SnapshotInterval: store.SnapshotsOff, Stream: "rtsp"},
	); err != nil {
		t.Fatal(err)
	}

	if err = service.Configure("bed-cam"); err != nil {
		t.Fatal(err)
	}

	if got := told(); !bytes.Equal(got, camproto.Configuration(store.SnapshotsOff, "rtsp", "interval")) {
		t.Fatalf("after the change the camera was told %x", got)
	}
}

// Events sent at once never interleave on the wire: each header is followed by
// its own attachment, or the camera reads one event's payload as another's.
func TestEventsSentAtOnceKeepTheirAttachments(t *testing.T) {
	t.Parallel()

	cameras, err := store.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}

	paired, err := cameras.Pair("bed-cam", "printer-1", "")
	if err != nil {
		t.Fatal(err)
	}

	service := device.New(cameras, &core{}, 30, slog.New(slog.DiscardHandler))
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)

	camera := dial(t, server)
	camera.emit("1", "camera_authentication", auth(paired.Token))

	for range 7 { // the ack, then the configuration and two triggers, each a header and a payload
		_ = camera.read()
	}

	const senders = 50

	var wg sync.WaitGroup
	for range senders {
		wg.Go(func() { _ = service.Configure("bed-cam") })
	}

	// Each Configure sends two events, each a header and an attachment.
	for frame := range senders * 2 * 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		typ, b, readErr := camera.conn.Read(ctx)
		cancel()

		if readErr != nil {
			t.Fatalf("frame %d: %v", frame, readErr)
		}

		if header := frame%2 == 0; header != (typ == websocket.MessageText && strings.HasPrefix(string(b), "451-")) {
			t.Fatalf("frame %d is %v %q, want a header: %v", frame, typ, b, header)
		}
	}

	wg.Wait()
}

// A turn reaches the camera as one rotation, waits for its ack and asks for
// a picture after it. A command the camera acks with a code other than done
// is answered as refused, with the camera's code.
func TestACommandWaitsForTheCamerasAnswer(t *testing.T) {
	t.Parallel()

	cameras, err := store.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}

	paired, err := cameras.Pair("bed-cam", "printer-1", "")
	if err != nil {
		t.Fatal(err)
	}

	service := device.New(cameras, &core{}, 30, slog.New(slog.DiscardHandler))
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)

	camera := dial(t, server)
	camera.emit("1", "camera_authentication", auth(paired.Token))

	for range 7 { // the ack, then the configuration and two triggers, each a header and a payload
		_ = camera.read()
	}

	done := make(chan error, 1)

	go func() { done <- service.Move(t.Context(), "bed-cam", "left", 30) }()

	if got := camera.read(); got != `451-4["configuration",{"_placeholder":true,"num":0}]` {
		t.Fatalf("the turn: %q", got)
	}

	if got := camera.read(); !bytes.Equal([]byte(got), camproto.Rotation("left", 30, "move")) {
		t.Fatalf("the turn's payload: %x", got)
	}

	camera.write(websocket.MessageText, []byte("434[0]"))

	if err = <-done; err != nil {
		t.Fatalf("a turn the camera did: %v", err)
	}

	if got := camera.read(); got != `451-5["trigger",{"_placeholder":true,"num":0}]` {
		t.Fatalf("the picture after the turn: %q", got)
	}

	_ = camera.read()

	go func() { done <- service.Change(t.Context(), "bed-cam", camproto.Change{Mode: "night"}) }()

	_ = camera.read()
	if got := camera.read(); !bytes.Equal([]byte(got), camproto.Change{Mode: "night"}.Message("change")) {
		t.Fatalf("the mode's payload: %x", got)
	}

	camera.write(websocket.MessageText, []byte("436[102]"))

	var refused device.RefusedError
	if err = <-done; !errors.As(err, &refused) || refused.Code != 102 {
		t.Fatalf("a mode the camera refused: %v", err)
	}
}
