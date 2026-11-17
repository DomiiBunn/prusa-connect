package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prusa3d/prusa-connect/internal/authz"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/internal/transport/http/loginguard"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// The local account paths. They sit under the login ceremony's prefix because
// they are the same subject: how a person gets a credential on this server.
const (
	authInfoPath        = "/api/v1/auth/info"
	setupPath           = "/api/v1/auth/setup"
	accountsPath        = "/api/v1/auth/accounts"
	accountPath         = "/api/v1/auth/accounts/{account}"
	accountPasswordPath = "/api/v1/auth/accounts/{account}/password" //nolint:gosec // a route, not a password
	accountRolePath     = "/api/v1/auth/accounts/{account}/role"
)

// Accounts is the local credential store as this surface uses it. It is
// declared here because this is where it is consumed. The composition root
// satisfies it, because only the composition root may name both a credential
// store and a session.
//
// No method takes a principal. Who may administer accounts is a question for
// the authorization graph, and it is asked here, on the resource kind the
// accounts belong to, so a second transport asking it gets the same answer.
type Accounts interface {
	// Info is what a caller with no credential is told: which login this
	// deployment serves, and whether the first administrator exists yet.
	Info(ctx context.Context) (AuthInfo, error)
	// Setup creates the first administrator, under the id the authorization
	// graph's first grant already names. It is refused once an account exists.
	Setup(ctx context.Context, spec NewAccount) (Account, error)
	// Login turns a username and password into a session credential. Every
	// refusal it gives reads the same, whatever caused it.
	Login(ctx context.Context, username, password string) (Session, error)
	// List returns every account, without password hashes.
	List(ctx context.Context) ([]Account, error)
	// Get returns one account by its id, and a not-found refusal for an id this
	// server holds nothing under. It is what an operation writing about a
	// person asks before it writes, so an id nobody holds is refused rather
	// than recorded somewhere else.
	Get(ctx context.Context, id string) (Account, error)
	// Create adds an account under a minted id. The id holds no grant, so a new
	// person can log in and can do nothing until they are granted something.
	Create(ctx context.Context, spec NewAccount) (Account, error)
	// Delete removes an account and every credential it holds. The last account
	// is refused: nothing could then log in, and the setup path would reopen.
	Delete(ctx context.Context, id string) error
	// ChangePassword replaces a person's own password after checking the
	// current one. It ends every credential the account holds or minted, and
	// every pairing to it. That ends the caller's own session too, so it
	// returns a new one, made after the change, for the answer to carry.
	ChangePassword(ctx context.Context, id string, current, next string) (Session, error)
	// ResetPassword replaces a password without asking for the current one. It
	// ends every credential the account holds or minted, and every pairing to
	// it. It is the administrator's, for somebody who has lost theirs.
	ResetPassword(ctx context.Context, id string, next string) error
}

// Profiles reads what an identity provider said about a person who signed in
// there.
type Profiles interface {
	// Profile returns the person under a bare user id. Found is false for an
	// id nobody signed in as.
	Profile(ctx context.Context, id string) (profile Profile, found bool, err error)
}

// Profile is what an identity provider last said a person is called.
type Profile struct {
	DisplayName string
	Email       string
}

// AuthInfo is what the login screen reads before anybody has a credential.
//
// It names no account and counts nothing. A caller with no credential learns
// which flow to offer and whether this server has been set up, and nothing
// about who is on it.
type AuthInfo struct {
	// Provider is the identity provider this deployment serves, or empty when
	// it authenticates nobody.
	Provider string `json:"provider"`
	// ProviderName is what the sign-in button calls the provider:
	// core.auth.provider_name, or the issuer's host name. Empty when there is
	// no redirect sign-in.
	ProviderName string `json:"provider_name,omitempty" doc:"What the sign-in button calls the provider: core.auth.provider_name, or the issuer's host name."`
	// Credentials is true when a username and password are accepted here.
	Credentials bool `json:"credentials"`
	// Redirect is true when a login starts by sending the browser elsewhere.
	Redirect bool `json:"redirect"`
	// NeedsSetup is true while no account exists and the setup path is open.
	NeedsSetup bool `json:"needs_setup"`
}

