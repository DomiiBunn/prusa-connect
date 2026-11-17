package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/internal/app/config"
	"github.com/prusa3d/prusa-connect/internal/app/resource"
	"github.com/prusa3d/prusa-connect/internal/platform/auth"
	"github.com/prusa3d/prusa-connect/internal/platform/auth/dbstore"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

const (
	testOwner    = "owner@example.com"
	testPassword = "correct horse battery"
)

// localCore is the configuration a self-hoster runs: no issuer, this server's
// own accounts. The account path names a file that is not there, which is every
// deployment that never ran the build that wrote one.
func localCore(t *testing.T) config.Core {
	t.Helper()

	return config.Core{
		Principal: "operator",
		Auth:      config.Auth{Local: true, Accounts: filepath.Join(t.TempDir(), "accounts.json")},
	}
}

// wiredAccounts builds the account store over a host database of its own, and
// returns the verifier so a test can ask whether a session still resolves.
func wiredAccounts(t *testing.T, core config.Core) (accounts, *auth.Verifier) {
	t.Helper()

	return wiredAccountsOn(t, core, testHost(t))
}

// wiredAccountsOn is wiredAccounts against a host database the caller holds, so
// a test can wire the same database twice and see what the next process sees.
func wiredAccountsOn(t *testing.T, core config.Core, host hostStore) (accounts, *auth.Verifier) {
	t.Helper()

	store, err := dbstore.NewCredentials(host.db, host.engine)
	require.NoError(t, err)

	verifier := auth.NewVerifier(store, nil)

	local, err := openAccounts(t.Context(), core, host, store, verifier, discard())
	require.NoError(t, err)

	return local, verifier
}

// TestASelfHosterSetsUpLogsInAndAddsSomebody is the whole of what the local
// credential store is for, driven through the port the HTTP surface consumes.
func TestASelfHosterSetsUpLogsInAndAddsSomebody(t *testing.T) {
	t.Parallel()

	local, verifier := wiredAccounts(t, localCore(t))

	info, err := local.Info(t.Context())
	require.NoError(t, err)
	require.Equal(t, "local", info.Provider)
	require.True(t, info.Credentials)
	require.True(t, info.NeedsSetup)

	first, err := local.Setup(t.Context(), httpapi.NewAccount{
		Username: testOwner,
		Password: testPassword,
	})
	require.NoError(t, err)

	// The id is the principal the graph's first grant was written for, which is
	// what makes this person the administrator with no second bootstrap door.
	require.Equal(t, "operator", first.ID)

	info, err = local.Info(t.Context())
	require.NoError(t, err)
	require.False(t, info.NeedsSetup)

	opened, err := local.Login(t.Context(), testOwner, testPassword)
	require.NoError(t, err)

	cred, err := verifier.Verify(t.Context(), auth.Token(opened.Token), auth.TransportHTTP)
	require.NoError(t, err)
	require.Equal(t, "operator", cred.Subject)

	added, err := local.Create(t.Context(), httpapi.NewAccount{
		Username: "colleague@example.com",
		Password: "another good password",
	})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, added.ID, "the added account took the administrator's id")

	theirs, err := local.Login(t.Context(), "colleague@example.com", "another good password")
	require.NoError(t, err)

	cred, err = verifier.Verify(t.Context(), auth.Token(theirs.Token), auth.TransportHTTP)
	require.NoError(t, err)
	require.Equal(t, added.ID, cred.Subject)
}

// TestAChangedPasswordEndsTheSessionsOpenedUnderTheOldOne is the attack. The
// account store issues no credential and the credential store knows no
// password, so ending those sessions is a step only this layer can take.
func TestAChangedPasswordEndsTheSessionsOpenedUnderTheOldOne(t *testing.T) {
	t.Parallel()

	local, verifier := wiredAccounts(t, localCore(t))

	_, err := local.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	stolen, err := local.Login(t.Context(), testOwner, testPassword)
	require.NoError(t, err)

	mine, err := local.Login(t.Context(), testOwner, testPassword)
	require.NoError(t, err)

	_, err = local.ChangePassword(t.Context(), "operator", testPassword, "a new password")
	require.NoError(t, err)

	for name, token := range map[string]string{"the stolen one": stolen.Token, "the caller's own": mine.Token} {
		_, verifyErr := verifier.Verify(t.Context(), auth.Token(token), auth.TransportHTTP)
		require.Truef(t, auth.Refused(verifyErr), "%s session survived the password change", name)
	}

	_, err = local.Login(t.Context(), testOwner, testPassword)
	require.True(t, auth.Refused(err), "the old password still logs in")

	_, err = local.Login(t.Context(), testOwner, "a new password")
	require.NoError(t, err)
}

