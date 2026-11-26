package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/fault/faulttest"
)

// errStoreRefused stands in for whatever a handler failed on.
var errStoreRefused = errors.New("the record store refused the write")

// logLine is one line of the logger the harness is given.
type logLine struct {
	Message string `json:"msg"`
	TraceID string `json:"trace_id"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Code    string `json:"code"`
	Error   string `json:"error"`
	Panic   string `json:"panic"`
	Stack   string `json:"stack"`
}

// logLines reads every line the logger wrote. A panic writes two: where the
// handler stopped, and the failure the client was answered with.
func logLines(t *testing.T, written *bytes.Buffer) []logLine {
	t.Helper()

	var lines []logLine

	for raw := range bytes.SplitSeq(bytes.TrimSpace(written.Bytes()), []byte("\n")) {
		var line logLine

		require.NoError(t, json.Unmarshal(raw, &line))

		lines = append(lines, line)
	}

	return lines
}

// TestAnUnexpectedFailureIsLoggedUnderTheTraceItSends holds the promise the
// redacted code makes. The client is given a trace id and no detail, so the
// detail has to be in a log line carrying the same id; without that line the
// trace id in the response points at nothing.
func TestAnUnexpectedFailureIsLoggedUnderTheTraceItSends(t *testing.T) {
	t.Parallel()

	var written bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&written, nil))

	handler := newLoggingHarness(t, defaultBodyLimit, logger, func(api huma.API) {
		register(api, huma.Operation{
			OperationID: "get-broken",
			Method:      http.MethodGet,
			Path:        "/broken",
			Summary:     "fails",
			Security:    noSecurity,
		}, func(_ context.Context, _ *struct{}) (*struct{}, error) {
			return nil, errStoreRefused
		})
	})

	rec := call(t, handler, http.MethodGet, "/broken", "")

	body := requireProblem(t, rec, http.StatusInternalServerError)
	require.Equal(t, httpfault.CodeUnexpected(), body.Code)
	require.Empty(t, body.Detail)
	require.NotEmpty(t, body.TraceID)

	var line logLine

	require.NoError(t, json.Unmarshal(written.Bytes(), &line),
		"the transport wrote no log line for the failure it answered")
	require.Equal(t, body.TraceID, line.TraceID)
	require.Equal(t, http.MethodGet, line.Method)
	require.Equal(t, "/broken", line.Path)
	require.Equal(t, httpfault.CodeUnexpected().String(), line.Code)
	require.Contains(t, line.Error, errStoreRefused.Error())
}

// TestACallerThatHangsUpIsNotAnUnexpectedFailure holds what the server says
// when the caller leaves while a handler waits, which is what a command queued
// for an away printer does when the client gives up. Nobody reads the answer,
// so what matters is the log and the status the metrics count. Answered as
// core.internal/UNEXPECTED, every client that gave up wrote an ERROR line and
// counted as a 500.
func TestACallerThatHangsUpIsNotAnUnexpectedFailure(t *testing.T) {
	t.Parallel()

	var written bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&written, nil))

	caller, hangUp := context.WithCancel(t.Context())

	handler := newLoggingHarness(t, defaultBodyLimit, logger, func(api huma.API) {
		register(api, huma.Operation{
			OperationID: "post-waiting",
			Method:      http.MethodPost,
			Path:        "/waiting",
			Summary:     "waits for the caller to leave",
			Security:    noSecurity,
		}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			hangUp()
			<-ctx.Done()

			return nil, fmt.Errorf("printer command core.command.set_printer_ready: xbuddy: waiting: %w", ctx.Err())
		})
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(caller, http.MethodPost, "/waiting", nil))

	body := requireProblem(t, rec, httpfault.RequestAbandoned().Status())
	require.Equal(t, httpfault.CodeRequestAbandoned(), body.Code)
	require.NotContains(t, written.String(), httpfault.CodeUnexpected().String(),
		"a caller that hung up was logged as a failure of this server")
}

// TestACallerThatHangsUpDuringAStoreCallIsNotAnUnexpectedFailure is the same
// hang-up reaching the answer through serverFault, as it does when the caller
// leaves while an account or credential store is busy.
func TestACallerThatHangsUpDuringAStoreCallIsNotAnUnexpectedFailure(t *testing.T) {
	t.Parallel()

	var written bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&written, nil))

	caller, hangUp := context.WithCancel(t.Context())

	handler := newLoggingHarness(t, defaultBodyLimit, logger, func(api huma.API) {
		register(api, huma.Operation{
			OperationID: "get-listing",
			Method:      http.MethodGet,
			Path:        "/listing",
			Summary:     "lists while the caller leaves",
			Security:    noSecurity,
		}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			hangUp()
			<-ctx.Done()

			return nil, serverFault(fmt.Errorf("list the credentials: %w", ctx.Err()), "list the credentials")
		})
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(caller, http.MethodGet, "/listing", nil))

	body := requireProblem(t, rec, httpfault.RequestAbandoned().Status())
	require.Equal(t, httpfault.CodeRequestAbandoned(), body.Code)
	require.NotContains(t, written.String(), httpfault.CodeUnexpected().String(),
		"a caller that hung up was logged as a failure of this server")
}

// TestAContextTheHandlerCancelledItselfIsStillUnexpected is the other half. A
// handler that cancels a context of its own, while the caller still waits, has
// failed, and reading that as a caller who left would hide the failure.
func TestAContextTheHandlerCancelledItselfIsStillUnexpected(t *testing.T) {
	t.Parallel()

	var written bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&written, nil))

	handler := newLoggingHarness(t, defaultBodyLimit, logger, func(api huma.API) {
		register(api, huma.Operation{
			OperationID: "post-self-cancelled",
			Method:      http.MethodPost,
			Path:        "/self-cancelled",
			Summary:     "cancels its own work",
			Security:    noSecurity,
		}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			own, cancel := context.WithCancel(ctx)
			cancel()

			return nil, fmt.Errorf("the work stopped: %w", own.Err())
		})
	})

	rec := call(t, handler, http.MethodPost, "/self-cancelled", "")

	body := requireProblem(t, rec, http.StatusInternalServerError)
	require.Equal(t, httpfault.CodeUnexpected(), body.Code)
	require.Contains(t, written.String(), httpfault.CodeUnexpected().String(),
		"a failure the server caused itself was not logged")
}

// TestAStoreCallTheHandlerCancelledItselfNamesWhatItWasDoing is that failure
// reaching the answer through serverFault. The cancellation passes through
// unwrapped, and the log line still says which step it stopped.
func TestAStoreCallTheHandlerCancelledItselfNamesWhatItWasDoing(t *testing.T) {
	t.Parallel()

	var written bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&written, nil))

	handler := newLoggingHarness(t, defaultBodyLimit, logger, func(api huma.API) {
		register(api, huma.Operation{
			OperationID: "get-self-cancelled-listing",
			Method:      http.MethodGet,
			Path:        "/self-cancelled-listing",
			Summary:     "cancels its own store call",
			Security:    noSecurity,
		}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			own, cancel := context.WithCancel(ctx)
			cancel()

			return nil, serverFault(own.Err(), "list the credentials")
		})
	})

	rec := call(t, handler, http.MethodGet, "/self-cancelled-listing", "")

	body := requireProblem(t, rec, http.StatusInternalServerError)
	require.Equal(t, httpfault.CodeUnexpected(), body.Code)
	require.Contains(t, written.String(), "list the credentials: context canceled",
		"the log line lost the step the cancelled call was doing")
}

// TestAPanicIsLoggedWithItsCauseAndItsStack checks the other way into the
// redacted code: the panic value reaches the log and never the response, and
// the stack it happened on is written with it. Without the stack an operator
// reads that a handler panicked and cannot tell which line did it.
func TestAPanicIsLoggedWithItsCauseAndItsStack(t *testing.T) {
	t.Parallel()

	var written bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&written, nil))

	handler := newLoggingHarness(t, defaultBodyLimit, logger, func(api huma.API) {
		register(api, huma.Operation{
			OperationID: "get-panicking",
			Method:      http.MethodGet,
			Path:        "/panicking",
			Summary:     "panics",
			Security:    noSecurity,
		}, func(_ context.Context, _ *struct{}) (*struct{}, error) {
			panic("the connection string is postgres://user:hunter2@db/printers")
		})
	})

	rec := call(t, handler, http.MethodGet, "/panicking", "")

	body := requireProblem(t, rec, http.StatusInternalServerError)
	require.Equal(t, httpfault.CodeUnexpected(), body.Code)
	require.NotContains(t, rec.Body.String(), "hunter2")

	lines := logLines(t, &written)
	require.Len(t, lines, 2)

	stopped := lines[0]
	require.Equal(t, "a handler panicked", stopped.Message)
	require.Equal(t, body.TraceID, stopped.TraceID)
	require.Equal(t, http.MethodGet, stopped.Method)
	require.Equal(t, "/panicking", stopped.Path)
	require.Contains(t, stopped.Panic, "hunter2")
	require.Contains(t, stopped.Stack, "runtime/debug.Stack")
	require.Contains(t, stopped.Stack, "problem_test.go",
		"the stack does not reach the handler that panicked")

	answered := lines[1]
	require.Equal(t, body.TraceID, answered.TraceID)
	require.Contains(t, answered.Error, "hunter2")
}

// TestTheStateRefusalIsFramedAsAConflict is where a fault code becomes an HTTP
// status for ADR-20260831-c7b rule 31's refusal. The status is not written on this
// transport: it is the one the registration carries, and this holds the two
// ends of that against each other.
//
// The state a device is in is a conflict with the request rather than a fault in
// it, so 400 would tell a client to change what it sent. The framing also has to
// carry the state the refusal was decided on, because a refusal a client cannot
// render is a refusal a person reads as a failure.
func TestTheStateRefusalIsFramedAsAConflict(t *testing.T) {
	t.Parallel()

	raised := fault.PrinterVerbNotAllowedInState.
		New("the printer is printing, so the verb was not sent").
		With(fault.StringParam("device_id", examplePrinterID),
			fault.StringParam("verb", "core.command.set_nozzle_temperature"),
			fault.StringParam("state", "PRINTING"))

	framed := newProblemError(raised, "/api/v1/devices/"+examplePrinterID+"/command")

	require.Equal(t, http.StatusConflict, framed.GetStatus())
	require.Equal(t, problemMediaType, framed.ContentType("application/json"))

	body, err := json.Marshal(framed)
	require.NoError(t, err)

	faulttest.AssertBodyRegistered(t, fault.Global(), body)

	var problem struct {
		Code   string            `json:"code"`
		Status int               `json:"status"`
		Type   string            `json:"type"`
		Params map[string]string `json:"params"`
	}

	require.NoError(t, json.Unmarshal(body, &problem))

	require.Equal(t, "core.printer/VERB_NOT_ALLOWED_IN_STATE", problem.Code)
	require.Equal(t, http.StatusConflict, problem.Status)
	require.Equal(t, fault.TypeBaseURI+"#core-printer-verb-not-allowed-in-state", problem.Type)
	require.Equal(t, "PRINTING", problem.Params["state"],
		"the refusal reached the client without the state it was decided on")
	require.Equal(t, "core.command.set_nozzle_temperature", problem.Params["verb"])
}

// TestARefusalIsLoggedOnceWithoutItsDetail gives a 4xx a server-side line in
// a diagnostics bundle. The line names the operation, the status, the code and
// the trace; params, detail, path and principal could name the operator's
// things and stay out. A 401 writes nothing, because a page polling an expired
// session would fill the log with it, and a 5xx keeps its own single line.
func TestARefusalIsLoggedOnceWithoutItsDetail(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		want []map[string]any
	}{
		"refused": {
			err: httpfault.RequestInvalid("body.name", "Bob's MK4 is taken", "detail"),
			want: []map[string]any{{
				"level": "INFO", "msg": "operation refused", "operation": "post-thing",
				"status": float64(httpfault.RequestInvalid("", "", "").Status()), "code": httpfault.CodeRequestInvalid().String(),
			}},
		},
		"unauthorized": {err: fault.AuthTokenInvalid.New("expired")},
		"unexpected": {
			err:  errStoreRefused,
			want: []map[string]any{{"level": "ERROR", "msg": "unexpected failure answering a request"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var written bytes.Buffer

			handler := newLoggingHarness(t, defaultBodyLimit, slog.New(slog.NewJSONHandler(&written, nil)),
				func(api huma.API) {
					register(api, huma.Operation{
						OperationID: "post-thing",
						Method:      http.MethodPost,
						Path:        "/thing",
						Summary:     "fails",
						Security:    noSecurity,
					}, func(_ context.Context, _ *struct{}) (*struct{}, error) {
						return nil, tc.err
					})
				})

			call(t, handler, http.MethodPost, "/thing", "")

			var lines []map[string]any

			for raw := range bytes.SplitSeq(bytes.TrimSpace(written.Bytes()), []byte("\n")) {
				if len(raw) == 0 {
					continue
				}

				var line map[string]any

				require.NoError(t, json.Unmarshal(raw, &line))
				lines = append(lines, line)
			}

			require.Len(t, lines, len(tc.want))

			for i, want := range tc.want {
				require.NotEmpty(t, lines[i]["trace_id"])

				if want["msg"] == "operation refused" {
					delete(lines[i], "time")
					delete(lines[i], "trace_id")
					require.Equal(t, want, lines[i])

					continue
				}

				require.Equal(t, want["msg"], lines[i]["msg"])
				require.Equal(t, want["level"], lines[i]["level"])
			}
		})
	}
}
