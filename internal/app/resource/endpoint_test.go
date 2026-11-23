package resource_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/internal/app/resource"
	"github.com/prusa3d/prusa-connect/internal/authz"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// bench is the endpoint every case below writes unless it needs another shape,
// in the shape the overlay stores it.
func bench() resource.Endpoint {
	return resource.Endpoint{
		Name:      "bench",
		Connector: "moonraker",
		Address:   "192.0.2.10:7125",
		Enabled:   true,
		Options:   map[string]string{"api_key": "unused"},
	}
}

// benchSpec is the same endpoint as a caller declares it, which carries no name
// and no version: both are the server's.
func benchSpec() httpapi.EndpointSpec {
	return httpapi.EndpointSpec{
		Connector: "moonraker",
		Address:   "192.0.2.10:7125",
		Enabled:   true,
		Options:   map[string]string{"api_key": "unused"},
	}
}

func (h *harness) createEndpoint(
	name string, desired httpapi.EndpointSpec,
) (httpapi.EndpointResult, error) {
	h.t.Helper()

	return h.store.ApplyEndpoint(h.t.Context(), owner, name, desired,
		httpapi.Precondition{MustNotExist: true})
}

// TestAnAddressLivesInTheOverlayAndNotInTheOperatorsFile is the owner's answer
// of 2026-08-28, held as a property of the store.
//
// A customer adds a printer from the interface and from the API and never edits
// a file, so the address is something this server writes into
// the file it owns. The operator's own document carries no endpoint at all, and
// core.connectors carries no address key for one to be typed under.
func TestAnAddressLivesInTheOverlayAndNotInTheOperatorsFile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.store.Reload(t.Context()))

	before, err := h.store.ListEndpoints(t.Context(), owner)
	require.NoError(t, err)

	written, err := h.createEndpoint("bench", benchSpec())
	require.NoError(t, err)
	require.True(t, written.Created)
	require.Equal(t, "192.0.2.10:7125", written.Endpoint.Address)
	require.Equal(t, "bench", written.Endpoint.Name)
	require.NotEmpty(t, written.Endpoint.Version)

	var overlay struct {
		Endpoints []resource.Endpoint `json:"endpoints"`
	}

	require.NoError(t, json.Unmarshal(h.overlayBytes(), &overlay))
	require.Len(t, overlay.Endpoints, 1)
	require.Equal(t, bench(), overlay.Endpoints[0])

	after, err := h.store.ListEndpoints(t.Context(), owner)
	require.NoError(t, err)
	require.NotEqual(t, before.Version, after.Version,
		"an endpoint write shares the instances' token, so it moves it")

	// The dial loop reads the merged document, which is what a restart reads
	// too: the same list from the same bytes.
	reloaded, err := resource.Load(h.paths)
	require.NoError(t, err)
	require.Equal(t, []resource.Endpoint{bench()}, reloaded.Endpoints)
	require.Empty(t, h.stray(), "a write left a staged file behind")
}

// TestReadingAnAddressTakesViewResourceAndWritingTakesConfigureResource is the
// rule in both directions. The grant is on the driver kind, which is the kind a
// printer connector already answers to: a caller who may configure this
// deployment's connectors is the caller who may add a printer to dial.
func TestReadingAnAddressTakesViewResourceAndWritingTakesConfigureResource(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.store.Reload(t.Context()))

	_, err := h.createEndpoint("bench", benchSpec())
	require.NoError(t, err)

	const reader = "user:reader"

	require.NoError(t, h.graph.Write(t.Context(), h.tuple(ref.KindDriver, authz.RelViewer, reader)))

	listed, err := h.store.ListEndpoints(t.Context(), reader)
	require.NoError(t, err, "a viewer on the driver kind may read the endpoint list")
	require.Len(t, listed.Items, 1)
	require.Equal(t, benchSpec(), listed.Items[0].EndpointSpec)
	require.NotEmpty(t, listed.Version)

	version := listed.Version

	_, err = h.store.ApplyEndpoint(t.Context(), reader, "bench", benchSpec(),
		httpapi.Precondition{IfMatch: []string{version}})
	require.Error(t, err, "a viewer rewrote an address this server dials")
	require.True(t, fault.HasCode(err, authz.CodeAccessDenied()),
		"a viewer who may see the endpoint is told which grant is missing, got %v", err)

	require.Error(t, h.store.DeleteEndpoint(t.Context(), reader, "bench",
		httpapi.Precondition{IfMatch: []string{version}}), "a viewer deleted an address")
}

// TestAPrincipalWithNoGrantOnTheDriverKindLearnsNoAddress is the absence rule.
// An address is where a printer is on somebody's network, so a caller holding
// nothing must not be able to tell one that exists from one that does not.
func TestAPrincipalWithNoGrantOnTheDriverKindLearnsNoAddress(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.store.Reload(t.Context()))

	_, err := h.createEndpoint("bench", benchSpec())
	require.NoError(t, err)

	listed, err := h.store.ListEndpoints(t.Context(), stranger)
	require.NoError(t, err)
	require.Empty(t, listed.Items, "a caller holding nothing was handed an address")
	require.Empty(t, listed.Version, "a caller holding nothing was handed the overlay's token")

	present := h.store.DeleteEndpoint(t.Context(), stranger, "bench", httpapi.Precondition{})
	absent := h.store.DeleteEndpoint(t.Context(), stranger, "nowhere", httpapi.Precondition{})

	require.Error(t, present)
	require.Error(t, absent)
	require.True(t, fault.HasCode(present, httpfault.CodeResourceNotFound()))
	require.Equal(t, absent.Error(), present.Error(),
		"an endpoint that exists answered a stranger differently from one that does not")
}

