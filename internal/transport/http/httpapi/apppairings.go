package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prusa3d/prusa-connect/internal/authz"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// The app pairing paths. An app asks at the first and collects at the second,
// with no credential, because a credential is what it is asking for. A person
// reads and answers a pairing at the other three, signed in.
const (
	appPairingsPath       = "/api/v1/auth/app-pairings"
	appPairingTokenPath   = "/api/v1/auth/app-pairings/token" //nolint:gosec // a route, not a credential
	appPairingPath        = "/api/v1/auth/app-pairings/{user_code}"
	appPairingApprovePath = "/api/v1/auth/app-pairings/{user_code}/approve"
	appPairingDenyPath    = "/api/v1/auth/app-pairings/{user_code}/deny"
)

// appPairingPage is where the web app shows a pairing to the person who
// decides it. The code rides in the query, so a link opens the right one.
const appPairingPage = "/pair"

// The bound on asking, per address. Asking takes no credential, so somebody on
// the network could fill the table of waiting pairings and lock every real app
// out. The server-wide bound holds the table; this one keeps one address from
// taking all of it.
const (
	appPairingsPerAddress  = 5
	appPairingAddressSpan  = 10 * time.Minute
	appPairingAddressTable = 1024
)

// The answers a poll and a request give. Each poll code is an error RFC 8628
// section 3.5 names, so a client written to the RFC knows what to do with it.
var (
	appPairingPending = fault.Register(fault.DomainAuth(), "APP_PAIRING_PENDING", http.StatusBadRequest,
		fault.Document(
			"Nobody has approved the pairing yet",
			"Poll again after the interval. This is authorization_pending in RFC 8628.",
			fault.RetryAllowed,
			fault.ShowDetail,
		))

	appPairingSlowDown = fault.Register(fault.DomainAuth(), "APP_PAIRING_SLOW_DOWN", http.StatusBadRequest,
		fault.Document(
			"The app polls faster than it was told to",
			"Wait five seconds longer between polls from now on. This is slow_down in RFC 8628.",
			fault.RetryAllowed,
			fault.ShowDetail,
		))

	appPairingDenied = fault.Register(fault.DomainAuth(), "APP_PAIRING_DENIED", http.StatusBadRequest,
		fault.Document(
			"The person denied the pairing",
			"Stop polling. Ask for a new pairing if the person changes their mind. This is access_denied in RFC 8628.",
			fault.RetryNever,
			fault.ShowDetail,
		))

	appPairingExpired = fault.Register(fault.DomainAuth(), "APP_PAIRING_EXPIRED", http.StatusBadRequest,
		fault.Document(
			"The pairing expired before it was approved",
			"Ask for a new pairing and show its code to the person again. This is expired_token in RFC 8628.",
			fault.RetryNever,
			fault.ShowDetail,
		))

	appPairingUnknown = fault.Register(fault.DomainAuth(), "APP_PAIRING_UNKNOWN", http.StatusBadRequest,
		fault.Document(
			"No pairing answers to that code",
			"A code works once. Ask for a new pairing. This is invalid_grant in RFC 8628.",
			fault.RetryNever,
			fault.ShowDetail,
		))

	appPairingLimited = fault.Register(fault.DomainAuth(), "APP_PAIRING_LIMITED", http.StatusTooManyRequests,
		fault.Document(
			"Too many pairings are waiting",
			"Wait for the pairings already asked for to be answered or to expire, then ask again.",
			fault.RetryAllowed,
			fault.ShowDetail,
		))

	appPairingClosed = fault.Register(fault.DomainAuth(), "APP_PAIRING_CLOSED", http.StatusConflict,
		fault.Document(
			"The pairing is no longer waiting for a decision",
			"Somebody has already approved or denied it, or it expired. The app asks for a new one.",
			fault.RetryNever,
			fault.ShowDetail,
		))

	appPairingMissing = fault.Register(fault.DomainAuth(), "APP_PAIRING_MISSING", http.StatusNotFound,
		fault.Document(
			"No waiting pairing has that code",
			"Check the code the app shows. A pairing waits ten minutes, and the app asks for a new one after that.",
			fault.RetryNever,
			fault.ShowDetail,
		))

	appPairingScopes = fault.Register(fault.DomainAuth(), "APP_PAIRING_SCOPES", http.StatusUnprocessableEntity,
		fault.Document(
			"The approval names a scope the app did not ask for",
			"Approve the scopes the app asked for, or fewer of them.",
			fault.RetryNever,
			fault.ShowDetail,
		))
)