// Account is one person, as this surface renders them. There is no password
// hash on it and no field one could travel in.
type Account struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	// Subject is the id in tuple spelling, user:<id>. A grant names this on
	// the subject side. The server renders it so that an administrator
	// writing a grant can copy it and does not have to guess which of the two
	// ids the graph wants.
	Subject string `json:"subject"`
	// Role is the org role this account holds, and empty for an account that
	// holds none. A page listing people has to say what each of them may do,
	// and reading the grants for every row one at a time is a request per
	// person.
	Role string `json:"role,omitempty" doc:"The org role this account holds: viewer, operator or admin. Empty is an account holding no role, which can log in and do nothing. Highest first where the graph holds more than one, and grants carries all of them."`
	// Grants is every relation this account holds on the org, in the order the
	// roles rank. Role is the first of them. An operation that withdraws a
	// relation has to name it, so a page changing a role reads the names here
	// and does not compose tuple spelling of its own.
	Grants []string `json:"grants,omitempty" doc:"Every org relation this account holds, highest role first. Change the role with set-account-role, which replaces all of them with the one it is given."`
}

// NewAccount is what a caller supplies to create one.
type NewAccount struct {
	Username    string
	DisplayName string
	Password    string
}

// accountBody is the create and setup body. The bounds are the store's,
// restated here so an oversized field is refused before anything is hashed.
type accountBody struct {
	Username    string `json:"username"               doc:"The name this person logs in with, compared case-folded."         maxLength:"254" minLength:"1"`
	DisplayName string `json:"display_name,omitempty" doc:"The label a UI renders. Empty takes the username."                maxLength:"128"`
	Password    string `json:"password"               doc:"At least 8 bytes and at most 72, which is what the hasher takes." maxLength:"72"  minLength:"8"`
	// Role is the org role to write for the new account, in the same call.
	// Without it a created account holds nothing and can do nothing. That is
	// safe, but the administrator then has to know to make a second call.
	// With it, one call makes a usable person.
	Role string `json:"role,omitempty" doc:"An org role to grant in the same transaction: viewer, operator or admin. Empty creates an account holding nothing." enum:"viewer,operator,admin"`
}

type authInfoOutput struct {
	Body AuthInfo
}

type setupInput struct {
	Body firstAccountBody
}

// firstAccountBody is the first administrator with the one-time setup code the
// server printed in its log. It spells out the account fields rather than
// embedding accountBody, because the schema reader does not flatten an
// embedded struct.
type firstAccountBody struct {
	Username    string `json:"username"               doc:"The name this person logs in with, compared case-folded."                                                           maxLength:"254" minLength:"1"`
	DisplayName string `json:"display_name,omitempty" doc:"The label a UI renders. Empty takes the username."                                                                  maxLength:"128"`
	Password    string `json:"password"               doc:"At least 8 bytes and at most 72, which is what the hasher takes."                                                   maxLength:"72"  minLength:"8"`
	Role        string `json:"role,omitempty"         doc:"An org role to grant in the same transaction: viewer, operator or admin. Empty creates an account holding nothing."                               enum:"viewer,operator,admin"`
	Code        string `json:"code,omitempty"         doc:"The one-time setup code this server printed in its log when it started, or the value of core.auth.setup_code."      maxLength:"128"`
}

// account is the body without the code, in the shape the account store takes.
func (b firstAccountBody) account() accountBody {
	return accountBody{Username: b.Username, DisplayName: b.DisplayName, Password: b.Password, Role: b.Role}
}

type accountOutput struct {
	Status int
	Body   Account
}

// loginBody is a credential login. The password travels in the body and not
// in a Basic header, because a proxy that logs request headers would keep it.
type loginBody struct {
	Username string `json:"username" doc:"The name this person logs in with."            maxLength:"254" minLength:"1"`
	Password string `json:"password" doc:"Their password. It is never logged or echoed." maxLength:"72"  minLength:"1"`
}

type loginInput struct {
	Body loginBody
}

// loginOutput sets the credential and says when it stops being one. The cookie
// is HttpOnly, so no script reads it, and SameSite=Lax, so it does not ride a
// cross-site form post.
type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie" doc:"The session credential, HttpOnly and SameSite=Lax."`

	Body struct {
		ExpiresAt time.Time `json:"expires_at" doc:"When this server stops accepting the credential."`
	}
}

type listAccountsOutput struct {
	Body struct {
		Items []Account `json:"items"`
	}
}

