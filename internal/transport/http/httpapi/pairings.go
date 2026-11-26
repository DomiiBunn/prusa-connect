package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prusa3d/prusa-connect/internal/authz"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// The pairing paths. They sit under the login ceremony's prefix because they
// are the same subject: how a caller comes to hold a principal on this server.
const (
	pairingsPath = "/api/v1/auth/pairings"
	pairingPath  = "/api/v1/auth/pairings/{provider}/{subject}"
)

// Pairings maps the subjects an identity provider names onto local principals.
// A bearer token another issuer signed, such as a Prusa Account token, verifies
// offline and names a subject in the issuer's own space. The pairing says who
// that subject is here. It is declared here because this is where it is
// consumed, and satisfied by the composition root, which is the only thing
// that may name the store.
//
// Every route takes configure_resource on the auth resource kind, which is what
// administering this server's identity means in the graph. There is no
// self-service pairing: a subject that could pair itself would be a subject
// choosing its own principal.
type Pairings interface {
	// Pair writes one pairing. A subject already paired is refused; changing
	// whom a subject acts as is an unpair and a pair.
	Pair(ctx context.Context, spec NewPairing) (Pairing, error)
	// List returns every pairing, or one provider's when provider is not
	// empty, ordered by provider then subject.
	List(ctx context.Context, provider string) ([]Pairing, error)
	// Unpair removes one pairing. The next token the subject presents is
	// refused as unpaired.
	Unpair(ctx context.Context, provider, subject string) error
	// Fence reads the principal's generation. A pairing reads it before it
	// checks the session and saves it, so a password change between the
	// check and the save leaves the pairing refused.
	Fence(ctx context.Context, principal string) (int64, error)
}

// Pairing is one pairing as this surface renders it.
type Pairing struct {
	Provider  string    `json:"provider"   doc:"The identity provider type that vouches for the subject, as its plugin registers it."`
	Subject   string    `json:"subject"    doc:"The subject as the token's sub claim spells it."`
	Principal string    `json:"principal"  doc:"The principal the subject acts as on this server, in tuple spelling."`
	Label     string    `json:"label"      doc:"A note the administrator wrote for the listing."`
	CreatedAt time.Time `json:"created_at" doc:"When the pairing was written."`
}

// NewPairing is what a caller supplies to pair a subject. The principal is
// already checked: the handler decides it names a person or a service account
// before the store sees it.
type NewPairing struct {
	Provider  string
	Subject   string
	Principal string
	Label     string
	// Generation is what the handler read before its session check. Nil reads
	// the principal's current generation at the save.
	Generation *int64
	// CreatedBy is the caller who wrote the pairing, in tuple spelling, and
	// CreatorGeneration the caller's generation read before the session
	// check. A password change of that person ends the pairing.
	CreatedBy         string
	CreatorGeneration *int64
	// RootCreator is the person at the top of the chain the caller's
	// credential came through, and RootGeneration that person's generation
	// read before the session check. The root's password change ends the
	// pairing, however long the chain.
	RootCreator    string
	RootGeneration *int64
}

// pairingBody is the create body.
type pairingBody struct {
	Provider  string `json:"provider"        doc:"The identity provider type that vouches for the subject, as its plugin registers it."                                                                                        maxLength:"64"  minLength:"1" example:"prusa-account"`
	Subject   string `json:"subject"         doc:"The subject as the token's sub claim spells it."                                                                                                                             maxLength:"255" minLength:"1" example:"8f1e1b2c-0000-4000-8000-abcdefabcdef"`
	Principal string `json:"principal"       doc:"The principal the subject acts as, in tuple spelling: user:<id> or service_account:<id>. It is not created here; the grants written for it are what the subject then holds." maxLength:"128" minLength:"1" example:"user:operator"`
	Label     string `json:"label,omitempty" doc:"A note for the listing: whose account this is."                                                                                                                              maxLength:"128"`
}

type createPairingInput struct {
	Body pairingBody
}

type createPairingOutput struct {
	Status int
	Body   Pairing
}

type listPairingsInput struct {
	Provider string `query:"provider" doc:"Only this provider's pairings. Empty lists every provider's." maxLength:"64" example:"prusa-account"`
}

type listPairingsOutput struct {
	Body struct {
		Items []Pairing `json:"items" doc:"The pairings this server holds, ordered by provider then subject."`
	}
}