// TestAnAdministratorResetEndsTheSessionsToo is the same rule on the path a
// person who has lost their password takes. An account that may be compromised
// is the whole reason a reset happens.
func TestAnAdministratorResetEndsTheSessionsToo(t *testing.T) {
	t.Parallel()

	local, verifier := wiredAccounts(t, localCore(t))

	_, err := local.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	added, err := local.Create(t.Context(), httpapi.NewAccount{
		Username: "colleague@example.com",
		Password: "another good password",
	})
	require.NoError(t, err)

	theirs, err := local.Login(t.Context(), "colleague@example.com", "another good password")
	require.NoError(t, err)

	require.NoError(t, local.ResetPassword(t.Context(), added.ID, "a reset password"))

	_, err = verifier.Verify(t.Context(), auth.Token(theirs.Token), auth.TransportHTTP)
	require.True(t, auth.Refused(err), "the reset left the account's session alive")
}

// TestDeletingAnAccountEndsItsSessions is the third path to the same property.
// Removing a person who has left must log them out now, not at the end of their
// window.
func TestDeletingAnAccountEndsItsSessions(t *testing.T) {
	t.Parallel()

	local, verifier := wiredAccounts(t, localCore(t))

	_, err := local.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	added, err := local.Create(t.Context(), httpapi.NewAccount{
		Username: "colleague@example.com",
		Password: "another good password",
	})
	require.NoError(t, err)

	theirs, err := local.Login(t.Context(), "colleague@example.com", "another good password")
	require.NoError(t, err)

	require.NoError(t, local.Delete(t.Context(), added.ID))

	_, err = verifier.Verify(t.Context(), auth.Token(theirs.Token), auth.TransportHTTP)
	require.True(t, auth.Refused(err), "a deleted account kept a live session")

	_, err = local.Login(t.Context(), "colleague@example.com", "another good password")
	require.True(t, auth.Refused(err), "a deleted account still logs in")
}

// TestARestartDoesNotReopenTheSetupPath is the door a restart would otherwise
// open. An account store that emptied on restart would make the next caller to
// reach the setup path the administrator, on every restart.
func TestARestartDoesNotReopenTheSetupPath(t *testing.T) {
	t.Parallel()

	core := localCore(t)
	path := filepath.Join(t.TempDir(), "host.db")

	first, _ := wiredAccountsOn(t, core, testHostAt(t, path))

	_, err := first.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	opened, err := first.Login(t.Context(), testOwner, testPassword)
	require.NoError(t, err)

	// The process comes back: the same database, opened again, and everything
	// above it built from scratch.
	second, verifier := wiredAccountsOn(t, core, testHostAt(t, path))

	info, err := second.Info(t.Context())
	require.NoError(t, err)
	require.False(t, info.NeedsSetup, "a restart reopened the first-run path")

	_, err = second.Setup(t.Context(), httpapi.NewAccount{
		Username: "intruder@example.com",
		Password: "an intruder's password",
	})
	requireFaultCode(t, err, fault.AuthSetupClosed.Code())

	// The password survived the restart, and so did the session the first
	// process issued: both are rows in the database this server just reopened.
	cred, err := verifier.Verify(t.Context(), auth.Token(opened.Token), auth.TransportHTTP)
	require.NoError(t, err, "a session issued before the restart was refused after it")
	require.Equal(t, "operator", cred.Subject)

	again, err := second.Login(t.Context(), testOwner, testPassword)
	require.NoError(t, err)
	require.NotEmpty(t, again.Token)
}

