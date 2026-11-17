package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/internal/authz"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// fakeAccounts is a local credential store with the hashing taken out. It
// records what it was asked to do, so a test can hold the surface to calling
// the store rather than to answering by itself.
type fakeAccounts struct {
	mu sync.Mutex

	info AuthInfo
	rows []Account

	setupErr    error
	loginErr    error
	createErr   error
	deleteErr   error
	passwordErr error

	created  []NewAccount
	deleted  []string
	changed  []string
	reset    []string
	loggedIn []string
}

var _ Accounts = (*fakeAccounts)(nil)

func (a *fakeAccounts) Info(context.Context) (AuthInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.info, nil
}

func (a *fakeAccounts) Setup(_ context.Context, spec NewAccount) (Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.setupErr != nil {
		return Account{}, a.setupErr
	}

	a.created = append(a.created, spec)

	return Account{ID: "operator", Username: spec.Username, DisplayName: spec.DisplayName}, nil
}

func (a *fakeAccounts) Login(_ context.Context, username, _ string) (Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.loginErr != nil {
		return Session{}, a.loginErr
	}

	a.loggedIn = append(a.loggedIn, username)

	return Session{
		Token:     "pcl_sessiontoken",
		ExpiresAt: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
	}, nil
}

func (a *fakeAccounts) List(context.Context) ([]Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.rows, nil
}

// Get answers from the rows the test seeded. An id no row holds is the
// not-found every other operation on a missing account gives.
func (a *fakeAccounts) Get(_ context.Context, id string) (Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, row := range a.rows {
		if row.ID == id {
			return row, nil
		}
	}

	return Account{}, httpfault.ResourceNotFound(httpfault.Reference("account", ""))
}

func (a *fakeAccounts) Create(_ context.Context, spec NewAccount) (Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.createErr != nil {
		return Account{}, a.createErr
	}

	a.created = append(a.created, spec)

	return Account{ID: "u0123456789abcdefghij", Username: spec.Username}, nil
}

func (a *fakeAccounts) Delete(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.deleteErr != nil {
		return a.deleteErr
	}

	a.deleted = append(a.deleted, id)

	return nil
}

func (a *fakeAccounts) ChangePassword(_ context.Context, id, _, _ string) (Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.passwordErr != nil {
		return Session{}, a.passwordErr
	}

	a.changed = append(a.changed, id)

	return Session{Token: "pcl_after_the_change", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (a *fakeAccounts) ResetPassword(_ context.Context, id, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.passwordErr != nil {
		return a.passwordErr
	}

	a.reset = append(a.reset, id)

	return nil
}

// grantingStore is a graph that holds whatever it was told to hold, so a test
// can put a caller on either side of the account administration check.
type grantingStore struct {
	fakeGrantStore

	allow bool
	asked []authz.Object
}

func (g *grantingStore) Can(
	_ context.Context, _ authz.Subject, _ authz.Action, object authz.Object,
) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.asked = append(g.asked, object)

	return g.allow, g.refuse
}

// serverWithAccounts is a server whose credential store and graph are the
// caller's.
func serverWithAccounts(t *testing.T, accounts Accounts, grants GrantStore) *Server {
	t.Helper()

	return accountServer(t, accounts, grants, fixedPrincipal("user:operator"))
}

// serverBeforeAnybodyHasLoggedIn resolves no caller at all, which is every
// request on a server nobody has set up yet.
func serverBeforeAnybodyHasLoggedIn(t *testing.T, accounts Accounts) *Server {
	t.Helper()

	return accountServer(t, accounts, &fakeGrantStore{}, fixedPrincipal(""))
}

func accountServer(t *testing.T, accounts Accounts, grants GrantStore, who Principals) *Server {
	t.Helper()

	server, err := New(Config{
		Version:    "test",
		Principals: who,
		Resources:  seededStore(),
		Presets:    seededPresets(),
		Endpoints:  seededEndpoints(),
		Grants:     grants,
		Sessions:   newFakeSessions(),
		Accounts:   accounts,
		Readiness:  &fakeReadiness{report: readyReport()},
		Devices:    noPrinters{},
		Stores:     noStorages{},
		Events:     noEvents{},
	})
	require.NoError(t, err)

	return server
}

// TestAServerWithNoCredentialStoreDoesNotStart is the same rule Sessions and
// Principals carry. A store that is missing is a wiring fault; one that refuses
// everything is a deployment.
func TestAServerWithNoCredentialStoreDoesNotStart(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Version:    "test",
		Principals: fixedPrincipal("user:operator"),
		Resources:  seededStore(),
		Presets:    seededPresets(),
		Endpoints:  seededEndpoints(),
		Grants:     &fakeGrantStore{},
		Sessions:   newFakeSessions(),
		Readiness:  &fakeReadiness{report: readyReport()},
		Devices:    noPrinters{},
		Stores:     noStorages{},
		Events:     noEvents{},
	})
	require.ErrorIs(t, err, ErrNoAccounts)
}

