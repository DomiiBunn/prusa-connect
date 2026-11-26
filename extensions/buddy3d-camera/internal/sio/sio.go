// Package sio is the small part of Socket.IO 4 a Buddy3D camera speaks:
// Engine.IO 4 over a websocket, the default namespace, events whose one
// argument is a binary attachment, and acks that carry a number.
//
// The camera opens the websocket directly (EIO=4&transport=websocket), so the
// long-polling transport is not served.
package sio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The ack codes the camera reads, from the cloud's AckCode.
const (
	AckOK            = 0
	AckNotAuthorized = 1
)

// AuthEvent is the event a camera opens its session with.
const AuthEvent = "camera_authentication"

const (
	pingInterval = 25 * time.Second
	pingTimeout  = 20 * time.Second
	readLimit    = 1 << 20
	writeTimeout = 10 * time.Second
)

// The errors Ask answers besides the camera's own code.
var (
	ErrNotConnected = errors.New("sio: the camera is not connected")
	ErrNoAck        = errors.New("sio: the camera did not ack the event")
)

// Handler is what the server asks of the module.
type Handler interface {
	// Authenticate reads the first event's payload and answers the key the
	// session is known by, such as the camera's name, or false to refuse it.
	Authenticate(payload []byte) (key string, ok bool)
	// Ready runs once a session is authenticated and acked.
	Ready(key string)
	// Event runs for every later event. Its ack is sent already.
	Event(key, event string, payload []byte)
}

// Server serves the websocket and keeps one session per key.
type Server struct {
	handler Handler
	log     *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
}

// New returns a server that hands events to handler.
func New(handler Handler, log *slog.Logger) *Server {
	return &Server{handler: handler, log: log, sessions: map[string]*session{}}
}

// Send emits one binary event to the camera known by key, asking for an ack.
func (s *Server) Send(key, event string, payload []byte) error {
	s.mu.Lock()
	held := s.sessions[key]
	s.mu.Unlock()

	if held == nil {
		return ErrNotConnected
	}

	_, err := held.emit(event, payload)

	return err
}

// Ask emits one binary event to the camera known by key and waits for its
// ack, which it answers: AckOK when the camera did it, or the camera's code.
func (s *Server) Ask(ctx context.Context, key, event string, payload []byte) (int, error) {
	s.mu.Lock()
	held := s.sessions[key]
	s.mu.Unlock()

	if held == nil {
		return 0, ErrNotConnected
	}

	acked := make(chan int, 1)

	id, err := held.emit(event, payload, acked)
	if err != nil {
		return 0, err
	}

	defer held.unwait(id)

	select {
	case code := <-acked:
		return code, nil
	case <-ctx.Done():
		return 0, ErrNoAck
	}
}

// Connected reports whether the camera known by key has a session open.
func (s *Server) Connected(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sessions[key] != nil
}

// Drop closes the session of the camera known by key, if it has one.
func (s *Server) Drop(key string) {
	s.mu.Lock()
	held := s.sessions[key]
	s.mu.Unlock()

	if held != nil {
		_ = held.conn.Close(websocket.StatusPolicyViolation, "removed")
	}
}

// ServeHTTP upgrades one request to a session.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("transport") != "websocket" {
		http.Error(w, "this server speaks the websocket transport only", http.StatusBadRequest)

		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}

	conn.SetReadLimit(readLimit)

	held := &session{server: s, conn: conn, id: strconv.FormatInt(time.Now().UnixNano(), 36)}
	held.serve(r.Context())
}

func (s *Server) adopt(key string, held *session) {
	s.mu.Lock()
	old := s.sessions[key]
	s.sessions[key] = held
	s.mu.Unlock()

	if old != nil && old != held {
		_ = old.conn.Close(websocket.StatusNormalClosure, "replaced")
	}
}

func (s *Server) forget(key string, held *session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessions[key] == held {
		delete(s.sessions, key)
	}
}

type session struct {
	server *Server
	conn   *websocket.Conn
	id     string

	writeMu sync.Mutex
	nextAck int
	// Each event Ask sent waits here for its ack, by the ack's id.
	waiting map[int]chan<- int
	key     string

	// A binary event waits here for its attachments.
	pending []any
	ack     string
	want    int
	bins    [][]byte
}

func (h *session) serve(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	defer func() {
		if h.key != "" {
			h.server.forget(h.key, h)
		}
	}()

	open, _ := json.Marshal(map[string]any{
		"sid": h.id, "upgrades": []string{}, "maxPayload": readLimit,
		"pingInterval": pingInterval.Milliseconds(), "pingTimeout": pingTimeout.Milliseconds(),
	})
	h.text("0" + string(open))

	go h.ping(ctx)

	for {
		typ, data, err := h.conn.Read(ctx)
		if err != nil {
			return
		}

		if typ == websocket.MessageBinary {
			h.binary(data)

			continue
		}

		if !h.engine(string(data)) {
			return
		}
	}
}

func (h *session) ping(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.text("2")
		}
	}
}

func (h *session) write(typ websocket.MessageType, b []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	return h.writeLocked(typ, b)
}

