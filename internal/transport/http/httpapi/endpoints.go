package httpapi

import (
	"context"
)

// The bounds a declared endpoint is held to. They are exported because the
// store that persists a row is held to the same numbers, and a second copy of
// a bound is a second thing to keep right. The address bound is in the schema
// as a literal too, because a struct tag cannot hold a call, and the schema is
// what a generated client reads.
const (
	MaxEndpointAddressBytes     = 253
	MaxEndpointOptions          = 16
	MaxEndpointOptionKeyBytes   = 63
	MaxEndpointOptionValueBytes = 253
)

// EndpointStore is the addresses this server dials: one row per machine the
// host reaches out to, kept in the same machine-owned overlay the resource
// instances and the gcode presets are kept in and fenced by the same version
// token. It is declared here because this is where it is consumed.
//
// A printer that dials in needs no row here. This is the other direction: a
// machine that listens and waits to be connected to. A customer adds it from
// the interface or from the API, without editing a file. The dial supervisor
// watches the same list, so an accepted write starts a dial loop with no
// restart.
//
// Reading takes view_resource on the driver kind and writing takes
// configure_resource on it. There is no second object type: an endpoint is one
// address a connector dials, so whoever may configure this deployment's
// connectors is whoever may add a machine for them to dial.
type EndpointStore interface {
	// ListEndpoints returns every endpoint the caller may read, ordered by
	// name. A caller holding no view_resource on the driver kind is answered an
	// empty listing and not a refusal. The resource listing and the preset
	// listing do the same.
	ListEndpoints(ctx context.Context, principal string) (EndpointListing, error)

	// ApplyEndpoint writes one endpoint into the overlay. It validates before
	// it writes: a rejected declaration leaves the file untouched.
	//
	// Two refusals belong to the store, because only the store knows the
	// current state: a failed precondition is httpfault.VersionConflict,
	// and a name nothing carries is the same refusal a caller who may read no
	// endpoint is given.
	ApplyEndpoint(
		ctx context.Context,
		principal string,
		name string,
		desired EndpointSpec,
		pre Precondition,
	) (EndpointResult, error)

	// DeleteEndpoint removes one from the overlay. The dial loop for it stops
	// on the next sync.
	DeleteEndpoint(ctx context.Context, principal string, name string, pre Precondition) error
}

// EndpointSpec is what a caller declares: which connector dials, what it dials,
// whether it dials at all, and whatever else that dialect needs.
//
// What is at the address is not checked when the row is written. The machine
// may be switched off, and the dial loop is what finds out, so a write that
// answers 201 says the row was stored and never that a printer answered.
type EndpointSpec struct {
	// Connector is the instance under core.connectors whose dialect speaks to
	// this machine. A name this build does not carry is stored and reported by
	// the dial supervisor, because the endpoint list survives a configuration
	// change that drops a connector.
	Connector string `json:"connector" maxLength:"63" minLength:"1" doc:"The connector instance that dials this address, by the name it is declared under in core.connectors."`

	// Address is handed to the driver as it was written. Its meaning is the
	// dialect's: a host and port, a URL, or a serial device path.
	Address string `json:"address" maxLength:"253" minLength:"1" doc:"What the connector dials. The dialect decides what it means: a host and port, a URL, or a serial device path. An address carries no user name or password."`

	// Enabled has no default on the wire. A row that arrived without it would
	// be stored switched off, and a caller adding a printer would watch nothing
	// happen with nothing refused.
	Enabled bool `json:"enabled" doc:"Whether the dial loop runs for this address. An endpoint switched off is still declared and still listed."`

	// Options are the dialect's own settings. The host stores them, bounds
	// them and hands them over without reading any of them.
	Options map[string]string `json:"options,omitempty" doc:"Settings this dialect reads and the host does not: at most 16 entries, each name at most 63 bytes and each value at most 253."`
}

// ConnectorEndpoint is one declared address as the API returns it.
type ConnectorEndpoint struct {
	EndpointSpec

	// Name is the operator-chosen name, unique across endpoints. It is the
	// path, and it is in the body too so a listing is usable without reparsing
	// links.
	Name string `json:"name" doc:"The operator-chosen name, unique across endpoints."`

	// Version is the fencing token, which covers the whole overlay rather than
	// this endpoint alone: a write rewrites that file whole, so a write to any
	// endpoint, preset or resource instance moves it.
	Version string `json:"version" doc:"Fencing token, covering the whole overlay. Quote the ETag in If-Match on a write."`
}

// EndpointListing is the collection as one artefact, with the token every entry
// under it carries.
type EndpointListing struct {
	Items   []ConnectorEndpoint
	Version string
}

// EndpointResult is what a write produced. Created separates the PUT that
// brought an endpoint into existence, which answers 201 with a Location, from
// the one that replaced it.
type EndpointResult struct {
	Endpoint ConnectorEndpoint
	Created  bool
}