// The errors an AppPairings implementation returns, which this surface turns
// into the codes above. They are its own, so the implementation needs nothing
// of the platform's to name them.
var (
	ErrAppPairingPending  = errors.New("httpapi: the pairing waits for a decision")
	ErrAppPairingSlowDown = errors.New("httpapi: the app polls too fast")
	ErrAppPairingDenied   = errors.New("httpapi: the pairing was denied")
	ErrAppPairingExpired  = errors.New("httpapi: the pairing expired")
	ErrAppPairingUnknown  = errors.New("httpapi: no pairing answers to that code")
	ErrAppPairingBusy     = errors.New("httpapi: too many pairings are waiting")
	ErrAppPairingClosed   = errors.New("httpapi: the pairing is no longer waiting for a decision")
)

// AppPairings opens, shows, decides and collects app pairings. The composition
// root satisfies it over the platform's pairing store and the credential
// store, and it writes the audit line for each step.
type AppPairings interface {
	// Request opens a pairing for an app.
	Request(ctx context.Context, request AppPairingRequest) (IssuedAppPairing, error)
	// Lookup finds a live pairing by the code a person types.
	Lookup(ctx context.Context, userCode string) (AppPairing, error)
	// Decide records a person's answer.
	Decide(ctx context.Context, decision AppPairingDecision) error
	// Collect is one poll of the app. It mints and returns the credential the
	// first time it is called after an approval.
	Collect(ctx context.Context, deviceCode string) (IssuedCredential, error)
	// OpenPageSession trades an app pairing credential for a page session
	// (ADR-20261112-nay). Anything but a live app pairing credential is
	// ErrNotAppCredential, or the verifier's own refusal. admit is asked with
	// the credential's public id once the credential is known to be one, and
	// a false answer is ErrPageSessionLimited.
	OpenPageSession(ctx context.Context, token string, admit func(parent string) bool) (PageSession, error)
}

// AppPairingRequest is what an app asks with.
type AppPairingRequest struct {
	AppName       string
	Scopes        []string
	ClientAddress string
}

// AppPairing is a pairing as a person reads it before deciding.
type AppPairing struct {
	UserCode      string    `json:"user_code"      doc:"The code the app shows, with a dash in the middle."`
	AppName       string    `json:"app_name"       doc:"The name the app gave itself. Nothing checks it, so the person decides whether to believe it."`
	Scopes        []string  `json:"scopes"         doc:"The scopes the app asked for."                                                                 nullable:"false" enum:"read,operate,administer"`
	ClientAddress string    `json:"client_address" doc:"The network address the app asked from, as this server saw it."`
	ExpiresAt     time.Time `json:"expires_at"     doc:"When the pairing stops waiting."`
}

// IssuedAppPairing is a new pairing and the code the app polls with.
type IssuedAppPairing struct {
	AppPairing

	DeviceCode string
	Interval   time.Duration
	// Lifetime is how long the pairing waits from now.
	Lifetime time.Duration
}

// AppPairingDecision is a person's answer to a pairing.
type AppPairingDecision struct {
	UserCode string
	Approve  bool
	// DecidedBy is the person, in tuple spelling, and Root the root of their
	// sign-in.
	DecidedBy string
	Root      string
	// Scopes are the scopes approved, the ones asked for or fewer. Empty on a
	// denial.
	Scopes []string
	// Fence is the generations read before the sign-in was checked.
	Fence CredentialFence
}

type appPairingRequestBody struct {
	AppName string   `json:"app_name" doc:"The name the person sees when they decide, such as the app and the machine it runs on." minLength:"1" maxLength:"64"`
	Scopes  []string `json:"scopes"   doc:"The scopes the app asks for. The person may approve fewer."                                                          nullable:"false" enum:"read,operate,administer" minItems:"1" maxItems:"3"`
}

type requestAppPairingInput struct {
	Body appPairingRequestBody
}