// TestAnAccountFileLeftByAnEarlierBuildIsImportedOnce is the one-way door in.
// A deployment that ran the build which kept accounts in a JSON file must not
// meet a server that has never heard of its operators.
func TestAnAccountFileLeftByAnEarlierBuildIsImportedOnce(t *testing.T) {
	t.Parallel()

	core := localCore(t)
	require.NoError(t, os.WriteFile(core.Auth.Accounts, []byte(`{
	  "version": 1,
	  "accounts": [
	    {
	      "id": "operator",
	      "username": "`+testOwner+`",
	      "display_name": "The owner",
	      "password_hash": "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
	      "created_at": "2026-01-01T00:00:00Z",
	      "password_changed_at": "2026-01-01T00:00:00Z"
	    }
	  ]
	}`), 0o600))

	path := filepath.Join(t.TempDir(), "host.db")

	imported, _ := wiredAccountsOn(t, core, testHostAt(t, path))

	info, err := imported.Info(t.Context())
	require.NoError(t, err)
	require.False(t, info.NeedsSetup, "the imported account did not close the setup path")

	rows, err := imported.List(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, testOwner, rows[0].Username)

	// The password is changed after the import, and the file still holds the
	// old hash. Opening the same database again must not put the old one back.
	require.NoError(t, imported.ResetPassword(t.Context(), "operator", testPassword))

	second, _ := wiredAccountsOn(t, core, testHostAt(t, path))

	_, err = second.Login(t.Context(), testOwner, testPassword)
	require.NoError(t, err, "the account file was imported a second time over a changed password")
}

// TestAnUnreadableAccountFileStopsTheServer is fail-closed at startup. A store
// this build cannot read must not be reported as a first run, because the next
// caller to reach the setup path would then take the server.
func TestAnUnreadableAccountFileStopsTheServer(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not json":           "this is not a document",
		"a future version":   `{"version":99,"accounts":[]}`,
		"a row with no id":   `{"version":1,"accounts":[{"username":"owner@example.com"}]}`,
		"a row with no name": `{"version":1,"accounts":[{"id":"operator"}]}`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "accounts.json")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			requireNoAccountStore(t, path)
		})
	}

	// A path this process cannot read at all. It is the case an operator meets
	// on a mounted volume with the wrong owner, and the one where reporting a
	// first run hands the server to whoever asks next.
	t.Run("a path that is not a file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "accounts.json")
		require.NoError(t, os.Mkdir(path, 0o700))

		requireNoAccountStore(t, path)
	})
}

// requireNoAccountStore holds that a store which cannot be read stops the
// server rather than starting it empty.
func requireNoAccountStore(t *testing.T, path string) {
	t.Helper()

	core := config.Core{
		Principal: "operator",
		Auth:      config.Auth{Local: true, Accounts: path},
	}

	_, err := openAccounts(t.Context(), core, testHost(t), auth.NewMemoryStore(), nil, discard())
	require.ErrorIs(t, err, ErrAccountFile)
}

// TestADeploymentWithNoAccountsRefusesEveryPassword is what an OIDC deployment
// and an unauthenticated one wire. Refusing is the point: a store that is
// missing would be a nil dereference, and one that admitted would be a bypass.
func TestADeploymentWithNoAccountsRefusesEveryPassword(t *testing.T) {
	t.Parallel()

	none := noAccounts{info: httpapi.AuthInfo{Provider: "oidc", Redirect: true}}

	info, err := none.Info(t.Context())
	require.NoError(t, err)
	require.Equal(t, "oidc", info.Provider)
	require.True(t, info.Redirect)
	require.False(t, info.Credentials)
	require.False(t, info.NeedsSetup, "a deployment with no accounts reported a first run")

	_, err = none.Login(t.Context(), testOwner, testPassword)
	require.True(t, auth.Refused(err))

	_, err = none.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	requireFaultCode(t, err, fault.AuthSetupClosed.Code())

	_, err = none.ChangePassword(t.Context(), "operator", "a", "b")
	require.True(t, auth.Refused(err))
}

// TestTheIdentityWiringServesOneProvider holds the branch. A deployment has an
// issuer, or its own accounts, or neither, and the third is the one that
// authenticates nobody.
func TestTheIdentityWiringServesOneProvider(t *testing.T) {
	t.Parallel()

	core := localCore(t)

	who, err := buildIdentity(t.Context(), core, testHost(t), identityPlane{},
		newSetupMode(""), discard())
	require.NoError(t, err)

	info, err := who.accounts.Info(t.Context())
	require.NoError(t, err)
	require.Equal(t, "local", info.Provider)
	require.True(t, info.Credentials)

	// The redirect ceremony refuses, because one deployment serves one provider.
	_, err = who.sessions.Begin(t.Context())
	require.True(t, auth.Refused(err), "a local deployment offered to start a redirect login")

	// And a request with no credential resolves to nobody rather than to the
	// configured principal.
	_, err = who.principals.Principal(httptest.NewRequest(http.MethodGet, "/api/v1/resources", nil))
	require.True(t, auth.Refused(err), "a local deployment served a request that carried no credential")
}

