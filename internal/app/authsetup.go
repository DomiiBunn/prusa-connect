package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/prusa3d/prusa-connect/internal/app/config"
	"github.com/prusa3d/prusa-connect/internal/app/resource"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
	"github.com/prusa3d/prusa-connect/internal/kernel/version"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// setupMode is the recorded first-run decision as the running process reads it.
//
// It is a cell and not a plain value. The identity resolver is wired at startup
// and the decision can be taken while the process runs, so recording open must
// take effect without a restart.
type setupMode struct {
	value atomic.Pointer[string]
}

// newSetupMode holds the decision the overlay recorded, empty for an install
// that has taken none.
func newSetupMode(mode string) *setupMode {
	held := &setupMode{}
	held.set(mode)

	return held
}

func (m *setupMode) get() string {
	held := m.value.Load()
	if held == nil {
		return ""
	}

	return *held
}

func (m *setupMode) set(mode string) { m.value.Store(&mode) }

// firstRun is the first run as the HTTP surface consumes it. It is in this
// package because the transport may not import the platform group and neither
// may name the machine-owned overlay. This package is the composition root and
// holds the three together.
type firstRun struct {
	mode *setupMode
	// code is the one-time setup code a decision has to carry.
	code *setupGate
	// recording lets one decision through at a time, so a second caller with
	// the same code finds the first run closed.
	recording sync.Mutex
	store     *resource.Store
	accounts  httpapi.Accounts
	logger    *slog.Logger
	// version is the build the first-run page names.
	version string
	// issuer says an OIDC issuer is configured, so the page offers no password.
	issuer bool
	// local says the operator's own file already turned the password login on.
	local bool
	// principal is the subject a write from the first run is made as, in graph
	// spelling. It is the subject the authorization graph's first grant names,
	// so switching a module on here goes through the same rules an
	// administrator's own call does.
	principal string
	// chosenPrincipal says the operator named a principal of their own, which
	// is somebody choosing who an unauthenticated request is served as.
	chosenPrincipal bool
	// configPath is the operator's file, beside which the wizard writes its
	// own.
	configPath string
	// restart stops this process so it starts again on the file the wizard
	// wrote. Nil in a process that cannot restart itself.
	restart func()
	// importFirst writes the first administrator from a hash the wizard made
	// before the restart. Nil on a deployment that keeps no accounts.
	importFirst func(ctx context.Context, username, hash string) error
	// applied says this start recorded the decision a confirmed wizard left,
	// whose file goes once every port is open.
	applied bool
	// apiFlag is the -addr the process was started with, which wins over the
	// wizard's own answer for the port.
	apiFlag string
	// discover reads an issuer's discovery document, so an issuer the next
	// start could not reach is refused while the person is still on the page.
	discover func(ctx context.Context, issuer string) error
	// listening is the address the API listens on.
	listening string
}

var _ httpapi.Setup = (*firstRun)(nil)

// newFirstRun builds the first run over the configuration and the stores this
// process runs with.
func newFirstRun(
	core config.Core, store *resource.Store, mode *setupMode, code *setupGate,
	accounts httpapi.Accounts, version string, logger *slog.Logger,
) *firstRun {
	first := &firstRun{
		mode:            mode,
		code:            code,
		store:           store,
		accounts:        accounts,
		logger:          logger,
		version:         version,
		issuer:          core.Auth.Enabled(),
		local:           core.Auth.Local,
		principal:       subjectOf(principalID(core)),
		chosenPrincipal: core.Principal != config.DefaultPrincipal,
		discover:        discoverIssuer,
	}

	if importer, ok := accounts.(firstImporter); ok {
		first.importFirst = importer.importFirst
	}

	return first
}

// firstImporter is the account store that can write the first administrator
// from a hash.
type firstImporter interface {
	importFirst(ctx context.Context, username, hash string) error
}

// Pending reports whether this install has recorded no decision.
func (f *firstRun) Pending() bool { return f.mode.get() == "" }

// State is what the first-run page reads.
//
// The mode and the admission choice are read from the disk and not from this
// process's memory. A settled server reports what its first run decided, maybe
// several restarts ago, and the file holds that decision.
func (f *firstRun) State(ctx context.Context) (httpapi.SetupState, error) {
	mode, err := f.store.SetupMode()
	if err != nil {
		return httpapi.SetupState{}, fmt.Errorf("read the recorded first-run decision: %w", err)
	}

	admission, err := f.store.AdmissionMode()
	if err != nil {
		return httpapi.SetupState{}, fmt.Errorf("read the recorded admission choice: %w", err)
	}

	modules := f.modules()

	state := httpapi.SetupState{
		State:         httpapi.SetupPending,
		Version:       f.version,
		Documentation: version.Documentation(),
		Issuer:        f.issuer,
		Admission:     admission,
		Modules:       modules,
		Steps:         f.steps(ctx, mode, admission, modules),
	}

	if mode != "" {
		state.State = httpapi.SetupDone
		state.Mode = mode
	}

	// A failure is reported whether or not the decision was recorded: a start
	// that recorded it and then could not open a port goes back to the
	// configuration it had, and the page must not call that a success.
	if f.configPath != "" {
		state.LastError = lastFailure(f.configPath)
	}

	return state, nil
}