// AccountRef is the account a path names. It is exported because Huma binds
// parameters by reflection and cannot write into an unexported embedded field.
type AccountRef struct {
	Account string `path:"account" doc:"The account's id, as the listing renders it." maxLength:"63" example:"u4k2p9z1m7x3c8v5b0nq"`
}

type createAccountInput struct {
	Body accountBody
}

type deleteAccountInput struct {
	AccountRef
}

type deleteAccountOutput struct {
	Status int
}

// passwordBody carries the new password and, for a person changing their own,
// the current one. An administrator resetting somebody else's sends no current
// password, and the graph is what decides whether they may.
type passwordBody struct {
	CurrentPassword string `json:"current_password,omitempty" doc:"Required to change your own password. An administrator resetting another account sends none." maxLength:"72"`
	NewPassword     string `json:"new_password"               doc:"At least 8 bytes and at most 72."                                                             maxLength:"72" minLength:"8"`
}

type setPasswordInput struct {
	AccountRef

	Body passwordBody
}

type setPasswordOutput struct {
	Status int
	// SetCookie is the new session a person receives for changing their own
	// password, which ended the session they sent the change with.
	SetCookie http.Cookie `header:"Set-Cookie" doc:"A new session credential, when you changed your own password. The change ended the one you sent. Absent after a reset of somebody else's."`
}

// roleBody is the role to put an account on. It carries no enum, so a role this
// server does not have is refused by the graph's own vocabulary and the answer
// names the relation and the object type it was not found on. The three roles
// are named in the description, which is where a client reads them.
type roleBody struct {
	Role string `json:"role" doc:"The role this account is to hold: viewer, operator or admin." maxLength:"63" minLength:"1"`
}

type setRoleInput struct {
	AccountRef

	Body roleBody
}

type setRoleOutput struct {
	Status int
}

// registerAccounts publishes the local account surface. The operations are
// registered whatever the process was wired with, so the document a client
// generates from does not depend on whether this deployment holds accounts.
func registerAccounts(api huma.API, cfg Config) {
	registerAuthInfo(api, cfg)
	registerSetup(api, cfg)
	registerPasswordLogin(api, cfg)
	registerListAccounts(api, cfg)
	registerCreateAccount(api, cfg)
	registerDeleteAccount(api, cfg)
	registerSetAccountPassword(api, cfg)
	registerSetAccountRole(api, cfg)
}

func registerAuthInfo(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "get-auth-info",
		Method:      http.MethodGet,
		Path:        authInfoPath,
		Summary:     "How to log in to this server",
		Description: "Reports which identity provider this deployment serves and whether the first " +
			"administrator has been created. It names no account and counts none: a caller with no " +
			"credential learns which form to show and nothing about who is on this server.",
		Tags:     []string{"auth"},
		Security: noSecurity,
		Errors:   []int{http.StatusInternalServerError},
	}, func(ctx context.Context, _ *struct{}) (*authInfoOutput, error) {
		info, err := cfg.Accounts.Info(ctx)
		if err != nil {
			return nil, httpfault.Unexpected("read how this server authenticates")
		}

		return &authInfoOutput{Body: info}, nil
	})
}

func registerSetup(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "create-first-account",
		Method:      http.MethodPost,
		Path:        setupPath,
		Summary:     "Create the first administrator",
		Description: "Creates the account this server's first grant was written for, which is what makes " +
			"the person who runs it an administrator. It is refused once an account exists, and two calls " +
			"arriving together create one account: the id is fixed, so the second write has nowhere to go.\n\n" +
			"`code` is the one-time setup code the server printed in its log, as for POST /api/v1/setup. " +
			"A missing or wrong code answers 403, and an address that sent too many wrong codes answers 429.",
		Tags:          []string{"auth"},
		Security:      noSecurity,
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *setupInput) (*accountOutput, error) {
		var created Account

		client := setupClientOf(cfg, requestFrom(ctx))

		err := cfg.Setup.FirstAccount(ctx, client, in.Body.Code, func(ctx context.Context) error {
			var err error

			created, err = cfg.Accounts.Setup(ctx, newAccountOf(in.Body.account()))

			return err
		})
		if err != nil {
			return nil, withRetryAfter(accountFault(err, "create the first administrator"))
		}

		created.Subject = subjectOfAccount(created)

		return &accountOutput{Status: http.StatusCreated, Body: created}, nil
	})
}

