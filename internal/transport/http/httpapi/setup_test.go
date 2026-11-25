package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// fakeSetup is the first run with the overlay and the account store taken out.
// It records what it was asked to record, so a test holds this surface to
// calling the composition root rather than to deciding by itself.
type fakeSetup struct {
	mu sync.Mutex

	pending  bool
	state    SetupState
	result   SetupResult
	err      error
	recorded []SetupDecision
	// firstAccount is every call to FirstAccount, with the code and the client.
	firstAccount []SetupDecision
	// contract is what Configuration answers, and codes every code it was sent.
	contract SetupConfiguration
	codes    []string
}

var _ Setup = (*fakeSetup)(nil)

func (s *fakeSetup) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.pending
}

func (s *fakeSetup) State(context.Context) (SetupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state, nil
}

func (s *fakeSetup) Record(_ context.Context, decision SetupDecision) (SetupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return SetupResult{}, s.err
	}

	s.recorded = append(s.recorded, decision)
	s.pending = false

	return s.result, nil
}

// Configuration refuses with err when one is set, and otherwise records the
// code and answers the contract.
func (s *fakeSetup) Configuration(_ context.Context, _ SetupClient, code string) (SetupConfiguration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.codes = append(s.codes, code)

	if s.err != nil {
		return SetupConfiguration{}, s.err
	}

	return s.contract, nil
}

// CreateCertificates answers the files under /certs, or refuses with err.
func (s *fakeSetup) CreateCertificates(
	_ context.Context,
	_ SetupClient,
	code string,
	names []string,
) (SetupCertificates, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.codes = append(s.codes, code)

	if s.err != nil {
		return SetupCertificates{}, s.err
	}

	return SetupCertificates{Folder: "/certs", Names: names}, nil
}

// CheckOIDC answers every step as passed, or refuses with err.
func (s *fakeSetup) CheckOIDC(
	_ context.Context,
	_ SetupClient,
	code string,
	probe SetupOIDCProbe,
) (SetupOIDCCheck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.codes = append(s.codes, code)

	if s.err != nil {
		return SetupOIDCCheck{}, s.err
	}

	passed := SetupOIDCStep{Status: "passed"}

	return SetupOIDCCheck{Issuer: probe.Issuer, Discovery: passed, Client: passed, Redirect: passed}, nil
}

// CheckStorage answers the folder as writable, or refuses with err.
func (s *fakeSetup) CheckStorage(_ context.Context, _ SetupClient, code, root string) (SetupStorageCheck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.codes = append(s.codes, code)

	if s.err != nil {
		return SetupStorageCheck{}, s.err
	}

	return SetupStorageCheck{Root: root, Writable: true}, nil
}

// FirstAccount refuses with err when one is set, and otherwise records the code
// and creates the account.
func (s *fakeSetup) FirstAccount(
	ctx context.Context, client SetupClient, code string, create func(context.Context) error,
) error {
	s.mu.Lock()
	refusal := s.err
	s.firstAccount = append(s.firstAccount, SetupDecision{Code: code, Client: client})
	s.mu.Unlock()

	if refusal != nil {
		return refusal
	}

	return create(ctx)
}

// setupServer is a server whose first run is the caller's.
func setupServer(t *testing.T, setup Setup) *Server {
	t.Helper()

	server, err := New(Config{
		Version:    "0.1.0",
		Principals: fixedPrincipal("user:operator"),
		Resources:  seededStore(),
		Presets:    seededPresets(),
		Endpoints:  seededEndpoints(),
		Grants:     &fakeGrantStore{},
		Sessions:   newFakeSessions(),
		Accounts:   &fakeAccounts{},
		Setup:      setup,
		Readiness:  &fakeReadiness{report: readyReport()},
		Devices:    noPrinters{},
		Stores:     noStorages{},
		Events:     noEvents{},
	})
	require.NoError(t, err)

	return server
}