// issuedAppPairingBody follows RFC 8628 section 3.2, so a client written to
// the RFC reads it as it is.
type issuedAppPairingBody struct {
	DeviceCode              string `json:"device_code"               doc:"The code the app polls with. It is secret: only the app holds it."`
	UserCode                string `json:"user_code"                 doc:"The code the app shows the person, with a dash in the middle."`
	VerificationURI         string `json:"verification_uri"          doc:"Where the person confirms the code, on this server's web app."`
	VerificationURIComplete string `json:"verification_uri_complete" doc:"The same page with the code filled in, for a link or a QR code."`
	ExpiresIn               int    `json:"expires_in"                doc:"How many seconds the pairing waits."`
	Interval                int    `json:"interval"                  doc:"How many seconds the app waits between two polls."`
}

type requestAppPairingOutput struct {
	Status int
	Body   issuedAppPairingBody
}

type collectAppPairingBody struct {
	DeviceCode string `json:"device_code" doc:"The code the request answered with." minLength:"1" maxLength:"128"`
}

type collectAppPairingInput struct {
	Body collectAppPairingBody
}

// collectedCredentialBody is the credential a collection answers with, once
// bare and once in the JWT shape of ADR-20261112-nay. It is spelled out
// rather than embedding createdCredentialBody, because the schema generator
// skips an unexported embedded struct.
type collectedCredentialBody struct {
	ID          string     `json:"id"                     doc:"The public half of the token, which is what a revocation names."`
	Name        string     `json:"name"                   doc:"The app name the person approved."`
	Subject     string     `json:"subject"                doc:"The person who approved, in tuple spelling. The credential speaks for them."`
	CreatedAt   time.Time  `json:"created_at"             doc:"When it was minted."`
	ExpiresAt   time.Time  `json:"expires_at"             doc:"When this server stops accepting it."`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty" doc:"Absent: a credential that has just been minted has not been presented."`
	Scopes      []string   `json:"scopes"                 doc:"The scopes the person approved."                                                                                                                                                                                                                   nullable:"false" enum:"read,operate,administer"`
	Origin      string     `json:"origin"                 doc:"Always app_pairing."                                                                                                                                                                                                                                                enum:"app_pairing"`
	Token       string     `json:"token"                  doc:"The credential, shown once. Present it as Authorization: Bearer <token>. This server keeps only a hash and cannot show it again."`
	AccessToken string     `json:"access_token"           doc:"The same credential in the shape of a JWT whose third segment is the token. Its payload names sub, iat, exp, iss and jti, and it is not signed. Present it as Authorization: Bearer <access_token>. This server checks it as it checks the token."`
}

// collectedCredentialOf renders a collected credential with both tokens.
func collectedCredentialOf(issued IssuedCredential) collectedCredentialBody {
	return collectedCredentialBody{
		ID:          issued.ID,
		Name:        issued.Name,
		Subject:     issued.Subject,
		CreatedAt:   issued.CreatedAt,
		ExpiresAt:   issued.ExpiresAt,
		LastUsedAt:  issued.LastUsedAt,
		Scopes:      issued.Scopes,
		Origin:      issued.Origin,
		Token:       issued.Token,
		AccessToken: issued.AccessToken,
	}
}

type collectAppPairingOutput struct {
	Body collectedCredentialBody
}

// AppPairingRef is the pairing a path names. It is exported because Huma binds
// parameters by reflection and cannot write into an unexported embedded field.
type AppPairingRef struct {
	UserCode string `path:"user_code" doc:"The code the app shows, with or without the dash, in any case." example:"BCDF-GHJK" maxLength:"16"`
}

type getAppPairingInput struct {
	AppPairingRef
}

type getAppPairingOutput struct {
	Body AppPairing
}

type approveAppPairingBody struct {
	Scopes []string `json:"scopes,omitempty" nullable:"false" doc:"The scopes to approve: the ones the app asked for or fewer. Empty approves what it asked for." enum:"read,operate,administer" maxItems:"3"`
}

type approveAppPairingInput struct {
	AppPairingRef

	// Body is optional. Left out, the person approves what the app asked
	// for.
	Body *approveAppPairingBody
}

type decideAppPairingInput struct {
	AppPairingRef
}

type decideAppPairingOutput struct {
	Status int
}