// loginLimited refuses a login from an address that already has attempts
// queued past the guard's ceiling. It says nothing about the account or the
// password, which were never checked, so it tells a caller nothing about which
// usernames exist.
var loginLimited = fault.Register(fault.DomainAuth(), "LOGIN_LIMITED", http.StatusTooManyRequests,
	fault.Document(
		"Too many login attempts are waiting from this address",
		"Wait for the time Retry-After gives in seconds, then log in again. Attempts from one "+
			"address are checked one after another.",
		fault.RetryAllowed,
		fault.ShowDetail,
		RetryAfterParam,
	))

func registerPasswordLogin(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "password-login",
		Method:      http.MethodPost,
		Path:        loginPath,
		Summary:     "Log in with a password",
		Description: "Checks a username and password against this server's own account store and sets a " +
			"session credential. Every refusal reads the same: an unknown name, a wrong password and an " +
			"account that has been guessed at too often are one answer, and cost the same time to give. " +
			"An address that already has too many attempts waiting is answered 429 with Retry-After, " +
			"before any password is checked.",
		Tags:     []string{"auth"},
		Security: noSecurity,
		Errors:   []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *loginInput) (*loginOutput, error) {
		// The limit on attempts lives here and not in the credential store.
		// The store receives a username and a password, so it cannot tell the
		// account's holder from somebody guessing. The guard holds the caller.
		// It refuses one only while their address has attempts queued past the
		// ceiling, so a correct password is still admitted once its turn comes.
		address := cfg.Logins.Address(requestFrom(ctx))
		if err := cfg.Logins.Hold(ctx, address, in.Body.Username); err != nil {
			if busy, ok := errors.AsType[*loginguard.BusyError](err); ok {
				return nil, loginLimitedFault(busy)
			}

			return nil, identityFault(err)
		}

		session, err := cfg.Accounts.Login(ctx, in.Body.Username, in.Body.Password)
		if err != nil {
			// Recorded here and not in the guard, which never learns how the
			// check went. The password is never logged.
			cfg.Logger.WarnContext(ctx, "login refused",
				"account", loginguard.AccountKey(in.Body.Username), "address", address, "error", err)

			return nil, loginFault(err)
		}

		cfg.Logins.Passed(address, in.Body.Username)

		out := &loginOutput{SetCookie: sessionCookie(cfg, session)}
		out.Body.ExpiresAt = session.ExpiresAt

		return out, nil
	})
}

func registerListAccounts(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "list-accounts",
		Method:      http.MethodGet,
		Path:        accountsPath,
		Summary:     "List the local accounts",
		Description: "Returns every account this server holds a password for, without the hashes. Each row " +
			"carries the org role that account holds and every org relation behind it, so a user " +
			"management page says what each person may do without a request per person. An account with " +
			"no role can log in and do nothing. Change a role with set-account-role. It takes " +
			"configure_resource on the auth resource " +
			"kind, which is what administering this server's identity means in the graph. " +
			"Any credential that carries the grant will do, an api credential included.",
		Tags:   []string{"auth"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, func(ctx context.Context, _ *struct{}) (*listAccountsOutput, error) {
		if err := requireAccountAdmin(ctx, cfg); err != nil {
			return nil, err
		}

		items, err := cfg.Accounts.List(ctx)
		if err != nil {
			return nil, accountFault(err, "list the accounts")
		}

		held, err := orgRolesHeld(ctx, cfg)
		if err != nil {
			return nil, err
		}

		out := &listAccountsOutput{}
		out.Body.Items = make([]Account, 0, len(items))

		for _, account := range items {
			account.Subject = subjectOfAccount(account)
			account.Grants = held[account.Subject]

			if len(account.Grants) > 0 {
				account.Role = account.Grants[0]
			}

			out.Body.Items = append(out.Body.Items, account)
		}

		return out, nil
	})
}

