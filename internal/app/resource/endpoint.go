package resource

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/prusa3d/prusa-connect/internal/authz"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
)

// The bounds of a stored endpoint. An address reaches a dial loop and a log
// field, so the store bounds it before any driver parses it. The numbers are
// the transport's constants, so the published schema and the store's refusals
// agree.
const (
	maxAddressBytes    = httpapi.MaxEndpointAddressBytes
	maxEndpointOptions = httpapi.MaxEndpointOptions
	maxOptionKeyBytes  = httpapi.MaxEndpointOptionKeyBytes
	maxOptionValBytes  = httpapi.MaxEndpointOptionValueBytes
)

var _ httpapi.EndpointStore = (*Store)(nil)

// endpointKind is the authorization object every endpoint grant is written on.
//
// It is the driver kind, the resource kind of a printer connector
// (internal/kernel/ref, KindDriver). An endpoint is one address a connector
// dials, so a caller who may configure the connectors of this deployment may
// also add a printer to dial. No second object type is needed.
const endpointKind = ref.KindDriver

// endpointObject is the name of the object in a refusal a client reads. It is
// not the name of the driver kind: the missing thing is an endpoint, and a
// message naming the kind would make a reader look for a resource instance.
const endpointObject = "connector_endpoint"

// Endpoint is one address this server dials, as the machine-owned overlay holds
// it.
//
// It is kept in the overlay and never in the operator's file. A customer adds
// a printer from the interface and from the API and never edits a file, so
// the server writes the address and an operator does not type it under
// core.connectors.
type Endpoint struct {
	// Name is the operator-chosen name for this address, unique across the list.
	Name string `json:"name"`
	// Connector is the instance name under core.connectors whose dialect dials
	// this address.
	Connector string `json:"connector"`
	// Address is what the driver gets as connectorapi.Endpoint.Address. The
	// dialect defines its meaning: a host and port, a URL, or a serial device
	// path.
	Address string `json:"address"`
	// Enabled is whether the dial loop runs for it. An endpoint switched off is
	// still declared and still readable.
	Enabled bool `json:"enabled"`
	// Options are dialect-specific settings the host does not interpret.
	Options map[string]string `json:"options,omitempty"`
}

// Same reports whether two entries declare the same dial.
//
// Endpoint holds a map, so == cannot compare it. A dial loop is restarted when
// its declaration changes, so the comparison includes the options: the same
// address with a changed dialect option is dialled in a different way.
func (e Endpoint) Same(other Endpoint) bool {
	return e.Name == other.Name &&
		e.Connector == other.Connector &&
		e.Address == other.Address &&
		e.Enabled == other.Enabled &&
		maps.Equal(e.Options, other.Options)
}

// EndpointWatcher receives the endpoint list every time it changes, so an
// added printer is dialled after the write and needs no restart. It is
// declared here because this package calls it, and the dial supervisor in
// internal/app satisfies it.
type EndpointWatcher interface {
	// SyncEndpoints takes the whole list and not one entry. The watcher owns
	// a goroutine per endpoint, so it must learn about a removal as well as
	// an addition. With the whole list in one call, the two cannot arrive in
	// the wrong order.
	SyncEndpoints(ctx context.Context, endpoints []Endpoint)
}

// WatchEndpoints sets the watcher and passes it the current list. The store
// has one watcher, because two watchers would both run the same dial loops.
func (s *Store) WatchEndpoints(ctx context.Context, watcher EndpointWatcher) {
	s.mu.Lock()
	s.endpoints = watcher
	current := slices.Clone(s.current.Endpoints)
	s.mu.Unlock()

	if watcher != nil {
		watcher.SyncEndpoints(ctx, current)
	}
}

// ListEndpoints returns every endpoint the caller may read, ordered by name.
func (s *Store) ListEndpoints(
	ctx context.Context, principal string,
) (httpapi.EndpointListing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	listing := httpapi.EndpointListing{Items: []httpapi.ConnectorEndpoint{}}

	who, err := s.subject(principal)
	if err != nil {
		return httpapi.EndpointListing{}, err
	}

	// The graph is asked before the disk is read, as in every other read of
	// this store, so the access answer cannot depend on the configuration.
	allowed, err := s.mayReadEndpoints(ctx, who)
	if err != nil {
		return httpapi.EndpointListing{}, err
	}

	read, err := s.readEndpoints(ctx, principal, "list endpoints")
	if err != nil {
		return httpapi.EndpointListing{}, err
	}

	// A caller who may read no endpoint gets no token, for the same reason as
	// in ListPresets: the version covers the whole overlay, so it would show
	// when a configuration they may not read changes.
	if !allowed {
		return listing, nil
	}

	listing.Version = read.version

	for _, stored := range read.endpoints {
		listing.Items = append(listing.Items, stored.project(read.version))
	}

	return listing, nil
}