// TestTheFirstRunRoutesTakeNoIdentity is what lets a self-hoster reach the
// server they have just started. Nobody holds a credential yet, so the three
// routes that hand one out must not need one.
func TestTheFirstRunRoutesTakeNoIdentity(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true, NeedsSetup: true}}
	handler := serverBeforeAnybodyHasLoggedIn(t, accounts).Handler()

	// The identity middleware reads the published document, so a route that
	// carries a security requirement is refused here before its handler runs.
	rec := call(t, handler, http.MethodGet, authInfoPath, "")
	require.Equal(t, http.StatusOK, rec.Code)

	rec = call(t, handler, http.MethodPost, setupPath,
		`{"username":"owner@example.com","password":"correct horse battery"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	rec = call(t, handler, http.MethodPost, loginPath,
		`{"username":"owner@example.com","password":"correct horse battery"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

// TestAuthInfoNamesTheLoginAndCountsNobody is what a login screen reads. It
// says which form to show. It says nothing about who is on this server, because
// the caller reading it has no credential.
func TestAuthInfoNamesTheLoginAndCountsNobody(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{
		info: AuthInfo{Provider: "local", Credentials: true, NeedsSetup: true},
		rows: []Account{{ID: "operator", Username: "owner@example.com"}},
	}

	rec := call(t, serverWithAccounts(t, accounts, &fakeGrantStore{}).Handler(),
		http.MethodGet, authInfoPath, "")
	require.Equal(t, http.StatusOK, rec.Code)

	var body AuthInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "local", body.Provider)
	require.True(t, body.Credentials)
	require.False(t, body.Redirect)
	require.True(t, body.NeedsSetup)

	require.NotContains(t, rec.Body.String(), "owner@example.com",
		"the public login report named an account")
}

// TestSetupIsRefusedOnceThisServerHasAnAdministrator is the door, at the
// transport. The store closes it; this holds that the refusal reaches the
// caller as a conflict rather than as a success or a 500.
func TestSetupIsRefusedOnceThisServerHasAnAdministrator(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{
		info:     AuthInfo{Provider: "local", Credentials: true},
		setupErr: fault.AuthSetupClosed.New("this server already has an administrator, so setup is closed"),
	}

	rec := call(t, serverWithAccounts(t, accounts, &fakeGrantStore{}).Handler(),
		http.MethodPost, setupPath, `{"username":"intruder@example.com","password":"correct horse battery"}`)

	body := requireProblem(t, rec, http.StatusConflict)
	require.Equal(t, fault.AuthSetupClosed.Code(), body.Code)
	require.Empty(t, accounts.created, "a refused setup reached the store")
}

// TestSetupRefusesAPasswordBelowTheFloor holds the bound at the schema, so a
// short password is refused before anything is hashed.
func TestSetupRefusesAPasswordBelowTheFloor(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true, NeedsSetup: true}}

	rec := call(t, serverWithAccounts(t, accounts, &fakeGrantStore{}).Handler(),
		http.MethodPost, setupPath, `{"username":"owner@example.com","password":"short"}`)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	require.Empty(t, accounts.created, "a password below the floor reached the store")
}