func registerCreateAccount(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "create-account",
		Method:      http.MethodPost,
		Path:        accountsPath,
		Summary:     "Add a person",
		Description: "Creates an account under a minted id. Without a role the id holds no grant, so " +
			"the new person can log in and can do nothing until one is written for them; with a role " +
			"the role is written in the same call, so one request produces a usable person. The " +
			"answer carries the subject in tuple spelling, which is what a grant names. It takes " +
			"configure_resource on the auth resource kind, and the role is written as " +
			"set-account-role writes it, so any role that call accepts is accepted here, admin " +
			"included. " +
			"It also takes a sign-in, which is a session or a Prusa Account token that a pairing " +
			"names a person with. An api credential, and a token paired to a service account, are " +
			"refused with SESSION_REQUIRED.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict,
		},
	}, func(ctx context.Context, in *createAccountInput) (*accountOutput, error) {
		if err := requireSignedInAccountAdmin(ctx, cfg); err != nil {
			return nil, err
		}

		created, err := cfg.Accounts.Create(ctx, newAccountOf(in.Body))
		if err != nil {
			return nil, accountFault(err, "create the account")
		}

		created.Subject = subjectOfAccount(created)

		if err = grantRole(ctx, cfg, created, in.Body.Role); err != nil {
			return nil, err
		}

		return &accountOutput{Status: http.StatusCreated, Body: created}, nil
	})
}

func registerDeleteAccount(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "delete-account",
		Method:      http.MethodDelete,
		Path:        accountPath,
		Summary:     "Remove a person",
		Description: "Deletes an account and every credential it holds, so the person is logged out of " +
			"every transport at once. The last account is refused: nothing could then log in, and the " +
			"first-run path would reopen for whoever reached it. It takes configure_resource on the " +
			"auth resource kind. " +
			"It also takes a sign-in, which is a session or a Prusa Account token that a pairing " +
			"names a person with. An api credential, and a token paired to a service account, are " +
			"refused with SESSION_REQUIRED.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		},
	}, func(ctx context.Context, in *deleteAccountInput) (*deleteAccountOutput, error) {
		if err := requireSignedInAccountAdmin(ctx, cfg); err != nil {
			return nil, err
		}

		if err := cfg.Accounts.Delete(ctx, in.Account); err != nil {
			return nil, accountFault(err, "delete the account")
		}

		return &deleteAccountOutput{Status: http.StatusNoContent}, nil
	})
}

func registerSetAccountPassword(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "set-account-password",
		Method:      http.MethodPost,
		Path:        accountPasswordPath,
		Summary:     "Set an account's password",
		Description: "Changes a password and ends every session and every api credential the " +
			"account holds, on every transport. It also ends every api credential the person minted " +
			"for somebody else, every pairing to the account and every pairing the person wrote. " +
			"It ends every credential and pairing written down a chain the person started too, for " +
			"example a colleague's, made after the person paired a Prusa Account to that colleague. " +
			"A credential minted for somebody else, or a pairing written, by version 0.1.0 or earlier " +
			"records nobody as its creator, so it is not ended. " +
			"Your own account takes the current password. Another person's takes " +
			"configure_resource on the auth resource kind and no current password, which is the reset " +
			"for somebody who has lost theirs. The reset also takes a sign-in, which is a session or " +
			"a Prusa Account token that a pairing names a person with. An api credential, and a " +
			"token paired to a service account, are refused with SESSION_REQUIRED.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		},
	}, func(ctx context.Context, in *setPasswordInput) (*setPasswordOutput, error) {
		who, err := caller(ctx)
		if err != nil {
			return nil, err
		}

		if who.Type() == authz.TypeUser && who.ID() == in.Account {
			if in.Body.CurrentPassword == "" {
				return nil, httpfault.RequestInvalid("body.current_password",
					"the current password", "changing your own password takes the current one")
			}

			fresh, changeErr := cfg.Accounts.ChangePassword(
				ctx, in.Account, in.Body.CurrentPassword, in.Body.NewPassword,
			)
			if changeErr != nil {
				return nil, accountFault(changeErr, "change the password")
			}

			return &setPasswordOutput{Status: http.StatusNoContent, SetCookie: sessionCookie(cfg, fresh)}, nil
		}

		if adminErr := requireSignedInAccountAdmin(ctx, cfg); adminErr != nil {
			return nil, adminErr
		}

		if resetErr := cfg.Accounts.ResetPassword(ctx, in.Account, in.Body.NewPassword); resetErr != nil {
			return nil, accountFault(resetErr, "reset the password")
		}

		return &setPasswordOutput{Status: http.StatusNoContent}, nil
	})
}