// registerAppPairings publishes the app pairing surface. The operations are
// registered whatever the process was wired with, so the document a client
// generates from does not depend on whether this deployment pairs any app.
func registerAppPairings(api huma.API, cfg Config) {
	limiter := newAddressLimiter(appPairingsPerAddress, appPairingAddressSpan, appPairingAddressTable)

	registerRequestAppPairing(api, cfg, limiter)
	registerCollectAppPairing(api, cfg)
	registerGetAppPairing(api, cfg)
	registerApproveAppPairing(api, cfg)
	registerDenyAppPairing(api, cfg)
	registerOpenPageSession(api, cfg,
		newAddressLimiter(pageSessionsPerCredential, appPairingAddressSpan, appPairingAddressTable))
}

func registerRequestAppPairing(api huma.API, cfg Config, limiter *addressLimiter) {
	register(api, huma.Operation{
		OperationID: "request-app-pairing",
		Method:      http.MethodPost,
		Path:        appPairingsPath,
		Summary:     "Ask a person for a credential",
		Description: "Opens a pairing, the device authorization request of RFC 8628. It takes no " +
			"credential. Show user_code and verification_uri to the person, or open " +
			"verification_uri_complete for them, and poll the token operation with device_code. " +
			"The person signs in to this server, reads the app name and the scopes, and approves " +
			"or denies. A pairing waits ten minutes. One address may ask five times in ten minutes, " +
			"and at most 32 pairings wait at once over the whole server.",
		Tags:          []string{"auth"},
		Security:      noSecurity,
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *requestAppPairingInput) (*requestAppPairingOutput, error) {
		request := requestFrom(ctx)
		address := clientAddress(cfg, request)

		scopes, err := authz.ParseScopes(in.Body.Scopes)
		if err != nil {
			return nil, httpfault.RequestInvalid("body.scopes", "read, operate or administer",
				"a scope this server does not know")
		}

		if !limiter.take(address, time.Now()) {
			return nil, appPairingLimited.New("this address has asked for too many pairings")
		}

		issued, err := cfg.AppPairings.Request(ctx, AppPairingRequest{
			AppName: in.Body.AppName, Scopes: authz.ScopeNames(scopes), ClientAddress: address,
		})
		if err != nil {
			return nil, appPairingFault(err, "open the pairing")
		}

		page := verificationURI(request)

		return &requestAppPairingOutput{
			Status: http.StatusCreated,
			Body: issuedAppPairingBody{
				DeviceCode:              issued.DeviceCode,
				UserCode:                issued.UserCode,
				VerificationURI:         page,
				VerificationURIComplete: page + "?code=" + url.QueryEscape(issued.UserCode),
				ExpiresIn:               int(issued.Lifetime / time.Second),
				Interval:                int(issued.Interval / time.Second),
			},
		}, nil
	})
}

func registerCollectAppPairing(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "collect-app-pairing",
		Method:      http.MethodPost,
		Path:        appPairingTokenPath,
		Summary:     "Collect the credential a person approved",
		Description: "One poll of the device access token request of RFC 8628. It takes no " +
			"credential. Until a person decides it answers APP_PAIRING_PENDING, and a poll sooner " +
			"than the interval answers APP_PAIRING_SLOW_DOWN and adds five seconds to it. After an " +
			"approval the next poll answers the credential, once: its token, the scopes the person " +
			"approved and the person it speaks for, and the same token in the shape of a JWT as " +
			"access_token. A denial is answered once with " +
			"APP_PAIRING_DENIED. After that, and after the credential is collected, the code is " +
			"unknown.",
		Tags:     []string{"auth"},
		Security: noSecurity,
		Errors:   []int{http.StatusBadRequest},
	}, func(ctx context.Context, in *collectAppPairingInput) (*collectAppPairingOutput, error) {
		issued, err := cfg.AppPairings.Collect(ctx, in.Body.DeviceCode)
		if err != nil {
			return nil, appPairingFault(err, "collect the pairing")
		}

		return &collectAppPairingOutput{Body: collectedCredentialOf(issued)}, nil
	})
}