// TestAPendingInstallServesTheFirstRunAndNothingElse is the old backlog's first
// part on the wire. A server that has recorded nothing about identity cannot
// say whether a caller needs a credential, so it answers every other path with
// the code that says what to do about it.
func TestAPendingInstallServesTheFirstRunAndNothingElse(t *testing.T) {
	t.Parallel()

	handler := setupServer(t, &fakeSetup{
		pending: true,
		state:   SetupState{State: SetupPending, Version: "0.1.0", Modules: []SetupModule{}},
	}).Handler()

	rec := call(t, handler, http.MethodGet, "/api/v1/devices", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body: %s", rec.Body.String())
	requireProblemCode(t, rec.Body.Bytes(), fault.AuthSetupPending.Code())

	// The probes and the first run itself still answer, because an orchestrator
	// sends the first two whatever the process is doing and nobody can record
	// the decision without the third.
	for _, path := range []string{healthPath, readinessPath, firstRunPath} {
		require.Equal(t, http.StatusOK, call(t, handler, http.MethodGet, path, "").Code,
			"%s is refused on a pending install", path)
	}
}

// TestTheFirstRunStateIsWhatThePageReads drives the sample the document
// publishes for get-setup, which the round trip gate leaves to this test
// because a settled fake cannot produce the pending answer.
func TestTheFirstRunStateIsWhatThePageReads(t *testing.T) {
	t.Parallel()

	published, ok := setupExamples()["get-setup"]
	require.True(t, ok, "get-setup publishes no sample")

	state, ok := published.Response.(SetupState)
	require.True(t, ok, "the get-setup sample is not a SetupState")

	handler := setupServer(t, &fakeSetup{pending: true, state: state}).Handler()

	rec := call(t, handler, http.MethodGet, firstRunPath, "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	wanted, err := json.Marshal(state)
	require.NoError(t, err)
	require.JSONEq(t, string(wanted), rec.Body.String())
}

// TestRecordingRequiredSignsTheAdministratorIn is the second half of the first
// run: the page that created the administrator continues as them rather than
// sending them to a login form they have no reason to see. It drives the sample
// the document publishes for record-setup.
func TestRecordingRequiredSignsTheAdministratorIn(t *testing.T) {
	t.Parallel()

	published, ok := setupExamples()["record-setup"]
	require.True(t, ok, "record-setup publishes no sample")

	answered, ok := published.Response.(map[string]any)
	require.True(t, ok, "the record-setup sample is not an object")

	steps, ok := answered["steps"].([]SetupStep)
	require.True(t, ok, "the record-setup sample carries no steps")

	setup := &fakeSetup{
		pending: true,
		result: SetupResult{
			Mode:      SetupModeRequired,
			Admission: SetupAdmissionAsk,
			SignedIn:  true,
			Steps:     steps,
			Session: Session{
				Token:     "pcl_sessiontoken",
				ExpiresAt: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
		},
	}

	sent, err := json.Marshal(published.Request)
	require.NoError(t, err)

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath, string(sent))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	wanted, err := json.Marshal(published.Response)
	require.NoError(t, err)
	require.JSONEq(t, string(wanted), rec.Body.String())

	require.Equal(t, "pcl_sessiontoken", sessionCookieOf(t, rec.Result()),
		"the answer carries no session, so the page would have to log in again")

	require.Len(t, setup.recorded, 1)
	require.Equal(t, SetupModeRequired, setup.recorded[0].Mode)
	require.NotNil(t, setup.recorded[0].Account)
	require.Equal(t, "owner@example.com", setup.recorded[0].Account.Username)
	require.Equal(t, []SetupModuleChoice{{Instance: "sink:planner", Enabled: true}},
		setup.recorded[0].Modules)
	require.Equal(t, SetupAdmissionAsk, setup.recorded[0].Admission)
}

// TestAnAbsentAdmissionKeyAsksFirst is the careful default. A body that says
// nothing about how a printer joins must not be the one that lets any machine
// on the wire in.
func TestAnAbsentAdmissionKeyAsksFirst(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true, result: SetupResult{
		Mode: SetupModeOpen, Admission: SetupAdmissionAsk,
	}}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath, `{"mode":"open"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	require.Len(t, setup.recorded, 1)
	require.Equal(t, SetupAdmissionAsk, setup.recorded[0].Admission,
		"an absent admission key was not read as ask")

	var answer struct {
		Admission string      `json:"admission"`
		Steps     []SetupStep `json:"steps"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
	require.Equal(t, SetupAdmissionAsk, answer.Admission)
	require.NotNil(t, answer.Steps, "steps is absent, so a page would have to handle null")
}

// TestTheAdmissionChoiceReachesTheCompositionRoot is the other value, and the
// whole point of the field: the page draws "let every printer in" and the
// choice has somewhere to go.
func TestTheAdmissionChoiceReachesTheCompositionRoot(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true, result: SetupResult{
		Mode: SetupModeOpen, Admission: SetupAdmissionOpen,
	}}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"mode":"open","admission":"open"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	require.Len(t, setup.recorded, 1)
	require.Equal(t, SetupAdmissionOpen, setup.recorded[0].Admission)
}

