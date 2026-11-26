package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prusa3d/prusa-connect/internal/app/config"
	"github.com/prusa3d/prusa-connect/internal/app/resource"
	"github.com/prusa3d/prusa-connect/internal/platform/auth"
	"github.com/prusa3d/prusa-connect/internal/platform/auth/dbstore"
	"github.com/prusa3d/prusa-connect/internal/platform/auth/local"
	"github.com/prusa3d/prusa-connect/internal/platform/auth/session"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/authapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// errNoAccountStore is the cause behind a refusal from a deployment that holds
// no accounts. It reaches a log line and never a client.
var errNoAccountStore = errors.New("app: this deployment holds no local accounts")

// accounts is the local credential store as the HTTP surface consumes it. It is
// in this package because the transport may not import the platform group and
// the platform group may not name the transport's types.
//
// A password change also ends credentials here. The account store issues
// nothing and the credential store knows no passwords, so only the composition
// root can end the credentials a changed password invalidates.
type accounts struct {
	store    *local.Accounts
	login    *session.Passwords
	verifier *auth.Verifier
	pairings auth.Pairings
	// sessions is the credential store a new session is saved to.
	sessions auth.Store
	// betweenChangeAndSession runs after a password change is written and
	// before its new session is minted. Only a test sets it, to land another
	// change in that gap.
	betweenChangeAndSession func(ctx context.Context)
	logger                  *slog.Logger
	// firstID is the id the first administrator is created under: the principal
	// the authorization graph's first grant was written for. The person who
	// runs setup gets that id and so is an administrator. No second bootstrap
	// path exists and no grant is written outside the graph's own rules.
	firstID string
	// mode is the first-run decision on an install that had taken none when
	// it started, and nil on every other. Once it records open mode, nobody
	// signs in with a password and no administrator is missing.
	mode *setupMode
}

var _ httpapi.Accounts = accounts{}

// Info says this deployment serves a password login, and whether the first
// administrator exists yet. An install that recorded open mode while this
// process runs answers as one that started open.
func (a accounts) Info(ctx context.Context) (httpapi.AuthInfo, error) {
	if a.mode != nil && a.mode.get() == resource.SetupOpen {
		return httpapi.AuthInfo{}, nil
	}

	needed, err := a.store.NeedsSetup(ctx)
	if err != nil {
		return httpapi.AuthInfo{}, err
	}

	return httpapi.AuthInfo{
		Provider:    local.Type,
		Credentials: true,
		NeedsSetup:  needed,
	}, nil
}

// Setup creates the first administrator.
func (a accounts) Setup(ctx context.Context, spec httpapi.NewAccount) (httpapi.Account, error) {
	created, err := a.store.CreateFirst(ctx, a.firstID, newAccountOf(spec))
	if err != nil {
		return httpapi.Account{}, accountRefusal(err)
	}

	a.logger.InfoContext(ctx, "the first administrator was created",
		"account", created.ID, "username", created.Username)

	return accountOf(created), nil
}

// Login turns a username and password into a session credential.
func (a accounts) Login(ctx context.Context, username, password string) (httpapi.Session, error) {
	result, err := a.login.Login(ctx, username, password)
	if err != nil {
		return httpapi.Session{}, err
	}

	return httpapi.Session{
		Token:     string(result.Token),
		ExpiresAt: result.Credential.ExpiresAt,
	}, nil
}

// List returns every account.
func (a accounts) List(ctx context.Context) ([]httpapi.Account, error) {
	rows, err := a.store.List(ctx)
	if err != nil {
		return nil, accountRefusal(err)
	}

	out := make([]httpapi.Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, accountOf(row))
	}

	return out, nil
}

// Get returns one account by its id, and the same not-found refusal every other
// operation on a missing account gives.
func (a accounts) Get(ctx context.Context, id string) (httpapi.Account, error) {
	row, err := a.store.Get(ctx, id)
	if err != nil {
		return httpapi.Account{}, accountRefusal(err)
	}

	return accountOf(row), nil
}

// Create adds an account under a minted id.
func (a accounts) Create(ctx context.Context, spec httpapi.NewAccount) (httpapi.Account, error) {
	created, err := a.store.Create(ctx, newAccountOf(spec))
	if err != nil {
		return httpapi.Account{}, accountRefusal(err)
	}

	return accountOf(created), nil
}