// TestAnAnonymousDeploymentStillAnswersHowToLogIn keeps the login report
// working where there is no login. A client asking what to show must get an
// answer rather than an error.
func TestAnAnonymousDeploymentStillAnswersHowToLogIn(t *testing.T) {
	t.Parallel()

	who, err := buildIdentity(t.Context(), config.Core{Principal: "operator"}, testHost(t), identityPlane{},
		newSetupMode(resource.SetupOpen), discard())
	require.NoError(t, err)

	info, err := who.accounts.Info(t.Context())
	require.NoError(t, err)
	require.Empty(t, info.Provider)
	require.False(t, info.Credentials)
	require.False(t, info.Redirect)
	require.False(t, info.NeedsSetup)
}

// requireFaultCode asserts the registered code an error carries.
func requireFaultCode(t *testing.T, err error, want fault.Code) {
	t.Helper()

	var carried *fault.Fault

	require.ErrorAs(t, err, &carried)
	require.Equal(t, want, carried.Code())
}

// TestAPasswordChangeEndsTheTokensThePersonHolds runs the leaked-password case
// over the shipping composition. Whoever held the password could mint tokens,
// mint them for a service account, and pair an outside account to the person.
// The change ends all of it.
func TestAPasswordChangeEndsTheTokensThePersonHolds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	running := start(t, dir, bearerStreamConfig(dir))

	owner := signUpTheOwner(t, running)
	script := mintOver(t, running, owner, `{"name":"script","scopes":["administer"]}`)
	service := mintOver(t, running, owner, `{"name":"ci","scopes":["administer"],"subject":"service_account:ci"}`)

	paired := send(t, running, http.MethodPost, "/api/v1/auth/pairings",
		`{"provider":"prusa-account","subject":"someone-else","principal":"user:operator"}`, owner)
	require.Equal(t, http.StatusCreated, paired.status, paired.body)

	changed := send(t, running, http.MethodPost, "/api/v1/auth/accounts/operator/password",
		`{"current_password":"`+testPassword+`","new_password":"a new password"}`, owner)
	require.Equal(t, http.StatusNoContent, changed.status, changed.body)

	requireRefusedOver(t, running, "the session cookie", owner)
	requireRefusedOver(t, running, "the person's own token", bearerOf(script))
	requireRefusedOver(t, running, "the token the person minted for a service account", bearerOf(service))

	again := logInOver(t, running, testOwner, "a new password")

	pairings := send(t, running, http.MethodGet, "/api/v1/auth/pairings", "", again)
	require.Equal(t, http.StatusOK, pairings.status, pairings.body)
	require.NotContains(t, pairings.body, "someone-else", "the pairing to the person survived the change")
}

// TestAnAdministratorResetEndsTheTokensTheirOwnerMinted is the same rule when
// somebody else resets the password, which is what happens after a leak.
func TestAnAdministratorResetEndsTheTokensTheirOwnerMinted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	running := start(t, dir, bearerStreamConfig(dir))

	owner := signUpTheOwner(t, running)

	const colleague, theirPassword = "colleague@example.com", "another good password"

	added := send(t, running, http.MethodPost, "/api/v1/auth/accounts",
		`{"username":"`+colleague+`","password":"`+theirPassword+`"}`, owner)
	require.Equal(t, http.StatusCreated, added.status, added.body)

	var account httpapi.Account

	require.NoError(t, json.Unmarshal([]byte(added.body), &account))

	theirs := logInOver(t, running, colleague, theirPassword)
	token := mintOver(t, running, theirs, `{"name":"their script","scopes":["administer"]}`)
	service := mintOver(t, running, owner, `{"name":"ci","scopes":["administer"],"subject":"service_account:ci"}`)

	reset := send(t, running, http.MethodPost, "/api/v1/auth/accounts/"+account.ID+"/password",
		`{"new_password":"a reset password"}`, owner)
	require.Equal(t, http.StatusNoContent, reset.status, reset.body)
	require.Empty(t, reset.header.Get("Set-Cookie"), "a reset of somebody else's password handed out a session")

	requireRefusedOver(t, running, "their session cookie", theirs)
	requireRefusedOver(t, running, "their token", bearerOf(token))

	still := send(t, running, http.MethodGet, "/api/v1/auth/credentials", "", owner)
	require.Equal(t, http.StatusOK, still.status, "the reset ended the administrator's own session")

	kept := send(t, running, http.MethodGet, "/api/v1/auth/credentials", "", bearerOf(service))
	require.Equal(t, http.StatusOK, kept.status, "the reset ended a token somebody else minted: %s", kept.body)
}