// TestAPasswordLoginSetsTheCredentialAsACookie is the login itself. The cookie
// is HttpOnly so no script reads it, SameSite=Lax so it does not ride a
// cross-site form post, and Secure unless this deployment told the server its
// own origin is plain HTTP.
func TestAPasswordLoginSetsTheCredentialAsACookie(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true}}

	rec := call(t, serverWithAccounts(t, accounts, &fakeGrantStore{}).Handler(),
		http.MethodPost, loginPath, `{"username":"owner@example.com","password":"correct horse battery"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	set := rec.Header().Get("Set-Cookie")
	require.Contains(t, set, testCookieName+"=pcl_sessiontoken")
	require.Contains(t, set, "HttpOnly")
	require.Contains(t, set, "SameSite=Lax")
	require.Contains(t, set, "Secure")

	require.NotContains(t, rec.Body.String(), "pcl_sessiontoken",
		"the credential was written into the body as well as the cookie")
	require.Equal(t, []string{"owner@example.com"}, accounts.loggedIn)
}

// TestARefusedPasswordLoginSetsNoCredential is the fail-closed half at the
// transport. A refusal must not leave a cookie behind, and must not say which
// half of the pair was wrong.
func TestARefusedPasswordLoginSetsNoCredential(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{
		info:     AuthInfo{Provider: "local", Credentials: true},
		loginErr: fault.AuthTokenInvalid.New("the credential is not one this server accepts"),
	}

	rec := call(t, serverWithAccounts(t, accounts, &fakeGrantStore{}).Handler(),
		http.MethodPost, loginPath, `{"username":"owner@example.com","password":"not the password"}`)

	body := requireProblem(t, rec, http.StatusUnauthorized)
	require.Equal(t, fault.AuthLoginRefused.Code(), body.Code)
	require.Empty(t, rec.Header().Values("Set-Cookie"), "a refused login set a credential")
	require.NotContains(t, strings.ToLower(rec.Body.String()), "not the password",
		"the refusal quotes what the caller sent")
}

// TestAWrongPasswordAndAnUnknownUserReadTheSame is what a person at the form
// is told. The credential check answers TOKEN_INVALID on every path, which
// reads as a broken token; this surface has to name it as a sign-in that was
// refused, and has to give one answer for both, because a second answer for an
// unknown name is a way to list the accounts this server holds.
func TestAWrongPasswordAndAnUnknownUserReadTheSame(t *testing.T) {
	t.Parallel()

	handler := serverWithAccounts(t, &oneGoodAccount{
		fakeAccounts: fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true}},
		username:     "owner@example.com",
	}, &fakeGrantStore{}).Handler()

	cases := map[string]string{
		"wrong password": `{"username":"owner@example.com","password":"not the password"}`,
		"unknown user":   `{"username":"nobody@example.com","password":"` + theRightPassword + `"}`,
	}

	answers := make(map[string]string, len(cases))

	for name, sent := range cases {
		rec := call(t, handler, http.MethodPost, loginPath, sent)

		body := requireProblem(t, rec, http.StatusUnauthorized)
		require.Equal(t, fault.AuthLoginRefused.Code(), body.Code, "case %s", name)
		require.Equal(t, "The username or password is wrong", body.Title, "case %s", name)
		require.Empty(t, rec.Header().Values("Set-Cookie"), "a refused login set a credential")

		// The trace id is this request's own and is the one member that is
		// meant to differ between two answers.
		answers[name] = tracelessBody(t, rec.Body.Bytes())
	}

	require.Equal(t, answers["wrong password"], answers["unknown user"],
		"the two refusals differ, so a caller can learn which usernames exist")
}

// tracelessBody is a problem document without its trace id, so two occurrences
// of one refusal compare as the bytes a caller can tell apart.
func tracelessBody(t *testing.T, body []byte) string {
	t.Helper()

	var document map[string]any

	require.NoError(t, json.Unmarshal(body, &document))
	delete(document, "trace_id")

	compared, err := json.Marshal(document)
	require.NoError(t, err)

	return string(compared)
}

// TestALoginRefusedForAnotherReasonKeepsItsOwnCode holds the narrow half of the
// rewrite. Only the credential refusal is renamed: a store that raised some
// other registered code still answers with it, or a caller would be told to
// check their typing about something typing cannot fix.
func TestALoginRefusedForAnotherReasonKeepsItsOwnCode(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{
		info:     AuthInfo{Provider: "local", Credentials: true},
		loginErr: fault.AuthSetupPending.New("this server has not been set up yet"),
	}

	rec := call(t, serverWithAccounts(t, accounts, &fakeGrantStore{}).Handler(),
		http.MethodPost, loginPath, `{"username":"owner@example.com","password":"correct horse battery"}`)

	body := requireProblem(t, rec, http.StatusServiceUnavailable)
	require.Equal(t, fault.AuthSetupPending.Code(), body.Code)
}

// TestAdministeringAccountsTakesTheAuthResourceKind is where the permission
// comes from. It is the graph's, on the kind the accounts belong to, so there
// is no second vocabulary for who may administer identity.
func TestAdministeringAccountsTakesTheAuthResourceKind(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true}}
	grants := &grantingStore{allow: true}

	rec := call(t, serverWithAccounts(t, accounts, grants).Handler(), http.MethodGet, accountsPath, "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	require.NotEmpty(t, grants.asked, "the surface answered without asking the graph")
	require.Equal(t, "resource_kind:auth", grants.asked[0].String())
}

// TestACallerWithNoGrantAdministersNothing is the refusal half, over every
// operation that writes. A permission enforced on the listing and not on the
// writes is the shape this project already found once.
func TestACallerWithNoGrantAdministersNothing(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true}}
	handler := serverWithAccounts(t, accounts, &grantingStore{allow: false}).Handler()

	cases := map[string]struct {
		method string
		path   string
		body   string
	}{
		"list": {method: http.MethodGet, path: accountsPath},
		"create": {
			method: http.MethodPost,
			path:   accountsPath,
			body:   `{"username":"x@example.com","password":"correct horse battery"}`,
		},
		"delete": {method: http.MethodDelete, path: accountsPath + "/u0123456789abcdefghij"},
		"password": {
			method: http.MethodPost,
			path:   accountsPath + "/u0123456789abcdefghij/password",
			body:   `{"new_password":"correct horse battery"}`,
		},
		"role": {
			method: http.MethodPut,
			path:   accountsPath + "/u0123456789abcdefghij/role",
			body:   `{"role":"admin"}`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := call(t, handler, tc.method, tc.path, tc.body)

			body := requireProblem(t, rec, http.StatusForbidden)
			require.Equal(t, authz.CodeAccessDenied(), body.Code)
		})
	}

	require.Empty(t, accounts.created, "a refused caller created an account")
	require.Empty(t, accounts.deleted, "a refused caller deleted an account")
	require.Empty(t, accounts.reset, "a refused caller reset a password")
}

// TestChangingYourOwnPasswordTakesTheCurrentOne is the door on the reset path.
// Without it, anybody holding a stolen cookie changes the password and keeps
// the account.
func TestChangingYourOwnPasswordTakesTheCurrentOne(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true}}
	handler := serverWithAccounts(t, accounts, &grantingStore{allow: false}).Handler()

	rec := call(t, handler, http.MethodPost, accountsPath+"/operator/password",
		`{"new_password":"a new password"}`)

	body := requireProblem(t, rec, http.StatusUnprocessableEntity)
	require.Equal(t, httpfault.CodeRequestInvalid(), body.Code)
	require.Empty(t, accounts.changed, "a change with no current password reached the store")

	rec = call(t, handler, http.MethodPost, accountsPath+"/operator/password",
		`{"current_password":"correct horse battery","new_password":"a new password"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, []string{"operator"}, accounts.changed)
}