func registerSetAccountRole(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "set-account-role",
		Method:      http.MethodPut,
		Path:        accountRolePath,
		Summary:     "Set a person's role",
		Description: "Puts an account on one of this server's three roles: viewer, operator or admin. " +
			"The role replaces whatever the account held, so one person holds one role and a page " +
			"changing a role sends one request. It takes configure_resource on the auth resource kind, " +
			"which is the grant that administers this server's identity. It and create-account are " +
			"the paths that write the admin role: the grant surface refuses that one. The last " +
			"account holding admin is refused, because a server with no administrator has nobody " +
			"who can add a person or " +
			"change what one may do. " +
			"It also takes a sign-in, which is a session or a Prusa Account token that a pairing " +
			"names a person with. An api credential, and a token paired to a service account, are " +
			"refused with SESSION_REQUIRED.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusConflict,
		},
	}, func(ctx context.Context, in *setRoleInput) (*setRoleOutput, error) {
		if err := requireSignedInAccountAdmin(ctx, cfg); err != nil {
			return nil, err
		}

		who, err := caller(ctx)
		if err != nil {
			return nil, err
		}

		// The account is read before the graph is written. A role asked for
		// under an id nobody holds is then a not-found, and no tuple is written
		// for somebody who is not on this server.
		account, err := cfg.Accounts.Get(ctx, in.Account)
		if err != nil {
			return nil, accountFault(err, "read the account")
		}

		subject, err := authz.NewSubject(authz.TypeUser, account.ID)
		if err != nil {
			return nil, httpfault.Unexpected("name the account in the authorization graph")
		}

		if err = cfg.Grants.SetOrgRole(ctx, who, subject, authz.Relation(in.Body.Role)); err != nil {
			return nil, err
		}

		return &setRoleOutput{Status: http.StatusNoContent}, nil
	})
}

// requireAccountAdmin refuses a caller who may not administer this server's
// accounts. The refusal names the object and the action, so an operator reading
// it knows which grant to write.
//
// The accounts are the auth resource kind's data, so the permission to
// configure that kind is the permission to administer who logs in. The graph
// already answers this question, so there is no second vocabulary for it.
//
// A read and a revocation take only this check, so a script holding an
// administrator's api credential can still list and revoke during an incident.
func requireAccountAdmin(ctx context.Context, cfg Config) error {
	return requireKindAdmin(ctx, cfg.Grants, ref.KindAuth, "the accounts on this server")
}

// requireSignedInAccountAdmin is requireAccountAdmin for an act that creates
// or grants access: an account, a password, a role, a pairing, a token for
// somebody else. It also takes a sign-in. A stolen administrator's api
// credential could otherwise make itself a way back in.
func requireSignedInAccountAdmin(ctx context.Context, cfg Config) error {
	if err := requireAccountAdmin(ctx, cfg); err != nil {
		return err
	}

	if !cfg.Credentials.SignedIn(requestFrom(ctx)) {
		return fault.AuthSessionRequired.New(
			"creating or granting access takes a sign-in as a person. An api credential is not one, " +
				"and neither is a Prusa Account token paired to a service account")
	}

	return nil
}

// requireKindAdmin refuses a caller who does not hold configure_resource on one
// resource kind.
//
// A surface whose data belongs to a resource kind is administered by whoever
// may configure that kind. Every such surface calls this one helper, so the
// check has one copy. what names the data in the refusal, so a caller reads
// what they were refused and not which kind holds it.
func requireKindAdmin(ctx context.Context, grants GrantStore, kind ref.Kind, what string) error {
	who, object, err := kindDecision(ctx, kind)
	if err != nil {
		return err
	}

	allowed, err := grants.Can(ctx, who, authz.ActionConfigureResource, object)

	return decided(allowed, err, object, authz.ActionConfigureResource, what)
}

// kindDecision is the caller and the object every decision on a resource kind is
// made about. It is separate from the decision so that each surface names the
// action at the call that decides it. A reader and the reachability gate in
// internal/arch cannot see an action that reaches a handler only through a
// variable.
func kindDecision(ctx context.Context, kind ref.Kind) (authz.Subject, authz.Object, error) {
	who, err := caller(ctx)
	if err != nil {
		return authz.Subject{}, authz.Object{}, err
	}

	object, err := authz.NewObject(authz.TypeResourceKind, kind.String())
	if err != nil {
		return authz.Subject{}, authz.Object{},
			httpfault.Unexpected("name the " + kind.String() + " resource kind")
	}

	return who, object, nil
}