// PairingRef is the pairing a path names. It is exported because Huma binds
// parameters by reflection and cannot write into an unexported embedded field.
type PairingRef struct {
	Provider string `path:"provider" doc:"The identity provider type, as the listing renders it." maxLength:"64"  example:"prusa-account"`
	Subject  string `path:"subject"  doc:"The subject, as the listing renders it."                maxLength:"255" example:"8f1e1b2c-0000-4000-8000-abcdefabcdef"`
}

type deletePairingInput struct {
	PairingRef
}

type deletePairingOutput struct {
	Status int
}

// registerPairings publishes the pairing surface. The operations are registered
// whatever the process was wired with, so the document a client generates from
// does not depend on whether this deployment verifies any bearer token.
func registerPairings(api huma.API, cfg Config) {
	registerCreatePairing(api, cfg)
	registerListPairings(api, cfg)
	registerDeletePairing(api, cfg)
}

func registerCreatePairing(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "create-pairing",
		Method:      http.MethodPost,
		Path:        pairingsPath,
		Summary:     "Pair a provider's subject to a principal",
		Description: "Writes the pairing a bearer token needs. A Prusa Account token verifies offline " +
			"against the issuer's published keys and names a subject; until an administrator pairs " +
			"that subject here, every request carrying it is refused with BEARER_UNPAIRED. The " +
			"pairing names a person or a service account in tuple spelling, and the token then acts " +
			"with that principal's grants. Nothing is created: the principal is whatever the graph " +
			"already says about it. Takes configure_resource on the auth resource kind. " +
			"It also takes a sign-in, which is a session or a Prusa Account token that a pairing " +
			"names a person with. An api credential, and a token paired to a service account, are " +
			"refused with SESSION_REQUIRED.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity,
		},
	}, func(ctx context.Context, in *createPairingInput) (*createPairingOutput, error) {
		if err := requireSignedInAccountAdmin(ctx, cfg); err != nil {
			return nil, err
		}

		principal, err := pairablePrincipal(in.Body.Principal)
		if err != nil {
			return nil, err
		}

		creator, err := caller(ctx)
		if err != nil {
			return nil, err
		}

		// Read before a second session check, so a password change of the
		// principal or of the caller after the first check leaves the pairing
		// refused.
		generation, err := cfg.Pairings.Fence(ctx, principal.String())
		if err != nil {
			return nil, serverFault(err, "read the principal's generation")
		}

		creatorGeneration, err := cfg.Pairings.Fence(ctx, creator.String())
		if err != nil {
			return nil, serverFault(err, "read the caller's generation")
		}

		root := callerRoot(ctx, cfg, creator)

		rootGeneration, err := cfg.Pairings.Fence(ctx, root)
		if err != nil {
			return nil, serverFault(err, "read the generation of the caller's root")
		}

		if !cfg.Credentials.SignedIn(requestFrom(ctx)) {
			return nil, fault.AuthSessionRequired.New("writing a pairing takes a sign-in as a person")
		}

		paired, err := cfg.Pairings.Pair(ctx, NewPairing{
			Provider:   in.Body.Provider,
			Subject:    in.Body.Subject,
			Principal:  principal.String(),
			Label:      in.Body.Label,
			Generation: &generation,

			CreatedBy:         creator.String(),
			CreatorGeneration: &creatorGeneration,

			RootCreator:    root,
			RootGeneration: &rootGeneration,
		})
		if err != nil {
			return nil, serverFault(err, "write the pairing")
		}

		return &createPairingOutput{Status: http.StatusCreated, Body: paired}, nil
	})
}

func registerListPairings(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "list-pairings",
		Method:      http.MethodGet,
		Path:        pairingsPath,
		Summary:     "List the paired subjects",
		Description: "Returns every pairing this server holds, or one provider's: which subject acts as " +
			"which principal, and when it was paired. Takes configure_resource on the auth resource kind. " +
			"Any credential that carries the grant will do, an api credential included.",
		Tags:   []string{"auth"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, func(ctx context.Context, in *listPairingsInput) (*listPairingsOutput, error) {
		if err := requireAccountAdmin(ctx, cfg); err != nil {
			return nil, err
		}

		items, err := cfg.Pairings.List(ctx, in.Provider)
		if err != nil {
			return nil, serverFault(err, "list the pairings")
		}

		out := &listPairingsOutput{}
		out.Body.Items = make([]Pairing, 0, len(items))
		out.Body.Items = append(out.Body.Items, items...)

		return out, nil
	})
}