// TestAnAdministratorResetsAnotherAccountWithNoCurrentPassword is the recovery
// path, and it is the graph that separates it from the case above.
func TestAnAdministratorResetsAnotherAccountWithNoCurrentPassword(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true}}
	handler := serverWithAccounts(t, accounts, &grantingStore{allow: true}).Handler()

	rec := call(t, handler, http.MethodPost, accountsPath+"/u0123456789abcdefghij/password",
		`{"new_password":"a reset password"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	require.Equal(t, []string{"u0123456789abcdefghij"}, accounts.reset)
	require.Empty(t, accounts.changed, "the reset went through the self-service path")
}

// TestTheAccountListingCarriesNoPasswordMaterial is the leak this surface would
// be worst at. The rendered shape has no field a hash could travel in, and the
// body is read to prove it rather than the struct.
func TestTheAccountListingCarriesNoPasswordMaterial(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{
		info: AuthInfo{Provider: "local", Credentials: true},
		rows: []Account{
			{ID: "operator", Username: "owner@example.com", DisplayName: "Owner"},
			{ID: "u0123456789abcdefghij", Username: "colleague@example.com"},
		},
	}

	rec := call(t, serverWithAccounts(t, accounts, &grantingStore{allow: true}).Handler(),
		http.MethodGet, accountsPath, "")
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "owner@example.com")

	for _, forbidden := range []string{"password", "hash", "argon2", "$"} {
		require.NotContainsf(t, body, forbidden,
			"the account listing carries %q, which is password material", forbidden)
	}
}

// TestAnEmptyAccountListIsAList keeps a client from having to tell an empty
// collection from a missing one.
func TestAnEmptyAccountListIsAList(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local", Credentials: true}}

	rec := call(t, serverWithAccounts(t, accounts, &grantingStore{allow: true}).Handler(),
		http.MethodGet, accountsPath, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"items":[]}`, rec.Body.String())
}