// TestAnAdmissionWordNobodyKnowsIsRefused keeps the enum closed. A third answer
// to a question with two would be recorded and then read back by nothing.
func TestAnAdmissionWordNobodyKnowsIsRefused(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"mode":"open","admission":"sometimes"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	require.Empty(t, setup.recorded, "a refused body was recorded anyway")
}

// TestTheFirstRunReportsTheStepsItHasTaken is the old backlog on the wire. A page
// that resumes halfway reads a key per step and the sentence beside it, rather
// than inferring what is done from three other fields.
func TestTheFirstRunReportsTheStepsItHasTaken(t *testing.T) {
	t.Parallel()

	state := SetupState{
		State: SetupDone, Mode: SetupModeRequired, Version: "0.1.0",
		Admission: SetupAdmissionOpen,
		Modules:   []SetupModule{},
		Steps: []SetupStep{
			{Step: SetupStepMode, Done: true, Detail: SetupModeDetail(SetupModeRequired)},
			{Step: SetupStepAdministrator, Done: true, Detail: "This server holds an account to sign in with."},
			{Step: SetupStepAdmission, Done: true, Detail: SetupAdmissionDetail(SetupAdmissionOpen)},
			{Step: SetupStepModulePrefix + "sink:planner", Done: false, Detail: "sink:planner is switched off."},
		},
	}

	rec := call(t, setupServer(t, &fakeSetup{state: state}).Handler(),
		http.MethodGet, firstRunPath, "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var answer SetupState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
	require.Equal(t, state.Steps, answer.Steps)
	require.Equal(t, SetupAdmissionOpen, answer.Admission)
}

// TestASettledServerReportsTheModeItWasWiredWith holds the wiring a test and an
// embedded build take. It reads no overlay and no account store, so the one
// step it can answer for is the mode, and it says so rather than reporting an
// empty list a page would read as nothing decided.
func TestASettledServerReportsTheModeItWasWiredWith(t *testing.T) {
	t.Parallel()

	rec := call(t, setupServer(t, nil).Handler(), http.MethodGet, firstRunPath, "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var answer SetupState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
	require.Equal(t, []SetupStep{{
		Step: SetupStepMode, Done: true, Detail: SetupModeDetail(answer.Mode),
	}}, answer.Steps)
}

// TestASecondDecisionIsRefused keeps the first run closed. Reopening it over
// the API would let whoever reaches the port choose again.
func TestASecondDecisionIsRefused(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{err: fault.AuthSetupClosed.New("already recorded")}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"mode":"open"}`)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	requireProblemCode(t, rec.Body.Bytes(), fault.AuthSetupClosed.Code())
}

// TestRecordingOpenTakesNoAccount holds the pairing that cannot mean anything.
// An open deployment asks for no credential, so a password sent here is one
// nothing would ever check.
func TestRecordingOpenTakesNoAccount(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true, result: SetupResult{Mode: SetupModeOpen}}
	handler := setupServer(t, setup).Handler()

	rec := call(t, handler, http.MethodPost, firstRunPath,
		`{"mode":"open","account":{"username":"owner@example.com","password":"correct horse battery"}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	require.Empty(t, setup.recorded, "a refused body was recorded anyway")

	rec = call(t, handler, http.MethodPost, firstRunPath, `{"mode":"open"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Empty(t, rec.Result().Cookies(), "an open deployment minted a session")

	// And the rest of the surface answers again, because the decision is there.
	require.Equal(t, http.StatusOK, call(t, handler, http.MethodGet, "/api/v1/devices", "").Code)
}

// TestRequiredTakesAnAccount is the other half of the same pairing. A
// deployment that asks every caller for a credential needs somebody to hold
// one, and recording the mode without creating them would lock the server.
func TestRequiredTakesAnAccount(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"mode":"required"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	require.Empty(t, setup.recorded)
}

// TestRequiredWithAnIssuerTakesNoAccount is the body the wizard sends for "Sign
// in with your own provider". People sign in at the issuer and core.principal
// names the administrator by their subject there, so nobody needs a password.
func TestRequiredWithAnIssuerTakesNoAccount(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true, result: SetupResult{Mode: SetupModeRequired, Restarting: true}}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"code":"K7QD-3M9X-TP2A","mode":"required","admission":"ask","configuration":{`+
			`"core.auth.issuer":"https://auth.example.com/application/o/prusa-connect/",`+
			`"core.auth.client_id":"prusa-connect","core.auth.client_secret":"s",`+
			`"core.principal":"admin-sub"}}`)
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, setup.recorded, 1)
	require.Nil(t, setup.recorded[0].Account)

	// An issuer left empty names nobody, so the account is required again.
	rec = call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"mode":"required","configuration":{"core.auth.issuer":" "}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, setup.recorded, 1, "a refused body was recorded anyway")
}