func registerGetAppPairing(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "get-app-pairing",
		Method:      http.MethodGet,
		Path:        appPairingPath,
		Summary:     "Read a pairing before deciding it",
		Description: "Returns what a waiting pairing asks for: the app name, the scopes and the " +
			"address it asked from. It takes a sign-in as a person, the same as a decision does.",
		Tags:   []string{"auth"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, func(ctx context.Context, in *getAppPairingInput) (*getAppPairingOutput, error) {
		if err := requireSignedIn(ctx, cfg); err != nil {
			return nil, err
		}

		pairing, err := cfg.AppPairings.Lookup(ctx, in.UserCode)
		if err != nil {
			return nil, decisionFault(err, "find the pairing")
		}

		return &getAppPairingOutput{Body: pairing}, nil
	})
}

func registerApproveAppPairing(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "approve-app-pairing",
		Method:      http.MethodPost,
		Path:        appPairingApprovePath,
		Summary:     "Approve a pairing",
		Description: "Gives the app a credential that speaks for you, limited to the scopes approved: " +
			"the ones it asked for or fewer. The credential never reaches more than you do, it lives " +
			"ninety days, and it is listed with your credentials, where you revoke it. It takes a " +
			"sign-in as a person, so an api credential cannot approve one.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity,
		},
	}, func(ctx context.Context, in *approveAppPairingInput) (*decideAppPairingOutput, error) {
		var scopes []string
		if in.Body != nil {
			scopes = in.Body.Scopes
		}

		return decideAppPairing(ctx, cfg, in.UserCode, true, scopes)
	})
}

func registerDenyAppPairing(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "deny-app-pairing",
		Method:      http.MethodPost,
		Path:        appPairingDenyPath,
		Summary:     "Deny a pairing",
		Description: "Refuses the app its credential. Its next poll is told, once. It takes a sign-in " +
			"as a person.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		},
	}, func(ctx context.Context, in *decideAppPairingInput) (*decideAppPairingOutput, error) {
		return decideAppPairing(ctx, cfg, in.UserCode, false, nil)
	})
}

// decideAppPairing records a signed-in person's answer to a pairing.
func decideAppPairing(
	ctx context.Context, cfg Config, userCode string, approve bool, scopes []string,
) (*decideAppPairingOutput, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	root := callerRoot(ctx, cfg, who)

	// Read before the sign-in check, so a password change between the check
	// and the collection leaves the credential refused.
	fence, err := cfg.Credentials.Fence(ctx, who.String(), who.String(), root)
	if err != nil {
		return nil, serverFault(err, "read the credential generations")
	}

	if err = requireSignedIn(ctx, cfg); err != nil {
		return nil, err
	}

	decision := AppPairingDecision{
		UserCode: userCode, Approve: approve, DecidedBy: who.String(), Root: root, Fence: fence,
	}

	if approve {
		decision.Scopes, err = approvedScopes(ctx, cfg, userCode, scopes)
		if err != nil {
			return nil, err
		}
	}

	if err = cfg.AppPairings.Decide(ctx, decision); err != nil {
		return nil, decisionFault(err, "decide the pairing")
	}

	return &decideAppPairingOutput{Status: http.StatusNoContent}, nil
}

// approvedScopes reads the scopes a person approves: what the app asked for
// when they name none, and otherwise a part of it.
func approvedScopes(ctx context.Context, cfg Config, userCode string, names []string) ([]string, error) {
	pairing, err := cfg.AppPairings.Lookup(ctx, userCode)
	if err != nil {
		return nil, decisionFault(err, "find the pairing")
	}

	asked, err := authz.ParseScopes(pairing.Scopes)
	if err != nil {
		return nil, httpfault.Unexpected("read the scopes the app asked for")
	}

	if len(names) == 0 {
		return authz.ScopeNames(asked), nil
	}

	approved, err := authz.ParseScopes(names)
	if err != nil {
		return nil, httpfault.RequestInvalid("body.scopes", "read, operate or administer",
			"a scope this server does not know")
	}

	if !authz.WithinScopes(approved, asked) {
		return nil, appPairingScopes.New("the approval names more than the app asked for")
	}

	return authz.ScopeNames(approved), nil
}