// An administrator writing a grant needs the subject a tuple names, and the two
// ids on an account row are not it. Rendering it is the whole of ADR-20260901-b2z
// rule 35: an admin copies instead of guessing.
func TestAnAccountRendersTheSubjectAGrantNames(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{
		info: AuthInfo{Provider: "local"},
		rows: []Account{{ID: "u-alice", Username: "alice", DisplayName: "Alice"}},
	}

	handler := serverWithAccounts(t, accounts, adminGrants()).Handler()

	rec := call(t, handler, http.MethodGet, "/api/v1/auth/accounts", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Items []Account `json:"items"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	require.Equal(t, "user:u-alice", body.Items[0].Subject)
}

// One call makes a usable person, which is ADR-20260901-b2z rule 36. The role is
// written in the caller's name through SetOrgRole, the path set-account-role
// takes, so it is refused wherever the caller could not have set it by hand. It
// never goes through the grant surface, which refuses the admin role on an org.
func TestCreatingAnAccountWithARoleWritesTheOrgTuple(t *testing.T) {
	t.Parallel()

	accounts := &fakeAccounts{info: AuthInfo{Provider: "local"}}
	grants := adminGrants()

	handler := serverWithAccounts(t, accounts, grants).Handler()

	rec := call(t, handler, http.MethodPost, "/api/v1/auth/accounts",
		`{"username":"bob","password":"a-long-enough-one","role":"operator"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	require.Len(t, grants.roles, 1, "no org role was set for the new account")
	require.Equal(t, "org:local#operator@user:u0123456789abcdefghij", grants.roles[0].String())
	require.Empty(t, grants.written, "the role went through the grant surface")

	// And with no role, nothing is written: an account that holds nothing is
	// the safe default and stays the default.
	rec = call(t, handler, http.MethodPost, "/api/v1/auth/accounts",
		`{"username":"carol","password":"a-long-enough-one"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Len(t, grants.roles, 1, "a role nobody asked for was written")
}

// adminGrants is a grant store whose caller administers this server's identity
// and may write what they ask for, so these cases are about the surface and not
// about the graph's own escalation rule.
type accountAdminGrants struct{ fakeGrantStore }

func (g *accountAdminGrants) Can(
	context.Context, authz.Subject, authz.Action, authz.Object,
) (bool, error) {
	return true, nil
}

func adminGrants() *accountAdminGrants { return &accountAdminGrants{} }