// steps is what the first run has done, read from what is on disk: the
// recorded mode, whether this server holds an account, the recorded admission
// choice, and each declared module.
//
// An account store that will not answer is reported as a step that is not
// done, with the reason in it. The page uses this list to resume where a
// person stopped, and a failed read would leave them with no page.
func (f *firstRun) steps(
	ctx context.Context, mode, admission string, modules []httpapi.SetupModule,
) []httpapi.SetupStep {
	steps := []httpapi.SetupStep{{
		Step: httpapi.SetupStepMode, Done: mode != "", Detail: httpapi.SetupModeDetail(mode),
	}, f.administratorStep(ctx), {
		Step:   httpapi.SetupStepAdmission,
		Done:   admission != "",
		Detail: httpapi.SetupAdmissionDetail(admission),
	}}

	for _, module := range modules {
		steps = append(steps, httpapi.SetupStep{
			Step:   httpapi.SetupStepModulePrefix + module.Instance,
			Done:   module.Enabled,
			Detail: moduleStepDetail(module),
		})
	}

	return steps
}

// administratorStep reports whether somebody holds an account on this server.
func (f *firstRun) administratorStep(ctx context.Context) httpapi.SetupStep {
	step := httpapi.SetupStep{Step: httpapi.SetupStepAdministrator}

	info, err := f.accounts.Info(ctx)
	if err != nil {
		step.Detail = "This server could not say whether it holds an account."

		return step
	}

	switch {
	case f.issuer:
		step.Done = true
		step.Detail = "People sign in through the configured identity provider."
	case info.Credentials && !info.NeedsSetup:
		step.Done = true
		step.Detail = "This server holds an account to sign in with."
	default:
		step.Detail = "No account has been created yet."
	}

	return step
}

// moduleStepDetail says what the first run left one module at.
func moduleStepDetail(module httpapi.SetupModule) string {
	if module.Enabled {
		return module.Instance + " is switched on."
	}

	return module.Instance + " is switched off."
}

// modules lists the declared modules and whether each is enabled. It reads the
// reconciled document and not the disk. The startup reload has already run
// when anything can call this, and the page must list what this process
// supervises.
func (f *firstRun) modules() []httpapi.SetupModule {
	document := f.store.Document()
	out := make([]httpapi.SetupModule, 0, len(document.Instances))

	for _, declared := range document.Instances {
		// A module the operator's key keeps off is not offered: whatever the
		// page chose, it would not run until modules are on again.
		if _, isModule := declared.ModuleNamespace(); !isModule || f.store.Held(declared) {
			continue
		}

		out = append(out, httpapi.SetupModule{
			Instance: declared.Ref.String(),
			Type:     declared.Spec.Type,
			Enabled:  declared.Spec.Enabled,
			Writable: declared.Source == resource.SourceOverlay,
		})
	}

	return out
}