// Delete removes an account and every credential it holds, so the person is
// logged out of every transport at once and not at their next expiry.
func (a accounts) Delete(ctx context.Context, id string) error {
	if err := a.store.Delete(ctx, id); err != nil {
		return accountRefusal(err)
	}

	return a.endCredentials(ctx, id)
}

// ChangePassword replaces a person's own password, ends every session and api
// credential they hold, and returns a new session for them. The new session
// is made after the change and carries the new generation, so the person who
// changed the password stays signed in.
func (a accounts) ChangePassword(ctx context.Context, id, current, next string) (httpapi.Session, error) {
	generation, err := a.store.ChangePasswordAtGeneration(ctx, id, []byte(current), []byte(next))
	if err != nil {
		return httpapi.Session{}, accountRefusal(err)
	}

	if err = a.endCredentials(ctx, id); err != nil {
		return httpapi.Session{}, err
	}

	if a.betweenChangeAndSession != nil {
		a.betweenChangeAndSession(ctx)
	}

	token, cred, err := auth.IssueAtGeneration(ctx, a.sessions, id, auth.KindSession, time.Now(), generation)
	if err != nil {
		return httpapi.Session{}, httpfault.Unexpected("open a session after the password change").Wrap(err)
	}

	return httpapi.Session{Token: string(token), ExpiresAt: cred.ExpiresAt}, nil
}

// ResetPassword replaces a password without the current one and ends every
// session and api credential the account holds.
func (a accounts) ResetPassword(ctx context.Context, id, next string) error {
	// The account is read first, so a reset against an id nobody holds is a
	// not-found and not a store error. The refusal is then the same one every
	// other operation on a missing account gives.
	if _, err := a.store.Get(ctx, id); err != nil {
		return accountRefusal(err)
	}

	if err := a.store.SetPassword(ctx, id, []byte(next)); err != nil {
		return accountRefusal(err)
	}

	return a.endCredentials(ctx, id)
}

// endCredentials revokes every credential an account holds. It runs after the
// password has changed. A store that cannot answer leaves the old credentials
// alive, so the failure is reported to the caller.
func (a accounts) endCredentials(ctx context.Context, id string) error {
	ended, err := revokeAccountCredentials(ctx, a.verifier, a.pairings, id)
	if err != nil {
		// A cancellation stays a failure here, unlike in accountRefusal. The
		// password has changed and the old credentials may still work, which an
		// operator has to see even when the caller has left.
		return httpfault.Unexpected("end the sessions and api credentials of the account that changed").Wrap(err)
	}

	if ended != (endedCredentials{}) {
		a.logger.InfoContext(ctx, "credentials ended by an account change", "account", id,
			"sessions", ended.sessions, "api_credentials", ended.apiCredentials,
			"minted_for_others", ended.mintedForOthers, "pairings", ended.pairings)
	}

	return nil
}

// subjectEnder ends every credential held for a set of subjects and dates the
// end. The verifier satisfies it, on a running server and in the shell
// commands.
type subjectEnder interface {
	EndSubjects(ctx context.Context, subjects ...string) (auth.Ended, error)
}

// principalUnpairer removes every pairing to one principal, and every pairing
// one person wrote.
type principalUnpairer interface {
	DeletePrincipal(ctx context.Context, principal string) (int, error)
	DeleteCreatedBy(ctx context.Context, creator string) (int, error)
}

// endedCredentials counts what a revocation removed, so the caller can say it.
type endedCredentials struct {
	sessions        int
	apiCredentials  int
	mintedForOthers int
	pairings        int
}