// TestAServerWiredWithoutAFirstRunIsSettled is what keeps every other wiring
// working. A deployment whose decision is already in its configuration must not
// have to record a second one before it answers anything.
func TestAServerWiredWithoutAFirstRunIsSettled(t *testing.T) {
	t.Parallel()

	handler := setupServer(t, nil).Handler()

	require.Equal(t, http.StatusOK, call(t, handler, http.MethodGet, "/api/v1/devices", "").Code)

	rec := call(t, handler, http.MethodGet, firstRunPath, "")
	require.Equal(t, http.StatusOK, rec.Code)

	var state SetupState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &state))
	require.Equal(t, SetupDone, state.State)

	rec = call(t, handler, http.MethodPost, firstRunPath, `{"mode":"open"}`)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
}

// requireProblemCode reads the registered code out of a problem document.
func requireProblemCode(t *testing.T, body []byte, want fault.Code) {
	t.Helper()

	var problem struct {
		Code string `json:"code"`
	}

	require.NoError(t, json.Unmarshal(body, &problem))
	require.Equal(t, want.String(), problem.Code)
}

// sessionCookieOf returns the session credential the answer set, empty when it
// set none.
func sessionCookieOf(t *testing.T, response *http.Response) string {
	t.Helper()

	for _, cookie := range response.Cookies() {
		if cookie.Name == testCookieName {
			return cookie.Value
		}
	}

	return ""
}

// TestTheSetupCodeAndTheClientReachTheCompositionRoot holds this surface to
// passing the code on as it was typed, with the address it came from. The
// composition root compares the code and counts wrong ones per address, so a
// surface that dropped either would let every code through or count nobody.
func TestTheSetupCodeAndTheClientReachTheCompositionRoot(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true, result: SetupResult{Mode: SetupModeOpen}}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"mode":"open","code":"k7qd 3m9x tp2a"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, setup.recorded, 1)
	require.Equal(t, "k7qd 3m9x tp2a", setup.recorded[0].Code)
	require.NotEmpty(t, setup.recorded[0].Client.Address)
	require.NotContains(t, rec.Body.String(), "k7qd", "the answer repeated the setup code")
}