// requireSignedIn refuses a request that does not carry a sign-in as a person.
// Deciding a pairing hands out access, which an api credential may not do.
func requireSignedIn(ctx context.Context, cfg Config) error {
	if cfg.Credentials.SignedIn(requestFrom(ctx)) {
		return nil
	}

	return fault.AuthSessionRequired.New("deciding a pairing takes a sign-in as a person. An api " +
		"credential cannot, and neither can a Prusa Account token paired to a service account")
}

// appPairingFault renders what the implementation reported.
func appPairingFault(err error, doing string) error {
	switch {
	case errors.Is(err, ErrAppPairingPending):
		return appPairingPending.New("nobody has decided the pairing yet")
	case errors.Is(err, ErrAppPairingSlowDown):
		return appPairingSlowDown.New("the poll came before the interval passed")
	case errors.Is(err, ErrAppPairingDenied):
		return appPairingDenied.New("a person denied the pairing")
	case errors.Is(err, ErrAppPairingExpired):
		return appPairingExpired.New("the pairing expired")
	case errors.Is(err, ErrAppPairingUnknown):
		return appPairingUnknown.New("no pairing answers to that code")
	case errors.Is(err, ErrAppPairingBusy):
		return appPairingLimited.New("too many pairings are waiting on this server")
	case errors.Is(err, ErrAppPairingClosed):
		return appPairingClosed.New("the pairing is no longer waiting for a decision")
	}

	return serverFault(err, doing)
}

// decisionFault is appPairingFault for a person reading or deciding a pairing,
// where a code no pairing answers to is a page that is not there.
func decisionFault(err error, doing string) error {
	if errors.Is(err, ErrAppPairingUnknown) {
		return appPairingMissing.New("no waiting pairing has that code")
	}

	return appPairingFault(err, doing)
}

// verificationURI is the page a person confirms a code on, on the origin the
// app reached this server at. That is the address the app already knows works
// from the network it is on.
func verificationURI(r *http.Request) string {
	if r == nil {
		return appPairingPage
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	return (&url.URL{Scheme: scheme, Host: r.Host, Path: appPairingPage}).String()
}

// clientAddress is where a request came from, read the way the login guard
// reads it, so a declared proxy is trusted here exactly as it is there.
func clientAddress(cfg Config, r *http.Request) string {
	if r == nil || cfg.Logins == nil {
		return ""
	}

	return cfg.Logins.Address(r)
}

// addressLimiter counts the requests each address made in a window and refuses
// one past the bound. Its table is capped, and a full table refuses an address
// it holds no row for, so a caller who varies the address cannot grow it or
// push out the record of one that asked too often.
type addressLimiter struct {
	mu     sync.Mutex
	limit  int
	span   time.Duration
	most   int
	counts map[string]addressCount
}

type addressCount struct {
	count int
	since time.Time
}

func newAddressLimiter(limit int, span time.Duration, most int) *addressLimiter {
	return &addressLimiter{limit: limit, span: span, most: most, counts: map[string]addressCount{}}
}

// take spends one request of an address, and reports whether it was allowed.
func (l *addressLimiter) take(address string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for key, held := range l.counts {
		if !now.Before(held.since.Add(l.span)) {
			delete(l.counts, key)
		}
	}

	held, ok := l.counts[address]
	if !ok {
		if len(l.counts) >= l.most {
			return false
		}

		held = addressCount{since: now}
	}

	if held.count >= l.limit {
		return false
	}

	held.count++
	l.counts[address] = held

	return true
}

// noAppPairings is a server that pairs no app: one that mints no credential.
type noAppPairings struct{}

func (noAppPairings) Request(context.Context, AppPairingRequest) (IssuedAppPairing, error) {
	return IssuedAppPairing{}, fault.AuthCredentialRefused.New("this server mints no api credential")
}

func (noAppPairings) Lookup(context.Context, string) (AppPairing, error) {
	return AppPairing{}, ErrAppPairingUnknown
}

func (noAppPairings) Decide(context.Context, AppPairingDecision) error { return ErrAppPairingUnknown }

func (noAppPairings) Collect(context.Context, string) (IssuedCredential, error) {
	return IssuedCredential{}, ErrAppPairingUnknown
}

func (noAppPairings) OpenPageSession(context.Context, string, func(string) bool) (PageSession, error) {
	return PageSession{}, ErrNotAppCredential
}