// TestAnEndpointIsRefusedBeforeAnythingIsWritten holds an earlier design decision's shape for
// this list too: a rejected declaration leaves the file exactly as it was.
func TestAnEndpointIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		name    string
		desired httpapi.EndpointSpec
		at      string
	}{
		"no name": {"", httpapi.EndpointSpec{Connector: "moonraker", Address: "host:1"}, "path.name"},
		"name is not a label": {
			"Bench One", httpapi.EndpointSpec{Connector: "moonraker", Address: "host:1"}, "path.name",
		},
		"no connector": {"bench", httpapi.EndpointSpec{Address: "host:1"}, "body.connector"},
		"no address":   {"bench", httpapi.EndpointSpec{Connector: "moonraker"}, "body.address"},
		"address too long": {
			"bench",
			httpapi.EndpointSpec{Connector: "moonraker", Address: strings.Repeat("a", 254)},
			"body.address",
		},
		"a url with a password": {
			"bench",
			httpapi.EndpointSpec{Connector: "moonraker", Address: "http://user:hunter2@host:1"},
			"body.address",
		},
		"a bare user and password": {
			"bench", httpapi.EndpointSpec{Connector: "moonraker", Address: "user:hunter2@host"}, "body.address",
		},
		"an option value too long": {
			"bench",
			httpapi.EndpointSpec{
				Connector: "moonraker", Address: "host:1",
				Options: map[string]string{"key": strings.Repeat("v", 254)},
			},
			"body.options",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			require.NoError(t, h.store.Reload(t.Context()))

			before := h.overlayBytes()

			_, err := h.createEndpoint(tc.name, tc.desired)
			require.Error(t, err)

			var carried *fault.Fault
			require.ErrorAs(t, err, &carried)
			require.Equal(t, httpfault.CodeRequestInvalid(), carried.Code())

			location, ok := carried.Params().Get("location")
			require.True(t, ok, "the refusal names no member")
			require.Equal(t, tc.at, location.String())
			require.NotContains(t, err.Error(), "hunter2", "the refusal echoed a credential")

			require.Equal(t, string(before), string(h.overlayBytes()),
				"a refused endpoint changed the file")
		})
	}
}

// TestAnAddressWithoutUserInformationIsAccepted keeps the refusal of a
// credential in the address from catching the forms that carry none.
func TestAnAddressWithoutUserInformationIsAccepted(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"host:7125", "https://host:7125/path", "/dev/ttyUSB0", "http://[fe80::1%25eth0]:7125"} {
		h := newHarness(t)
		require.NoError(t, h.store.Reload(t.Context()))

		desired := benchSpec()
		desired.Address = address

		_, err := h.createEndpoint("bench", desired)
		require.NoError(t, err, address)
	}
}

// TestTheDialLoopIsToldEveryTimeTheAddressListMoves is what makes adding a
// printer a call rather than a restart. The watcher is handed the list as it
// stands the moment it is installed, and again on every accepted write.
func TestTheDialLoopIsToldEveryTimeTheAddressListMoves(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.store.Reload(t.Context()))

	told := &recordingWatcher{}
	h.store.WatchEndpoints(t.Context(), told)

	require.Equal(t, [][]resource.Endpoint{{}}, told.seen(),
		"the watcher was not handed the list it starts from")

	_, err := h.createEndpoint("bench", benchSpec())
	require.NoError(t, err)

	require.Equal(t, [][]resource.Endpoint{{}, {bench()}}, told.seen(),
		"a write did not reach the dial loop")

	// A reload of the same bytes is not a change. Restarting every dial loop
	// because somebody read the file would drop every live printer.
	require.NoError(t, h.store.Reload(t.Context()))
	require.Len(t, told.seen(), 2, "reloading the same list restarted the dial loops")

	listed, err := h.store.ListEndpoints(t.Context(), owner)
	require.NoError(t, err)

	require.NoError(t, h.store.DeleteEndpoint(t.Context(), owner, "bench",
		httpapi.Precondition{IfMatch: []string{listed.Version}}))

	require.Equal(t, [][]resource.Endpoint{{}, {bench()}, {}}, told.seen(),
		"a removal did not reach the dial loop")
}

// recordingWatcher keeps every list it was handed, in order.
type recordingWatcher struct {
	mu    sync.Mutex
	lists [][]resource.Endpoint
}

func (w *recordingWatcher) SyncEndpoints(_ context.Context, endpoints []resource.Endpoint) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.lists = append(w.lists, endpoints)
}

func (w *recordingWatcher) seen() [][]resource.Endpoint {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([][]resource.Endpoint(nil), w.lists...)
}