// Record takes the decision.
//
// The order matters. The modules are switched first, because an operator can
// switch one back with a call. Then the administrator is created and signed
// in, then the admission choice is written, and the mode is written last. A
// failure anywhere leaves the install pending, so the page asks again and the
// server does not run with an identity mode nobody chose.
func (f *firstRun) Record(
	ctx context.Context, decision httpapi.SetupDecision,
) (httpapi.SetupResult, error) {
	if !f.Pending() {
		return httpapi.SetupResult{}, fault.AuthSetupClosed.New(
			"this server has already recorded what it does about identity")
	}

	if err := f.code.admit(ctx, decision.Client, decision.Code); err != nil {
		return httpapi.SetupResult{}, err
	}

	f.recording.Lock()
	defer f.recording.Unlock()

	if !f.Pending() {
		return httpapi.SetupResult{}, fault.AuthSetupClosed.New(
			"this server has already recorded what it does about identity")
	}

	if decision.Mode != resource.SetupOpen && decision.Mode != resource.SetupRequired {
		return httpapi.SetupResult{}, httpfault.RequestInvalid("body.mode",
			resource.SetupOpen+" or "+resource.SetupRequired,
			"a deployment either asks every caller for a credential or serves every request as core.principal")
	}

	// The operator's own file turned the password login on, so a deployment
	// that asks for no credential cannot be what this server serves.
	if f.local && decision.Mode == resource.SetupOpen {
		return httpapi.SetupResult{}, httpfault.RequestInvalid("body.mode",
			resource.SetupRequired,
			"core.auth.local is set, so every caller presents a credential")
	}

	admission := resource.EffectiveAdmission(decision.Admission)

	if admission != resource.AdmissionAsk && admission != resource.AdmissionOpen {
		return httpapi.SetupResult{}, httpfault.RequestInvalid("body.admission",
			resource.AdmissionAsk+" or "+resource.AdmissionOpen,
			"a printer either waits for an operator to approve its code or is let in when it dials")
	}

	if decision.Configuration != nil {
		return f.recordForRestart(ctx, decision, admission)
	}

	if err := f.switchModules(ctx, decision.Modules); err != nil {
		return httpapi.SetupResult{}, err
	}

	result := httpapi.SetupResult{Mode: decision.Mode, Admission: admission}

	if decision.Mode == resource.SetupRequired {
		session, err := f.createAdministrator(ctx, decision.Account)
		if err != nil {
			return httpapi.SetupResult{}, err
		}

		result.Session, result.SignedIn = session, true
	}

	if err := f.store.RecordAdmission(admission); err != nil {
		return httpapi.SetupResult{}, httpfault.Unexpected("record how a new printer joins this server").
			Wrap(fmt.Errorf("admission %q: %w", admission, err))
	}

	if err := f.store.RecordSetupMode(decision.Mode); err != nil {
		if errors.Is(err, resource.ErrSetupRecorded) {
			return httpapi.SetupResult{}, fault.AuthSetupClosed.New(
				"this server has already recorded what it does about identity")
		}

		return httpapi.SetupResult{}, httpfault.Unexpected("record what this server does about identity").
			Wrap(fmt.Errorf("mode %q: %w", decision.Mode, err))
	}

	f.mode.set(decision.Mode)
	f.code.consume()
	f.logger.InfoContext(ctx, "the first run recorded what this server does about identity",
		"mode", decision.Mode, "admission", admission)

	// The steps this call took, built from the same reads a GET makes, so the
	// page that recorded the decision and the page that reloads afterwards see
	// the same list.
	result.Steps = f.steps(ctx, decision.Mode, admission, f.modules())

	return result, nil
}

// FirstAccount runs create behind the setup code while this server's own
// account store holds no account. It is POST /api/v1/auth/setup, which a
// password login with no administrator still answers after its decision is
// recorded. Once an account exists the code is ended, and create runs so the
// account store gives its own refusal.
func (f *firstRun) FirstAccount(
	ctx context.Context, client httpapi.SetupClient, code string, create func(context.Context) error,
) error {
	needed, err := f.needsAccount(ctx)
	if err != nil {
		return err
	}

	if !needed {
		f.code.consume()

		return create(ctx)
	}

	if err = f.code.admit(ctx, client, code); err != nil {
		return err
	}

	f.recording.Lock()
	defer f.recording.Unlock()

	if err = create(ctx); err != nil {
		return err
	}

	f.code.consume()
	f.logger.InfoContext(ctx, "the first administrator was created with the setup code")

	return nil
}

// needsAccount reports whether this server holds its own accounts and none
// exists yet, which is the state POST /api/v1/auth/setup answers in.
func (f *firstRun) needsAccount(ctx context.Context) (bool, error) {
	info, err := f.accounts.Info(ctx)
	if err != nil {
		return false, fmt.Errorf("read whether this server holds accounts: %w", err)
	}

	return info.Credentials && info.NeedsSetup, nil
}

// createAdministrator creates the first administrator and signs them in, so the
// page that recorded the decision continues as them.
func (f *firstRun) createAdministrator(
	ctx context.Context, account *httpapi.NewAccount,
) (httpapi.Session, error) {
	if account == nil {
		return httpapi.Session{}, httpfault.RequestInvalid("body.account",
			"a username and a password",
			"a deployment that requires a credential needs somebody to hold one")
	}

	if _, err := f.accounts.Setup(ctx, *account); err != nil {
		return httpapi.Session{}, err
	}

	session, err := f.accounts.Login(ctx, account.Username, account.Password)
	if err != nil {
		return httpapi.Session{}, err
	}

	return session, nil
}