// writeLocked writes one frame while the caller holds writeMu.
func (h *session) writeLocked(typ websocket.MessageType, b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	return h.conn.Write(ctx, typ, b)
}

func (h *session) text(msg string) { _ = h.write(websocket.MessageText, []byte(msg)) }

// emit sends a binary event with one attachment and asks for an ack, which
// goes to acked when it is given. The header and its attachment go out under
// one hold of writeMu, so no other frame lands between them.
func (h *session) emit(event string, payload []byte, acked ...chan<- int) (int, error) {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	h.nextAck++
	id := h.nextAck

	if len(acked) > 0 {
		if h.waiting == nil {
			h.waiting = map[int]chan<- int{}
		}

		h.waiting[id] = acked[0]
	}

	header, err := json.Marshal([]any{event, map[string]any{"_placeholder": true, "num": 0}})
	if err != nil {
		return id, err
	}

	if err = h.writeLocked(websocket.MessageText, []byte("451-"+strconv.Itoa(id)+string(header))); err != nil {
		return id, err
	}

	return id, h.writeLocked(websocket.MessageBinary, payload)
}

func (h *session) unwait(id int) {
	h.writeMu.Lock()
	delete(h.waiting, id)
	h.writeMu.Unlock()
}

// acked hands the camera's ack of one event to whoever waits for it.
func (h *session) acked(ack, body string) {
	id, err := strconv.Atoi(ack)
	if err != nil {
		return
	}

	var codes []int
	if json.Unmarshal([]byte(body), &codes) != nil || len(codes) == 0 {
		return
	}

	h.writeMu.Lock()
	waiter := h.waiting[id]
	delete(h.waiting, id)
	h.writeMu.Unlock()

	if waiter != nil {
		waiter <- codes[0]
	}
}

// engine reads one Engine.IO packet. It answers false to end the session.
func (h *session) engine(msg string) bool {
	if msg == "" {
		return true
	}

	switch msg[0] {
	case '1':
		return false
	case '2': // a ping, which an Engine.IO 3 client sends
		h.text("3" + msg[1:])
	case '4':
		return h.socket(msg[1:])
	}

	return true
}

// socket reads one Socket.IO packet.
func (h *session) socket(p string) bool {
	if p == "" {
		return true
	}

	kind, rest := p[0], p[1:]

	attachments := 0
	if kind == '5' || kind == '6' {
		count, after, _ := strings.Cut(rest, "-")
		attachments, _ = strconv.Atoi(count)
		rest = after
	}

	if strings.HasPrefix(rest, "/") {
		_, rest, _ = strings.Cut(rest, ",")
	}

	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}

	ack, body := rest[:i], rest[i:]

	switch kind {
	case '0':
		h.text(fmt.Sprintf(`40{"sid":%q}`, h.id))
	case '1':
		return false
	case '2', '5':
		var args []any
		if json.Unmarshal([]byte(body), &args) != nil {
			return true
		}

		if attachments > 0 {
			h.pending, h.ack, h.want, h.bins = args, ack, attachments, nil

			return true
		}

		return h.complete(args, ack, nil)
	case '3':
		h.acked(ack, body)
	}

	return true
}

func (h *session) binary(b []byte) {
	if h.want == 0 {
		return
	}

	h.bins = append(h.bins, b)
	if len(h.bins) < h.want {
		return
	}

	args, ack, bins := h.pending, h.ack, h.bins
	h.pending, h.want, h.bins = nil, 0, nil

	if !h.complete(args, ack, bins) {
		_ = h.conn.Close(websocket.StatusPolicyViolation, "not authorized")
	}
}

// complete handles one whole event. It answers false to end the session.
func (h *session) complete(args []any, ack string, bins [][]byte) bool {
	if len(args) == 0 {
		return true
	}

	event, _ := args[0].(string)

	var payload []byte
	if len(args) > 1 {
		payload = attachment(args[1], bins)
	}

	if event == AuthEvent {
		key, ok := h.server.handler.Authenticate(payload)
		if !ok {
			h.reply(ack, AckNotAuthorized)
			h.server.log.Info("a camera's session was refused", "session", h.id)

			return false
		}

		h.reply(ack, AckOK)

		if h.key != key {
			h.key = key
			h.server.adopt(key, h)
		}

		h.server.handler.Ready(key)

		return true
	}

	// Nothing but authentication is read from a session that has not
	// authenticated.
	if h.key == "" {
		h.reply(ack, AckNotAuthorized)

		return true
	}

	h.reply(ack, AckOK)
	h.server.handler.Event(h.key, event, payload)

	return true
}

func (h *session) reply(ack string, code int) {
	if ack != "" {
		h.text("43" + ack + "[" + strconv.Itoa(code) + "]")
	}
}

// attachment is the bytes an argument carries: the binary attachment a
// placeholder points at, or nothing.
func attachment(arg any, bins [][]byte) []byte {
	m, ok := arg.(map[string]any)
	if !ok || m["_placeholder"] != true {
		return nil
	}

	n, ok := m["num"].(float64)
	if !ok || int(n) < 0 || int(n) >= len(bins) {
		return nil
	}

	return bins[int(n)]
}