// decided turns what the graph answered into what the caller is told. A graph
// that could not answer is this server failing and not the caller being wrong,
// and a refusal names the object and the action, so an operator reading it knows
// which grant to write.
func decided(allowed bool, err error, object authz.Object, action authz.Action, what string) error {
	if err != nil {
		return httpfault.Unexpected("ask the authorization graph who may administer " + what)
	}

	if !allowed {
		return fault.New(authz.CodeAccessDenied(),
			"this caller may not administer "+what).With(
			fault.StringParam("object", object.String()),
			fault.StringParam("action", action.String()))
	}

	return nil
}

// newAccountOf reads the body as the store takes it. The role is not on it: it
// is a grant and the account store holds no grants.
func newAccountOf(body accountBody) NewAccount {
	return NewAccount{
		Username:    body.Username,
		DisplayName: body.DisplayName,
		Password:    body.Password,
	}
}

// loginLimitedFault answers a queued address with the seconds until its turn,
// in the body and in Retry-After, rounded up so a client that waits exactly
// that long is let through.
func loginLimitedFault(busy *loginguard.BusyError) error {
	seconds := int64(math.Ceil(busy.RetryAfter.Seconds()))
	limited := loginLimited.New("login attempts from this address are queued").
		With(fault.IntParam(RetryAfterParam, seconds))

	return huma.ErrorWithHeaders(limited, http.Header{"Retry-After": []string{strconv.FormatInt(seconds, 10)}})
}

// loginFault renders a refused sign-in as a refused sign-in.
//
// Every credential check in this server answers TOKEN_INVALID, which reads as a
// broken token. On this path the caller typed a username and a password, so
// that answer sends a person who mistyped one looking for the wrong thing. A
// wrong password, a username nobody holds and a store that could not answer are
// still one answer here, because telling them apart tells a caller which
// usernames exist.
//
// A refusal carrying another code is kept as it is. The attempt limit in front
// of this check raises its own, and rewriting it would hide why a caller was
// held.
func loginFault(err error) *fault.Fault {
	var raised *fault.Fault
	if errors.As(err, &raised) && raised.Code() != fault.AuthTokenInvalid.Code() {
		return raised
	}

	return fault.AuthLoginRefused.New("the username and password name no account on this server")
}

// accountFault keeps a refusal the implementation raised and turns anything
// else into the server's own failure. An account store that could not be
// written is not the caller's mistake, and reporting it as one sends them
// looking at what they typed.
func accountFault(err error, doing string) error {
	if raised, ok := errors.AsType[*fault.Fault](err); ok {
		return raised
	}

	return httpfault.Unexpected(doing)
}

// subjectOfAccount renders an account's id the way a grant names it. An
// administrator writing a grant copies this and does not have to guess which
// of the two ids on the row the graph wants.
func subjectOfAccount(account Account) string {
	return authz.TypeUser.String() + ":" + account.ID
}

// grantRole writes the org role a create asked for, in the caller's name.
//
// It goes through SetOrgRole, the path set-account-role takes, so a create
// with a role is bounded by exactly what the caller could do in a second call.
// An administrator who could not set the role by hand cannot set it here
// either. The grant surface is the wrong path: it refuses the admin role on an
// org (see authz.adminNotConferrable), so a create asking for admin answered
// 403 after the account already existed.
//
// It is not in the account store's transaction. If the grant is refused, the
// account exists and holds nothing, which is the safe half to lose. The
// identity store and the authorization graph cannot share a transaction,
// because a deployment may put them in different databases.
func grantRole(ctx context.Context, cfg Config, account Account, role string) error {
	if role == "" {
		return nil
	}

	if cfg.Grants == nil {
		return httpfault.RequestInvalid("body.role",
			"this server serves no grant surface, so a role cannot be written with the account", role)
	}

	who, err := caller(ctx)
	if err != nil {
		return err
	}

	subject, err := authz.NewSubject(authz.TypeUser, account.ID)
	if err != nil {
		return err
	}

	return cfg.Grants.SetOrgRole(ctx, who, subject, authz.Relation(role))
}

// bootstrapOrgName is the org a role is written on. The composition root spells
// the same name; a resource instance does not federate and the graph is
// node-local, so one name serves every deployment.
const bootstrapOrgName = "local"