// TestAnAdministratorAddsAnotherAdministratorInOneCall is ADR-20260901-b2z
// rule 36 for the admin role, against the real graph. The grant surface refuses
// the admin role on an org, so a create that wrote its role there answered 403
// after the account already existed, and left a person holding nothing.
func TestAnAdministratorAddsAnotherAdministratorInOneCall(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	running := start(t, dir, bearerStreamConfig(dir))

	owner := signUpTheOwner(t, running)

	added := send(t, running, http.MethodPost, "/api/v1/auth/accounts",
		`{"username":"second-admin@example.com","password":"another good password","role":"admin"}`, owner)
	require.Equal(t, http.StatusCreated, added.status, added.body)

	var account httpapi.Account

	require.NoError(t, json.Unmarshal([]byte(added.body), &account))

	listed := send(t, running, http.MethodGet, "/api/v1/auth/accounts", "", owner)
	require.Equal(t, http.StatusOK, listed.status, listed.body)

	var page struct {
		Items []httpapi.Account `json:"items"`
	}

	require.NoError(t, json.Unmarshal([]byte(listed.body), &page))

	idx := slices.IndexFunc(page.Items, func(a httpapi.Account) bool { return a.ID == account.ID })
	require.NotEqual(t, -1, idx, "the new account is not listed")
	require.Equal(t, "admin", page.Items[idx].Role, "the new account does not hold the role it was created with")
}

// logInOver logs a person in and returns the headers a request from them
// carries.
func logInOver(t *testing.T, running *process, username, password string) map[string]string {
	t.Helper()

	opened := send(t, running, http.MethodPost, "/api/v1/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, nil)
	require.Equal(t, http.StatusOK, opened.status, opened.body)

	raw := opened.header.Get("Set-Cookie")
	require.NotEmpty(t, raw, "the login set no cookie")

	return map[string]string{"Cookie": strings.SplitN(raw, ";", 2)[0]}
}

// bearerOf is the header a script presents its token in.
func bearerOf(minted mintedToken) map[string]string {
	return map[string]string{"Authorization": "Bearer " + minted.Token}
}

// requireRefusedOver holds that a credential is refused at the running server.
func requireRefusedOver(t *testing.T, running *process, what string, headers map[string]string) {
	t.Helper()

	got := send(t, running, http.MethodGet, "/api/v1/auth/credentials", "", headers)
	require.Equalf(t, http.StatusUnauthorized, got.status, "%s still works: %s", what, got.body)
}

// TestALogoutEndsTheCredentialItCarries is finding CL-86. A logout on a server
// with local accounts expired the cookie and left the credential valid, so a
// copy of the cookie or token worked until it expired on its own.
func TestALogoutEndsTheCredentialItCarries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	running := start(t, dir, bearerStreamConfig(dir))

	owner := signUpTheOwner(t, running)
	script := bearerOf(mintOver(t, running, owner, `{"name":"script","scopes":["administer"]}`))

	// The cookie goes first, so the token's own check before its logout also
	// holds that a logout ends only the credential it carries.
	for _, carried := range []struct {
		what    string
		headers map[string]string
	}{{"the session cookie", owner}, {"the token", script}} {
		before := send(t, running, http.MethodGet, "/api/v1/auth/credentials", "", carried.headers)
		require.Equalf(
			t,
			http.StatusOK,
			before.status,
			"%s is refused before its logout: %s",
			carried.what,
			before.body,
		)

		out := send(t, running, http.MethodPost, "/api/v1/auth/logout", "", carried.headers)
		require.Equalf(t, http.StatusNoContent, out.status, "the logout over %s: %s", carried.what, out.body)

		requireRefusedOver(t, running, carried.what+" after its logout", carried.headers)
	}
}