// ApplyEndpoint writes one endpoint into the overlay. Every refusal happens
// before anything is written, so a rejected endpoint leaves the file
// unchanged.
func (s *Store) ApplyEndpoint(
	ctx context.Context,
	principal string,
	name string,
	desired httpapi.EndpointSpec,
	pre httpapi.Precondition,
) (httpapi.EndpointResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	write, err := s.beginEndpoint(ctx, principal, name, pre, true)
	if err != nil {
		return httpapi.EndpointResult{}, err
	}

	stored, err := validEndpoint(name, desired)
	if err != nil {
		return httpapi.EndpointResult{}, err
	}

	if pre.DryRun {
		return httpapi.EndpointResult{
			Endpoint: stored.project(write.version), Created: !write.exists,
		}, nil
	}

	next := write.overlay
	next.Endpoints = withEndpoint(write.overlay.Endpoints, stored)

	version, err := s.commit(ctx, next, write.read)
	if err != nil {
		return httpapi.EndpointResult{}, err
	}

	return httpapi.EndpointResult{
		Endpoint: stored.project(version), Created: !write.exists,
	}, nil
}

// DeleteEndpoint removes one endpoint from the overlay. Its dial loop stops
// at the sync that follows the write, so removing a printer needs no restart.
func (s *Store) DeleteEndpoint(
	ctx context.Context, principal string, name string, pre httpapi.Precondition,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	write, err := s.beginEndpoint(ctx, principal, name, pre, false)
	if err != nil {
		return err
	}

	if pre.DryRun {
		return nil
	}

	next := write.overlay
	next.Endpoints = withoutEndpoint(write.overlay.Endpoints, name)

	_, err = s.commit(ctx, next, write.read)

	return err
}

// pendingEndpoint is one endpoint write that passed its refusals and is not
// committed yet.
type pendingEndpoint struct {
	read    inputs
	overlay overlayFile
	version string
	exists  bool
}

// beginEndpoint authorizes the write and applies the two refusals of the
// store: a name that does not exist, and a token that does not match the
// current version.
//
// The graph is asked before the disk is read, so an access refusal cannot
// depend on the file. A caller who may not read endpoints therefore gets the
// same answer as for an endpoint that does not exist.
func (s *Store) beginEndpoint(
	ctx context.Context, principal string, name string, pre httpapi.Precondition, mayCreate bool,
) (pendingEndpoint, error) {
	s.log.DebugContext(ctx, "endpoint write",
		"op", writeName(mayCreate), "principal", principal, "endpoint", name)

	who, err := s.subject(principal)
	if err != nil {
		return pendingEndpoint{}, err
	}

	if err = s.authorizeEndpointWrite(ctx, who, name, mayCreate); err != nil {
		return pendingEndpoint{}, err
	}

	read, err := readInputs(s.paths)
	if err != nil {
		return pendingEndpoint{}, s.internal("read the configuration", err)
	}

	doc, err := document(read)
	if err != nil {
		return pendingEndpoint{}, s.internal("parse the configuration", err)
	}

	overlay, err := parseOverlay(read.overlay)
	if err != nil {
		return pendingEndpoint{}, s.internal("parse the overlay", err)
	}

	_, exists := findEndpoint(overlay.Endpoints, name)

	switch {
	case !exists && !pre.MustNotExist && len(pre.IfNoneMatch) == 0:
		return pendingEndpoint{}, endpointAbsent(name)
	case !pre.Holds(exists, doc.Version):
		return pendingEndpoint{}, httpfault.VersionConflict(httpfault.Reference(endpointObject, name), doc.Version)
	}

	return pendingEndpoint{
		read: read, overlay: overlay, version: doc.Version, exists: exists,
	}, nil
}