// switchModules flips enabled on the instances the decision names, through the
// same store and the same rules an administrator's own call goes through. An
// instance the operator declared in their own file is refused there. The
// state's writable member tells the page that before it asks.
func (f *firstRun) switchModules(ctx context.Context, choices []httpapi.SetupModuleChoice) error {
	for _, choice := range choices {
		instance, err := ref.Parse(choice.Instance)
		if err != nil {
			return httpfault.RequestInvalid("body.modules.instance",
				"an instance in kind:name spelling", choice.Instance)
		}

		// The page never offered a module the key keeps off, so a choice
		// naming one is a decision taken before the operator switched modules
		// off. Switching it on is skipped and not refused: a refusal here
		// would stop the whole decision from being applied at the next start.
		// Switching it off is applied, so the module is still off when modules
		// are on again.
		declared, found := f.store.Document().Find(instance)
		if choice.Enabled && found && f.store.Held(declared) {
			why := "the first run left an installed module off, because modules are switched off"
			if f.store.SafeMode() {
				why = "the first run left a module off, because the server runs in safe mode"
			}

			f.logger.WarnContext(ctx, why, "resource", instance.String())

			continue
		}

		// Read before each write. A write rewrites the overlay whole, so the
		// token of every instance moves with it and a token read once would be
		// stale by the second module.
		current, err := f.store.Get(ctx, f.principal, instance)
		if err != nil {
			return fmt.Errorf("read %s: %w", instance, err)
		}

		if current.Enabled == choice.Enabled {
			continue
		}

		desired := current.ResourceSpec
		desired.Enabled = choice.Enabled

		if _, err = f.store.Apply(ctx, f.principal, instance, desired,
			httpapi.Precondition{IfMatch: []string{current.Version}}); err != nil {
			return fmt.Errorf("switch %s: %w", instance, err)
		}
	}

	return nil
}

// settle records the decision an install that predates this key already took,
// so that install keeps working as it did.
//
// Three facts say a deployment has decided. An issuer means every caller
// already presents a credential. An account means somebody already created an
// administrator. A principal the operator named themselves, on an install with
// no password login, means they chose who an unauthenticated request is served
// as, and that is the open deployment. An install with none of the three has
// decided nothing and stays pending. That includes a password login with no
// account yet: its first run still has to create the administrator.
func (f *firstRun) settle(ctx context.Context) error {
	if !f.Pending() {
		return nil
	}

	mode, reason, decided, err := f.inferred(ctx)
	if err != nil || !decided {
		return err
	}

	if err = f.store.RecordSetupMode(mode); err != nil {
		return fmt.Errorf("record the first-run decision this install already took: %w", err)
	}

	f.mode.set(mode)
	f.logger.InfoContext(ctx, "this install predates the first-run decision and had already taken one",
		"mode", mode, "reason", reason)

	return nil
}

// inferred is the decision an existing install already took, and whether it took
// one at all.
func (f *firstRun) inferred(ctx context.Context) (mode, reason string, decided bool, err error) {
	if f.issuer {
		return resource.SetupRequired, "core.auth.issuer is configured", true, nil
	}

	info, err := f.accounts.Info(ctx)
	if err != nil {
		return "", "", false, fmt.Errorf("read whether this server holds accounts: %w", err)
	}

	if info.Credentials && !info.NeedsSetup {
		return resource.SetupRequired, "this server already holds accounts", true, nil
	}

	// A password login with no account yet stays pending, so the first run
	// creates the administrator behind the setup code. Recording required here
	// would leave POST /api/v1/auth/setup as the only way in.
	if f.local {
		return "", "", false, nil
	}

	if f.chosenPrincipal {
		return resource.SetupOpen, "core.principal names a subject the operator chose", true, nil
	}

	return "", "", false, nil
}

// modePrincipals resolves a caller by the decision the first run recorded.
//
// It is wired on an install that has recorded none, and on that install alone.
// Every other deployment takes the resolver its configuration names, so the
// wiring a running server has does not depend on a file it may rewrite. Until a
// decision is there it resolves through the credential store, which refuses
// everybody. The setup gate has already refused the request, and the resolver
// must not admit what the gate refused.
type modePrincipals struct {
	mode     *setupMode
	open     httpapi.Principals
	required httpapi.Principals
}

var _ httpapi.Principals = modePrincipals{}

// Principal resolves the caller through whichever resolver the decision names.
func (m modePrincipals) Principal(r *http.Request) (string, error) {
	if m.mode.get() == resource.SetupOpen {
		return m.open.Principal(r)
	}

	return m.required.Principal(r)
}

// Caller resolves the caller and its scopes through whichever resolver the
// decision names.
func (m modePrincipals) Caller(r *http.Request) (httpapi.Caller, error) {
	if m.mode.get() == resource.SetupOpen {
		return m.open.Caller(r)
	}

	return m.required.Caller(r)
}