// TestAMintThatRacedAPasswordChangeDoesNotSurviveIt is the interleaving a
// reviewer found. The mint read the generations and checked the session before
// the change, and its row lands after the change. The row carries the older
// generation, so it is refused.
func TestAMintThatRacedAPasswordChangeDoesNotSurviveIt(t *testing.T) {
	t.Parallel()

	host := testHost(t)

	store, err := dbstore.NewCredentials(host.db, host.engine)
	require.NoError(t, err)

	verifier := auth.NewVerifier(store, nil)

	local, err := openAccounts(t.Context(), localCore(t), host, store, verifier, discard())
	require.NoError(t, err)

	_, err = local.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	minter := apiCredentials{store: store, verifier: verifier, now: time.Now}

	fence, err := minter.Fence(t.Context(), "user:operator", "user:operator", "user:operator")
	require.NoError(t, err)

	_, err = local.ChangePassword(t.Context(), "operator", testPassword, "a new password")
	require.NoError(t, err)

	for name, subject := range map[string]string{"own": "user:operator", "service": "service_account:ci"} {
		raced, issueErr := minter.Issue(t.Context(), httpapi.NewCredential{
			Scopes: []string{"administer"}, Subject: subject, Name: "raced", MintedBy: "user:operator", Fence: &fence,
		})
		require.NoError(t, issueErr)

		_, err = verifier.Verify(t.Context(), auth.Token(raced.Token), auth.TransportHTTP)
		require.Truef(t, auth.Refused(err), "the %s token minted from before the change was accepted", name)
	}
}

// TestAWrongCurrentPasswordIsRefusedAsAWrongPassword holds the answer to a
// wrong current password. The same answer covers a password somebody else
// changed a moment earlier. It is the login's refusal and not a server failure.
func TestAWrongCurrentPasswordIsRefusedAsAWrongPassword(t *testing.T) {
	t.Parallel()

	local, _ := wiredAccounts(t, localCore(t))

	_, err := local.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	_, err = local.ChangePassword(t.Context(), "operator", "not the password", "a new password")
	requireFaultCode(t, err, fault.AuthLoginRefused.Code())
}

// TestChangingYourOwnPasswordKeepsYouSignedIn is the person at the account
// screen. The change ends every session they held, this one included, and
// the answer carries a new session made at the new generation. The screen
// that says the change worked is still signed in.
func TestChangingYourOwnPasswordKeepsYouSignedIn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	running := start(t, dir, bearerStreamConfig(dir))

	owner := signUpTheOwner(t, running)

	changed := send(t, running, http.MethodPost, "/api/v1/auth/accounts/operator/password",
		`{"current_password":"`+testPassword+`","new_password":"a new password"}`, owner)
	require.Equal(t, http.StatusNoContent, changed.status, changed.body)

	raw := changed.header.Get("Set-Cookie")
	require.NotEmpty(t, raw, "the change answered no new session")

	fresh := map[string]string{"Cookie": strings.SplitN(raw, ";", 2)[0]}

	requireRefusedOver(t, running, "the session the change was made from", owner)

	still := send(t, running, http.MethodGet, "/api/v1/auth/credentials", "", fresh)
	require.Equal(t, http.StatusOK, still.status, "the new session does not work: %s", still.body)
}

// TestAPasswordChangeEndsThePairingsThePersonMade is the review's case. With
// the person's session, an attacker pairs their own Prusa Account to a service
// account. The pairing names the service account, not the person, and the
// change still ends it, because the person made it.
func TestAPasswordChangeEndsThePairingsThePersonMade(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	running := start(t, dir, bearerStreamConfig(dir))

	owner := signUpTheOwner(t, running)
	mintOver(t, running, owner, `{"name":"ci","scopes":["administer"],"subject":"service_account:ci"}`)

	paired := send(t, running, http.MethodPost, "/api/v1/auth/pairings",
		`{"provider":"prusa-account","subject":"attacker","principal":"service_account:ci"}`, owner)
	require.Equal(t, http.StatusCreated, paired.status, paired.body)

	changed := send(t, running, http.MethodPost, "/api/v1/auth/accounts/operator/password",
		`{"current_password":"`+testPassword+`","new_password":"a new password"}`, owner)
	require.Equal(t, http.StatusNoContent, changed.status, changed.body)

	again := logInOver(t, running, testOwner, "a new password")

	pairings := send(t, running, http.MethodGet, "/api/v1/auth/pairings", "", again)
	require.Equal(t, http.StatusOK, pairings.status, pairings.body)
	require.NotContains(t, pairings.body, "attacker", "the pairing the person made survived the change")
}