// TestALimitedFirstRunSaysWhenToTryAgain is the refusal a client reads without
// parsing the body: the wait the composition root put in the fault arrives as
// Retry-After.
func TestALimitedFirstRunSaysWhenToTryAgain(t *testing.T) {
	t.Parallel()

	limited := fault.AuthSetupCodeLimited.New("too many").
		With(fault.IntParam(RetryAfterParam, 600))

	rec := call(t, setupServer(t, &fakeSetup{pending: true, err: limited}).Handler(),
		http.MethodPost, firstRunPath, `{"mode":"open","code":"K7QD-3M9X-TP2A"}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "body: %s", rec.Body.String())
	requireProblemCode(t, rec.Body.Bytes(), fault.AuthSetupCodeLimited.Code())
	require.Equal(t, "600", rec.Header().Get("Retry-After"))
}

// TestAWrongSetupCodeIsRefusedWithoutAWait is the other side: a wrong code is
// a 403 with its own code, and nothing tells the client to wait.
func TestAWrongSetupCodeIsRefusedWithoutAWait(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true, err: fault.AuthSetupCodeRefused.New("wrong")}

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath,
		`{"mode":"open","code":"nope"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
	requireProblemCode(t, rec.Body.Bytes(), fault.AuthSetupCodeRefused.Code())
	require.Empty(t, rec.Header().Get("Retry-After"))
}

// TestTheAccountPathAsksTheFirstRunForTheCode holds POST /api/v1/auth/setup to
// the same gate as the first run: the code and the client reach the
// composition root, and its refusal is the answer (ADR-20260929-a4j rule 17).
func TestTheAccountPathAsksTheFirstRunForTheCode(t *testing.T) {
	t.Parallel()

	account := `"username":"owner@example.com","password":"correct horse battery"`

	refusing := &fakeSetup{err: fault.AuthSetupCodeRefused.New("wrong")}
	rec := call(t, setupServer(t, refusing).Handler(), http.MethodPost, setupPath, `{`+account+`,"code":"nope"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
	requireProblemCode(t, rec.Body.Bytes(), fault.AuthSetupCodeRefused.Code())

	admitting := &fakeSetup{}
	rec = call(t, setupServer(t, admitting).Handler(), http.MethodPost, setupPath,
		`{`+account+`,"code":"K7QD-3M9X-TP2A"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, admitting.firstAccount, 1)
	require.Equal(t, "K7QD-3M9X-TP2A", admitting.firstAccount[0].Code)
	require.NotEmpty(t, admitting.firstAccount[0].Client.Address)
}

// TestAForwardedRequestFromLoopbackIsMarked holds the one fact the first run
// cannot learn for itself: a request from loopback that carries a forwarding
// header came through a proxy on this machine that core.trusted_proxies does
// not list.
func TestAForwardedRequestFromLoopbackIsMarked(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		remote    string
		header    string
		forwarded bool
	}{
		{name: "loopback with X-Forwarded-For", remote: "127.0.0.1:5000", header: "X-Forwarded-For", forwarded: true},
		{name: "loopback with Forwarded", remote: "[::1]:5000", header: "Forwarded", forwarded: true},
		{name: "loopback with no header", remote: "127.0.0.1:5000"},
		{name: "a LAN client with X-Forwarded-For", remote: "192.168.1.20:5000", header: "X-Forwarded-For"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			setup := &fakeSetup{pending: true, result: SetupResult{Mode: SetupModeOpen}}
			handler := setupServer(t, setup).Handler()

			request := httptest.NewRequest(http.MethodPost, firstRunPath,
				strings.NewReader(`{"mode":"open","code":"K7QD-3M9X-TP2A"}`))
			request.Header.Set("Content-Type", "application/json")
			request.RemoteAddr = tc.remote

			if tc.header != "" {
				request.Header.Set(tc.header, "for=203.0.113.9")
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, request)
			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			require.Len(t, setup.recorded, 1)
			require.Equal(t, tc.forwarded, setup.recorded[0].Client.Forwarded)
		})
	}
}

// TestTheWizardReadsTheConfigurationBehindTheCode holds get-setup-configuration
// to its published sample, and to handing the code in X-Setup-Code to the
// composition root rather than checking it here (ADR-20261015-ejz rule 9).
func TestTheWizardReadsTheConfigurationBehindTheCode(t *testing.T) {
	t.Parallel()

	published, ok := setupExamples()["get-setup-configuration"]
	require.True(t, ok)

	setup := &fakeSetup{pending: true, contract: published.Response.(SetupConfiguration)}
	handler := setupServer(t, setup).Handler()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, setupConfigurationPath, nil)
	req.Header.Set("X-Setup-Code", "K7QD-3M9X-TP2A")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	wanted, err := json.Marshal(published.Response)
	require.NoError(t, err)
	require.JSONEq(t, string(wanted), rec.Body.String())
	require.Equal(t, []string{"K7QD-3M9X-TP2A"}, setup.codes)

	setup.err = fault.AuthSetupCodeRefused.New("wrong code")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAConfirmedWizardAnswers202 holds the restart answer: the decision is
// written down, not recorded, so the state is restarting and no session is
// set (ADR-20261015-ejz rule 20).
func TestAConfirmedWizardAnswers202(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true, result: SetupResult{
		Mode: SetupModeRequired, Admission: SetupAdmissionAsk, Restarting: true,
	}}

	body := `{"code":"K7QD-3M9X-TP2A","mode":"required",` +
		`"account":{"username":"owner","password":"a long password"},` +
		`"configuration":{"core.site":"workshop"},"storage":{"root":"/srv/storage"}}`

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath, body)
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	require.JSONEq(t, `{"state":"restarting","restarting":true,"mode":"required",`+
		`"admission":"ask","signed_in":false,"steps":[]}`, rec.Body.String())
	require.Empty(t, rec.Result().Cookies())

	require.Len(t, setup.recorded, 1)
	require.Equal(t, map[string]any{"core.site": "workshop"}, setup.recorded[0].Configuration)
	require.Equal(t, "/srv/storage", setup.recorded[0].StorageRoot)
}