func registerDeletePairing(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "delete-pairing",
		Method:      http.MethodDelete,
		Path:        pairingPath,
		Summary:     "Unpair a provider's subject",
		Description: "Removes the pairing, so the next token the subject presents is refused as " +
			"unpaired. A token already accepted on an open stream is refused at the stream's next " +
			"re-check. Takes configure_resource on the auth resource kind. " +
			"Any credential that carries the grant will do, an api credential included.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		},
	}, func(ctx context.Context, in *deletePairingInput) (*deletePairingOutput, error) {
		if err := requireAccountAdmin(ctx, cfg); err != nil {
			return nil, err
		}

		if err := cfg.Pairings.Unpair(ctx, in.Provider, in.Subject); err != nil {
			return nil, serverFault(err, "remove the pairing")
		}

		return &deletePairingOutput{Status: http.StatusNoContent}, nil
	})
}

// pairablePrincipal reads the principal a pairing names and refuses one a
// bearer token could never speak for. Only a person and a service account hold
// a credential; a device, a sink and a node authenticate another way, a group
// presents nothing, and a userset names a position and not a holder. A
// person and a device are never linked, so a device cannot be named here.
func pairablePrincipal(spelling string) (authz.Subject, error) {
	subject, err := authz.ParseSubject(spelling)
	if err != nil {
		return authz.Subject{}, httpfault.RequestInvalid("body.principal", "type:id in tuple spelling",
			"the principal is not one the authorization graph can key on")
	}

	if subject.IsUserset() || !holdsAPICredentials(subject.Type()) {
		return authz.Subject{}, httpfault.RequestInvalid("body.principal",
			"user:<id> or service_account:<id>",
			"only a person or a service account can be paired to a subject")
	}

	return subject, nil
}

// UnknownPairing is the refusal for a subject no pairing names. It is exported
// so the composition root and the default below answer in the same words.
func UnknownPairing(provider, subject string) *fault.Fault {
	return httpfault.ResourceNotFound(httpfault.Reference("pairing", provider+"/"+subject))
}

// noPairings is the pairing store of a server wired without one, which is a
// deployment that verifies no bearer token. It holds nothing and refuses to
// write, for the reason noCredentials does: a pairing nothing would ever
// consult is not written.
type noPairings struct{}

var _ Pairings = noPairings{}

func (noPairings) Pair(context.Context, NewPairing) (Pairing, error) {
	return Pairing{}, httpfault.RequestInvalid("body.provider",
		"a provider this server verifies bearer tokens from",
		"this server authenticates nobody through a bearer token, so a pairing would be consulted by nothing")
}

func (noPairings) Fence(context.Context, string) (int64, error) { return 0, nil }

func (noPairings) List(context.Context, string) ([]Pairing, error) {
	return []Pairing{}, nil
}

func (noPairings) Unpair(_ context.Context, provider, subject string) error {
	return UnknownPairing(provider, subject)
}

// examplePairedAt is when the pairing in the worked examples was written, an
// hour after the first account, when the colleague's was.
var examplePairedAt = colleagueTime

// pairingExamples are the worked examples for the pairing surface. They live
// here so the feature is one file plus its registration lines.
func pairingExamples() map[string]operationExample {
	paired := Pairing{
		Provider:  "prusa-account",
		Subject:   "8f1e1b2c-0000-4000-8000-abcdefabcdef",
		Principal: "user:operator",
		Label:     "the owner's Prusa Account",
		CreatedAt: examplePairedAt,
	}

	return map[string]operationExample{
		"create-pairing": {
			Status: createdCode,
			Request: pairingBody{
				Provider:  paired.Provider,
				Subject:   paired.Subject,
				Principal: paired.Principal,
				Label:     paired.Label,
			},
			Response: paired,
		},

		"list-pairings": {Response: map[string]any{"items": []Pairing{
			paired,
			{
				Provider:  "prusa-account",
				Subject:   "c2a4d6e8-0000-4000-8000-fedcbafedcba",
				Principal: "service_account:wall-dashboard",
				Label:     "the wall dashboard",
				CreatedAt: examplePairedAt.Add(time.Hour),
			},
		}}},
	}
}