// revokeAccountCredentials ends everything a leaked password could have left
// behind. That is every session and api credential the account holds, every
// api credential it minted for a service account, and every pairing that lets
// an outside account act as it.
//
// A session is stored under the bare account id, and an api credential under
// the graph spelling user:<id>, so both are ended together. The pairings go
// first, so an outside account cannot act as the person while the rest ends.
func revokeAccountCredentials(
	ctx context.Context, store subjectEnder, pairings principalUnpairer, id string,
) (endedCredentials, error) {
	unpaired, err := pairings.DeletePrincipal(ctx, subjectOf(id))
	if err != nil {
		return endedCredentials{}, err
	}

	written, err := pairings.DeleteCreatedBy(ctx, subjectOf(id))
	if err != nil {
		return endedCredentials{pairings: unpaired}, err
	}

	unpaired += written

	ended, err := store.EndSubjects(ctx, id, subjectOf(id))
	if err != nil {
		return endedCredentials{pairings: unpaired}, err
	}

	return endedCredentials{
		sessions:        ended.Held[0],
		apiCredentials:  ended.Held[1],
		mintedForOthers: ended.MintedForOthers,
		pairings:        unpaired,
	}, nil
}

// noAccounts is the local credential store of a deployment that has none: one
// with an OIDC issuer, and one that authenticates nobody.
//
// It refuses to create an account and refuses every password, as noIssuer does
// for the redirect flow. Info still answers, because a login screen must be
// able to ask what this server serves without holding a credential.
type noAccounts struct {
	info httpapi.AuthInfo
}

var _ httpapi.Accounts = noAccounts{}

func (n noAccounts) Info(context.Context) (httpapi.AuthInfo, error) { return n.info, nil }

func (noAccounts) Setup(context.Context, httpapi.NewAccount) (httpapi.Account, error) {
	return httpapi.Account{}, fault.AuthSetupClosed.New(
		"this server holds no local accounts, so there is no first administrator to create").
		Wrap(errNoAccountStore)
}

func (noAccounts) Login(context.Context, string, string) (httpapi.Session, error) {
	return httpapi.Session{}, auth.Refuse(errNoAccountStore)
}

func (noAccounts) List(context.Context) ([]httpapi.Account, error) {
	return []httpapi.Account{}, nil
}

func (noAccounts) Get(context.Context, string) (httpapi.Account, error) {
	return httpapi.Account{}, httpfault.ResourceNotFound(httpfault.Reference("account", ""))
}

func (noAccounts) Create(context.Context, httpapi.NewAccount) (httpapi.Account, error) {
	return httpapi.Account{}, fault.AuthSetupClosed.New(
		"this server holds no local accounts, so there is nothing to add a person to").
		Wrap(errNoAccountStore)
}

func (noAccounts) Delete(context.Context, string) error {
	return httpfault.ResourceNotFound(httpfault.Reference("account", ""))
}

func (noAccounts) ChangePassword(context.Context, string, string, string) (httpapi.Session, error) {
	return httpapi.Session{}, auth.Refuse(errNoAccountStore)
}

func (noAccounts) ResetPassword(context.Context, string, string) error {
	return httpfault.ResourceNotFound(httpfault.Reference("account", ""))
}

// openAccounts builds the local account store this process runs with, over the
// host's own database, and imports the account file a previous build left
// behind if there is one.
//
// The accounts are in the database so that an account and the grants written
// for it share one backup, one restore and one engine selector. A separate
// file could be restored to a different moment than the graph beside it.
func openAccounts(
	ctx context.Context, core config.Core, host hostStore,
	store auth.Store, verifier *auth.Verifier, logger *slog.Logger,
) (accounts, error) {
	rows, err := accountRows(host)
	if err != nil {
		return accounts{}, fmt.Errorf("open the account store: %w", err)
	}

	if err = importAccountFile(ctx, core.Auth.Accounts, rows, logger); err != nil {
		return accounts{}, err
	}

	pairings, err := dbstore.NewPairings(host.db, host.engine)
	if err != nil {
		return accounts{}, fmt.Errorf("open the pairing store: %w", err)
	}

	options, err := accountOptions(core)
	if err != nil {
		return accounts{}, err
	}

	credentials := local.New(rows, nil, options...)

	return accounts{
		store:    credentials,
		login:    session.NewPasswords(credentials, store, nil),
		verifier: verifier,
		pairings: pairings,
		sessions: store,
		logger:   logger,
		firstID:  core.Principal,
	}, nil
}

// importFirst writes the first administrator from a hash the first-run wizard
// made before the restart.
func (a accounts) importFirst(ctx context.Context, username, hash string) error {
	_, err := a.store.ImportFirst(ctx, a.firstID, username, hash)

	return err
}