// TestAStorageWithoutAConfigurationIsRefused keeps the storage on the path
// that restarts, which is the only one that declares it.
func TestAStorageWithoutAConfigurationIsRefused(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true}
	body := `{"code":"K7QD-3M9X-TP2A","mode":"open","storage":{"root":"/srv/storage"}}`

	rec := call(t, setupServer(t, setup).Handler(), http.MethodPost, firstRunPath, body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	require.Empty(t, setup.recorded)
}

// TestTheWizardChecksAFolderBehindTheCode holds check-setup-storage to its
// published sample and to handing the code to the composition root.
func TestTheWizardChecksAFolderBehindTheCode(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true}
	handler := setupServer(t, setup).Handler()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, setupStoragePath,
		strings.NewReader(`{"root":"/srv/prusa-connect/storage"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Setup-Code", "K7QD-3M9X-TP2A")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	published := setupExamples()["check-setup-storage"]
	wanted, err := json.Marshal(published.Response)
	require.NoError(t, err)
	require.JSONEq(t, string(wanted), rec.Body.String())
	require.Equal(t, []string{"K7QD-3M9X-TP2A"}, setup.codes)
}

// TestTheWizardTriesAProviderBehindTheCode: the test of a sign-in provider is
// open while the first run is pending, takes the code, and answers with every
// step.
func TestTheWizardTriesAProviderBehindTheCode(t *testing.T) {
	t.Parallel()

	setup := &fakeSetup{pending: true}
	handler := setupServer(t, setup).Handler()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, setupOIDCPath,
		strings.NewReader(`{"issuer":"https://sso.example.com","client_id":"prusa-connect",`+
			`"client_secret":"s","redirect_uri":"http://localhost:8080/api/v1/auth/callback"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Setup-Code", "K7QD-3M9X-TP2A")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.JSONEq(t, `{"issuer":"https://sso.example.com","discovery":{"status":"passed"},`+
		`"client":{"status":"passed"},"redirect":{"status":"passed"}}`, rec.Body.String())
	require.Equal(t, []string{"K7QD-3M9X-TP2A"}, setup.codes)
}

func TestAnUnexpectedSetupFailureKeepsItsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("open /etc/prusa-connect/config.yaml: permission denied")
	err := setupFault(cause, "read the configuration the first run asks for")

	require.ErrorIs(t, err, cause)
	require.Contains(t, err.Error(), "permission denied")
}