// TestTheSessionAfterAChangeCarriesThatChangesGeneration holds the new session
// to the change that made it. A reset lands between the write and the mint.
// The session must carry the change's generation, which the reset has already
// passed, so the reset still ends it.
func TestTheSessionAfterAChangeCarriesThatChangesGeneration(t *testing.T) {
	t.Parallel()

	local, verifier := wiredAccounts(t, localCore(t))

	_, err := local.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	local.betweenChangeAndSession = func(ctx context.Context) {
		require.NoError(t, local.ResetPassword(ctx, "operator", "a reset password"))
	}

	fresh, err := local.ChangePassword(t.Context(), "operator", testPassword, "a new password")
	require.NoError(t, err)

	_, err = verifier.Verify(t.Context(), auth.Token(fresh.Token), auth.TransportHTTP)
	require.True(t, auth.Refused(err), "the session after the change outlived a reset that came after it")
}

// TestAPasswordChangeEndsAChainThroughAnotherPerson is the review's chain. With
// the owner's stolen session an attacker pairs their account to bob, which is
// a sign-in. With that token they mint for a service account and write a
// second pairing, both naming bob as their maker. Both record the owner as
// their root, so the owner's password change ends them.
func TestAPasswordChangeEndsAChainThroughAnotherPerson(t *testing.T) {
	t.Parallel()

	host := testHost(t)

	store, err := dbstore.NewCredentials(host.db, host.engine)
	require.NoError(t, err)

	verifier := auth.NewVerifier(store, nil)

	local, err := openAccounts(t.Context(), localCore(t), host, store, verifier, discard())
	require.NoError(t, err)

	_, err = local.Setup(t.Context(), httpapi.NewAccount{Username: testOwner, Password: testPassword})
	require.NoError(t, err)

	bob, err := local.Create(t.Context(), httpapi.NewAccount{Username: "bob@example.com", Password: "bob's password"})
	require.NoError(t, err)

	pairingRows, err := dbstore.NewPairings(host.db, host.engine)
	require.NoError(t, err)

	pairings := subjectPairings{store: pairingRows, generations: store.Generation, now: time.Now}
	minter := apiCredentials{store: store, verifier: verifier, now: time.Now}

	// The owner's session writes the first pairing, to bob.
	_, err = pairings.Pair(t.Context(), httpapi.NewPairing{
		Provider: "prusa-account", Subject: "attacker", Principal: "user:" + bob.ID,
		CreatedBy: "user:operator", RootCreator: "user:operator",
	})
	require.NoError(t, err)

	// Bob's paired token writes the second pairing and mints, under the root
	// of the pairing it came through.
	_, err = pairings.Pair(t.Context(), httpapi.NewPairing{
		Provider: "prusa-account", Subject: "second", Principal: "service_account:ci",
		CreatedBy: "user:" + bob.ID, RootCreator: "user:operator",
	})
	require.NoError(t, err)

	minted, err := minter.Issue(t.Context(), httpapi.NewCredential{
		Scopes: []string{
			"administer",
		},
		Subject:     "service_account:ci",
		Name:        "ci",
		MintedBy:    "user:" + bob.ID,
		RootCreator: "user:operator",
	})
	require.NoError(t, err)

	_, err = local.ChangePassword(t.Context(), "operator", testPassword, "a new password")
	require.NoError(t, err)

	_, err = verifier.Verify(t.Context(), auth.Token(minted.Token), auth.TransportHTTP)
	require.True(t, auth.Refused(err), "the token minted down the chain survived the root's change")

	for _, subject := range []string{"attacker", "second"} {
		_, err = pairingRows.Lookup(t.Context(), "prusa-account", subject)
		require.ErrorIsf(t, err, auth.ErrNoPairing, "the pairing %q survived the root's change", subject)
	}
}