// accountOptions are the settings of the local account store that come from
// the configuration. The server and the account commands both read them, so a
// password set from the command line is hashed at the same cost as a login.
func accountOptions(core config.Core) ([]local.Option, error) {
	hasher, err := auth.NewHasher(core.Auth.PasswordCost())
	if err != nil {
		return nil, fmt.Errorf("make the password hasher: %w", err)
	}

	return []local.Option{
		local.WithHasher(hasher),
		local.WithHashesAtOnce(core.Auth.PasswordHashesAtOnce),
	}, nil
}

// accountRows is the account store over the host's database. A password change
// there raises the generation of both spellings of the account, the bare id a
// session names and the user:<id> an api credential names.
func accountRows(host hostStore) (*dbstore.Accounts, error) {
	rows, err := dbstore.NewAccounts(host.db, host.engine)
	if err != nil {
		return nil, err
	}

	return rows.WithCredentialSubjects(func(id string) []string {
		return []string{id, subjectOf(id)}
	}), nil
}

// accountOf renders a stored account the way the HTTP surface publishes it.
func accountOf(account local.Account) httpapi.Account {
	return httpapi.Account{
		ID:          account.ID,
		Username:    account.Username,
		DisplayName: account.DisplayName,
		CreatedAt:   account.CreatedAt,
	}
}

// newAccountOf reads what the HTTP surface accepted as the store takes it.
func newAccountOf(spec httpapi.NewAccount) local.NewAccount {
	return local.NewAccount{
		Username:    spec.Username,
		DisplayName: spec.DisplayName,
		Password:    []byte(spec.Password),
	}
}

// accountRefusal turns the account store's errors into the ones a client sees.
//
// Everything the store can refuse is named here. Anything else is a failure of
// this server and is reported as one. A caller told that the input was bad
// when the disk was full would rewrite a password that was fine.
func accountRefusal(err error) error {
	switch {
	case errors.Is(err, local.ErrSetupClosed):
		return fault.AuthSetupClosed.New(
			"this server already has an administrator, so setup is closed").Wrap(err)

	case errors.Is(err, local.ErrUsernameTaken), errors.Is(err, local.ErrIDTaken):
		return fault.AuthAccountExists.New(
			"an account already holds that username").Wrap(err)

	case errors.Is(err, local.ErrLastAccount):
		return fault.AuthLastAccount.New(
			"this is the only account on this server, and removing it would leave nobody able to log in").
			Wrap(err)

	case errors.Is(err, local.ErrNoAccount):
		return httpfault.ResourceNotFound(httpfault.Reference("account", ""))

	case auth.Refused(err):
		return err

	case errors.Is(err, authapi.ErrInvalidCredentials):
		return fault.AuthLoginRefused.New("the current password is not this account's password").Wrap(err)

	case errors.Is(err, local.ErrInvalidAccount):
		return invalidAccountFault(err)
	}

	if raised, ok := errors.AsType[*fault.Fault](err); ok {
		return raised
	}

	// A caller who hung up is not a failure of this store. The HTTP surface
	// tells the two apart, and only while the cancellation is not a fault.
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("read or write the account store: %w", err)
	}

	return httpfault.Unexpected("read or write the account store").Wrap(err)
}

// invalidAccountFault names the field the store refused, so a caller is not
// left comparing two bounds to guess which one they crossed.
func invalidAccountFault(err error) error {
	switch {
	case errors.Is(err, auth.ErrPasswordTooShort):
		return httpfault.RequestInvalid("body.password",
			fmt.Sprintf("at least %d bytes", auth.MinPasswordBytes),
			"a shorter password is below the floor NIST SP 800-63B section 5.1.1.2 sets")

	case errors.Is(err, auth.ErrPasswordTooLong):
		return httpfault.RequestInvalid("body.password",
			fmt.Sprintf("at most %d bytes", auth.MaxPasswordBytes),
			"the hasher takes no more, and a longer password would be silently truncated")

	default:
		return httpfault.RequestInvalid("body.username",
			fmt.Sprintf("1 to %d bytes and no whitespace", local.MaxUsernameBytes),
			"the username is not one this server can store")
	}
}