// authorizeEndpointWrite is the access check for an endpoint write. It needs
// configure_resource on the driver kind. A caller who can neither read nor
// configure endpoints gets the refusal for an endpoint that does not exist.
func (s *Store) authorizeEndpointWrite(
	ctx context.Context, who authz.Subject, name string, mayCreate bool,
) error {
	seen, err := s.mayReadEndpoints(ctx, who)
	if err != nil {
		return err
	}

	writes, err := s.writable(ctx, who, endpointKind)
	if err != nil {
		return err
	}

	instance, err := ref.New(endpointKind, name)
	if err != nil {
		// The name is checked after the grants. A caller with no grant gets
		// the not-found answer for a malformed name too, and learns nothing
		// from it.
		if !seen && !writes {
			return endpointAbsent(name)
		}

		return httpfault.RequestInvalid("path.name",
			"expected a DNS-1123 label: lowercase letters, digits and hyphens", err.Error())
	}

	switch {
	case writes && (seen || mayCreate):
		return nil
	case seen:
		return authz.Denied(authz.ActionConfigureResource, instance)
	default:
		return endpointAbsent(name)
	}
}

// mayReadEndpoints is the access check for a read: view_resource on the
// driver kind.
func (s *Store) mayReadEndpoints(ctx context.Context, who authz.Subject) (bool, error) {
	object, err := authz.NewObject(authz.TypeResourceKind, endpointKind.String())
	if err != nil {
		return false, s.internal("name the driver kind", err)
	}

	allowed, err := s.graph.Can(ctx, who, authz.ActionViewResource, object)
	if err != nil {
		return false, s.internal("read what the caller may see", err)
	}

	return allowed, nil
}

// endpointRead is the overlay as one read saw it.
type endpointRead struct {
	endpoints []Endpoint
	version   string
}

// readEndpoints serves what is on disk and not what was last reconciled, for
// the same reason as reread: a client whose write was refused for an old
// token must be able to read the current token.
func (s *Store) readEndpoints(ctx context.Context, principal, op string) (endpointRead, error) {
	s.log.DebugContext(ctx, "endpoint read", "op", op, "principal", principal)

	read, err := readInputs(s.paths)
	if err != nil {
		return endpointRead{}, s.internal("read the configuration", err)
	}

	overlay, err := parseOverlay(read.overlay)
	if err != nil {
		return endpointRead{}, s.internal("parse the overlay", err)
	}

	endpoints, _ := declaredEndpoints(overlay)

	return endpointRead{endpoints: endpoints, version: read.version}, nil
}

// declaredEndpoints is the overlay's list, copied and ordered by name, so two
// reads of the same bytes give the same answer.
//
// A stored row whose address holds a credential is neither listed nor
// dialled, and its failure names the endpoint without the address.
func declaredEndpoints(overlay overlayFile) ([]Endpoint, []Failure) {
	out := make([]Endpoint, 0, len(overlay.Endpoints))

	var failures []Failure

	for _, stored := range overlay.Endpoints {
		if holdsUserInfo(stored.Address) {
			failures = append(failures, Failure{
				Key: "endpoints." + stored.Name,
				Err: fmt.Errorf("%w: endpoint %s: the address carries a user name or password",
					ErrConfigInvalid, stored.Name),
			})

			continue
		}

		copied := stored
		copied.Options = copyOptions(stored.Options)
		out = append(out, copied)
	}

	sortEndpoints(out)

	return out, failures
}

// holdsUserInfo is true for an address that may carry a credential, written
// plain or percent-encoded. No dialect takes "@" or "%40" in an address, and
// refusing them keeps a credential out of the overlay, the listing and the log.
func holdsUserInfo(address string) bool {
	return strings.Contains(address, "@") || strings.Contains(address, "%40")
}

// validEndpoint checks a declared endpoint and returns it in the stored form.
// It does not check what is at the address: the machine may be switched off,
// and the dial loop finds that out.
func validEndpoint(name string, desired httpapi.EndpointSpec) (Endpoint, error) {
	if _, err := ref.New(endpointKind, name); err != nil {
		return Endpoint{}, httpfault.RequestInvalid("path.name",
			"expected a DNS-1123 label: lowercase letters, digits and hyphens", err.Error())
	}

	if err := ref.ValidateID(desired.Connector); err != nil {
		return Endpoint{}, httpfault.RequestInvalid("body.connector",
			"expected the name of a connector declared under core.connectors", err.Error())
	}

	address := strings.TrimSpace(desired.Address)
	if address == "" || len(address) > maxAddressBytes {
		return Endpoint{}, httpfault.RequestInvalid("body.address",
			"expected an address of 1 to "+strconv.Itoa(maxAddressBytes)+" bytes",
			"the address is "+strconv.Itoa(len(address))+" bytes")
	}

	// The detail does not echo the address, so a refused credential is not
	// logged either.
	if holdsUserInfo(address) {
		return Endpoint{}, httpfault.RequestInvalid("body.address",
			"expected an address without a user name or password",
			`the address holds "@" or "%40", which is refused wherever it stands`)
	}

	options, err := validOptions(desired.Options)
	if err != nil {
		return Endpoint{}, err
	}

	return Endpoint{
		Name:      name,
		Connector: desired.Connector,
		Address:   address,
		Enabled:   desired.Enabled,
		Options:   options,
	}, nil
}

// project is the stored endpoint as the API returns it, with the version of
// the read or the write that produced it.
func (e Endpoint) project(version string) httpapi.ConnectorEndpoint {
	return httpapi.ConnectorEndpoint{
		EndpointSpec: httpapi.EndpointSpec{
			Connector: e.Connector,
			Address:   e.Address,
			Enabled:   e.Enabled,
			Options:   copyOptions(e.Options),
		},
		Name:    e.Name,
		Version: version,
	}
}

func validOptions(declared map[string]string) (map[string]string, error) {
	if len(declared) > maxEndpointOptions {
		return nil, httpfault.RequestInvalid("body.options",
			"expected at most "+strconv.Itoa(maxEndpointOptions)+" options",
			"options: "+strconv.Itoa(len(declared)))
	}

	for key, value := range declared {
		if key == "" || len(key) > maxOptionKeyBytes {
			return nil, httpfault.RequestInvalid("body.options",
				"expected an option name of 1 to "+strconv.Itoa(maxOptionKeyBytes)+" bytes",
				"one option name is "+strconv.Itoa(len(key))+" bytes")
		}

		if len(value) > maxOptionValBytes {
			return nil, httpfault.RequestInvalid("body.options",
				"expected an option value of at most "+strconv.Itoa(maxOptionValBytes)+" bytes",
				"option "+strconv.Quote(key)+" is "+strconv.Itoa(len(value))+" bytes")
		}
	}

	return copyOptions(declared), nil
}

// withEndpoint returns the list with one endpoint written. It replaces an
// entry with the same name.
func withEndpoint(endpoints []Endpoint, stored Endpoint) []Endpoint {
	next := make([]Endpoint, 0, len(endpoints)+1)
	replaced := false

	for _, existing := range endpoints {
		if existing.Name == stored.Name {
			next = append(next, stored)
			replaced = true

			continue
		}

		next = append(next, existing)
	}

	if !replaced {
		next = append(next, stored)
	}

	return next
}

// withoutEndpoint returns the list without the endpoint of one name.
func withoutEndpoint(endpoints []Endpoint, name string) []Endpoint {
	next := make([]Endpoint, 0, len(endpoints))

	for _, existing := range endpoints {
		if existing.Name == name {
			continue
		}

		next = append(next, existing)
	}

	return next
}

func findEndpoint(endpoints []Endpoint, name string) (Endpoint, bool) {
	for _, stored := range endpoints {
		if stored.Name == name {
			return stored, true
		}
	}

	return Endpoint{}, false
}

func copyOptions(options map[string]string) map[string]string {
	if len(options) == 0 {
		return nil
	}

	out := make(map[string]string, len(options))
	maps.Copy(out, options)

	return out
}

func sortEndpoints(endpoints []Endpoint) {
	slices.SortFunc(endpoints, func(a, b Endpoint) int { return strings.Compare(a.Name, b.Name) })
}

func sameEndpoint(a, b Endpoint) bool { return a.Same(b) }

// endpointAbsent is the refusal for an endpoint that does not exist and for a
// caller who may not read endpoints. Both cases call this function, so the
// two answers are always the same.
func endpointAbsent(name string) error {
	return httpfault.ResourceNotFound(httpfault.Reference(endpointObject, name))
}
