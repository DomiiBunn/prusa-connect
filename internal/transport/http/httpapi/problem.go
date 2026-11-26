package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// problemMediaType is the RFC 9457 framing every error on this transport is
// written in.
const problemMediaType = "application/problem+json"

// traceIDLen is the W3C trace-context trace-id: 16 bytes, 32 hex characters.
// parentIDLen is its parent-id: 8 bytes, 16 hex characters.
const (
	traceIDLen  = 16
	parentIDLen = 8
)

// traceparentFields is the count in a W3C traceparent header:
// version-traceid-parentid-flags.
const traceparentFields = 4

// errorCode is the schema projection of fault.Code, which is a struct with no
// exported fields and marshals as domain/REASON text. Without the alias the
// document would describe the identifier a client branches on as an empty
// object.
type errorCode string

// Schema describes the identifier for a client that generates types from the
// document, which would otherwise see an unconstrained string.
func (errorCode) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:    huma.TypeString,
		Pattern: `^[a-z0-9][a-z0-9.-]*/[A-Z][A-Z0-9_]*[A-Z0-9]$`,
		Description: "The stable identifier for this failure, as domain/REASON. A client branches on it " +
			"and indexes its message bundle by it. Codes in the core namespace are documented in the " +
			"error reference; a code from any other domain belongs to an extension.",
		Examples: []any{"core.printer/NOT_FOUND"},
	}
}

// errorParams is the schema projection of fault.Params, which marshals as an
// object of named scalars.
type errorParams map[string]any

// Schema names what the params object is for, which an empty object schema does
// not say.
func (errorParams) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type: huma.TypeObject,
		Description: "The values the code interpolates, keyed by the names the error reference publishes " +
			"for it. Each value is a JSON scalar. Absent when the code is registered as redacted.",
		AdditionalProperties: true,
	}
}

// problemError carries a Fault to the client as RFC 9457 problem+json. It
// embeds the payload rather than reshaping it, so the document Huma generates
// by reflecting over this type describes the bytes fault writes.
type problemError struct {
	fault.Problem

	cause *fault.Fault
}

// installErrorModel replaces Huma's own RFC 9457 model with ours. Huma
// documents the swap as its extension point for a custom error type, and it is
// the only one: defineErrors builds the error schema from
// reflect.TypeOf(NewError(0, "")), so a transformer that reshaped the body at
// write time would leave the published schema describing a body the server
// never sends.
var installErrorModel = sync.OnceFunc(func() {
	//nolint:reassign // huma.NewError is that library's documented hook for the error model.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return newProblemError(faultFor(context.Background(), status, msg, errs), "")
	}

	//nolint:reassign // the context-carrying half of the same hook.
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		f := faultFor(ctx.Context(), status, msg, errs).WithTrace(traceIDFrom(ctx.Context()))

		// The boundary an operation's failure leaves the server by, and the
		// only place that sees both the fault and the request it answers.
		logUnexpected(ctx.Context(), f, ctx.Method(), ctx.URL().Path)

		problem := newProblemError(f, ctx.URL().Path)
		logRefused(ctx.Context(), ctx.Operation(), problem.Problem)

		return problem
	}
})

// newProblemError frames a Fault. instance identifies the occurrence, which RFC
// 9457 wants and which the schema-only call site does not have.
func newProblemError(f *fault.Fault, instance string) *problemError {
	return &problemError{Problem: f.ProblemJSON(instance), cause: f}
}

// Error is the operator-facing line. It carries the wrapped cause and every
// param, neither of which the response body has.
func (e *problemError) Error() string { return e.cause.Error() }

// Unwrap exposes the Fault so errors.Is and fault.HasCode reach it.
func (e *problemError) Unwrap() error { return e.cause }

// GetStatus is huma.StatusError: the status the registry advises for this code.
func (e *problemError) GetStatus() int { return e.Status }

// ContentType is huma.ContentTypeFilter: RFC 9457 names the media type, and a
// client that negotiated plain JSON still gets the problem framing.
func (e *problemError) ContentType(ct string) string {
	switch ct {
	case "application/json":
		return problemMediaType
	case "application/cbor":
		return "application/problem+cbor"
	default:
		return ct
	}
}

// faultFor turns whatever Huma is reporting into a registered code. A Fault
// already on the error chain is used as it stands, because the domain that
// raised it knows more about the failure than the status code does.
func faultFor(ctx context.Context, status int, msg string, errs []error) *fault.Fault {
	for _, err := range errs {
		if f, ok := errors.AsType[*fault.Fault](err); ok {
			return f
		}
	}

	detail := strings.TrimSpace(msg + " " + joinDetails(errs))

	for _, err := range errs {
		if tooLarge, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return httpfault.BodyTooLarge(tooLarge.Limit)
		}

		// A handler that returned because its budget ran out says so with the
		// code for it, rather than reaching the client as an unexplained 500.
		// The budget is read off the context because the error carries no
		// number a caller could act on.
		if errors.Is(err, context.DeadlineExceeded) {
			return httpfault.RequestTimedOut(budgetFrom(ctx))
		}

		// A handler that returned because the caller hung up has not failed.
		// Both halves are checked: the request itself has to be the thing that
		// ended. A context the handler cancelled for its own reasons, under a
		// caller that is still waiting, is a failure and stays unexpected.
		if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
			return httpfault.RequestAbandoned()
		}
	}

	switch status {
	case http.StatusRequestEntityTooLarge:
		// The limit comes off the context rather than out of the message,
		// because a client told to send at most zero bytes has been told
		// nothing it can act on.
		return httpfault.BodyTooLarge(bodyLimitFrom(ctx))
	case http.StatusNotFound:
		return httpfault.RouteNotFound("", "")
	case http.StatusMethodNotAllowed:
		return httpfault.MethodNotAllowed("", "")
	}

	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		location, reason := firstDetail(errs)

		return httpfault.RequestInvalid(location, reason, detail)
	}

	return httpfault.Unexpected(detail)
}

// serverFault keeps a refusal the implementation raised and turns anything
// else into this server's own failure, with the error as its cause. A store
// that could not be read or written is not the caller's mistake, and reporting
// it as one sends them looking at what they typed. The answer stays redacted;
// the cause reaches the operator in the line logUnexpected writes.
//
// A cancellation is passed on unwrapped, so faultFor can tell a caller who hung
// up from a failure: only faultFor sees the request's context. A deadline is
// not, because faultFor reads every deadline as the request's own budget, and
// a store's own timeout would then reach the caller as REQUEST_TIMED_OUT with
// no log line.
func serverFault(err error, doing string) error {
	if raised, ok := errors.AsType[*fault.Fault](err); ok {
		return raised
	}

	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", doing, err)
	}

	return httpfault.Unexpected(doing).Wrap(err)
}

// firstDetail names the member Huma's validator rejected. fault.Params is a
// flat set, so one location is named and the rest reach the client through the
// detail text joinDetails builds.
func firstDetail(errs []error) (location, reason string) {
	for _, err := range errs {
		var detailer huma.ErrorDetailer
		if errors.As(err, &detailer) {
			detail := detailer.ErrorDetail()

			return detail.Location, detail.Message
		}
	}

	return "", ""
}

func joinDetails(errs []error) string {
	parts := make([]string, 0, len(errs))

	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}

	return strings.Join(parts, "; ")
}

// registerErrorSchemas teaches the schema generator the two fault types that
// carry their own JSON representation.
func registerErrorSchemas(registry huma.Registry) {
	registry.RegisterTypeAlias(reflect.TypeFor[fault.Code](), reflect.TypeFor[errorCode]())
	registry.RegisterTypeAlias(reflect.TypeFor[fault.Params](), reflect.TypeFor[errorParams]())
}

// logUnexpected writes the one line the unexpected code promises. That code is
// registered as redacted, so the client reads only a code and a trace id, and
// that id is the one way an operator finds the cause.
//
// The line carries the message and the wrapped cause. The params stay out of
// it: they are the values this occurrence would have interpolated, and a
// redacted code withholds them from the response, so the log does too.
func logUnexpected(ctx context.Context, f *fault.Fault, method, path string) {
	if f.Code() != httpfault.CodeUnexpected() {
		return
	}

	loggerFrom(ctx).ErrorContext(ctx, "unexpected failure answering a request",
		slog.String("trace_id", traceIDFrom(ctx)),
		slog.String("method", method),
		slog.String("path", path),
		slog.String("code", f.Code().String()),
		slog.String("error", f.Error()))
}

// logRefused gives a refusal one line, so a 4xx a browser reports has a
// server-side record under the same trace id. It names the operation and not
// the path, and carries no params, detail or principal: a diagnostics bundle
// holds this line. A 401 is left out because a page polling an expired session
// would fill the log with it.
func logRefused(ctx context.Context, op *huma.Operation, p fault.Problem) {
	if p.Status < http.StatusBadRequest || p.Status >= http.StatusInternalServerError ||
		p.Status == http.StatusUnauthorized || op == nil {
		return
	}

	loggerFrom(ctx).InfoContext(ctx, "operation refused",
		slog.String("operation", op.OperationID),
		slog.Int("status", p.Status),
		slog.String("code", p.Code.String()),
		slog.String("trace_id", traceIDFrom(ctx)))
}

// writeProblem renders a Fault on a plain net/http response, for the paths that
// never reach a Huma operation: the router's own refusals, a panic, and the
// handlers that serve the document and the reference.
func writeProblem(w http.ResponseWriter, r *http.Request, f *fault.Fault) {
	logUnexpected(r.Context(), f, r.Method, r.URL.Path)

	problem := f.WithTrace(traceIDFrom(r.Context())).ProblemJSON(r.URL.Path)

	body, err := json.Marshal(problem)
	if err != nil {
		// A Fault that cannot be marshalled is answered with no body. The status
		// is still correct.
		w.WriteHeader(problem.Status)

		return
	}

	w.Header().Set("Content-Type", problemMediaType)
	w.WriteHeader(problem.Status)
	_, _ = w.Write(body)
}

// traceKey is the context key the request trace id is carried under. An
// unexported type keeps it from colliding with anybody else's key.
type traceKey struct{}

// withTraceID returns a context carrying the W3C trace id of this request.
func withTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceKey{}, traceID)
}

// traceIDFrom returns the trace id the middleware put on the context, empty
// when there is none.
func traceIDFrom(ctx context.Context) string {
	traceID, _ := ctx.Value(traceKey{}).(string)

	return traceID
}

// loggerKey is the context key the server's logger is carried under, so the
// place that answers a failure can write its line without a package-level
// logger.
type loggerKey struct{}

// withLogger returns a context carrying the logger the server was built with.
func withLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// loggerFrom returns the logger the middleware put on the context. A request
// that did not pass through the middleware gets one that discards, so no caller
// has to test for nil before it logs.
func loggerFrom(ctx context.Context) *slog.Logger {
	logger, ok := ctx.Value(loggerKey{}).(*slog.Logger)
	if !ok || logger == nil {
		return slog.New(slog.DiscardHandler)
	}

	return logger
}

// bodyLimitKey is the context key the configured body bound is carried under,
// so a refusal can name the number the client has to fit inside.
type bodyLimitKey struct{}

func withBodyLimit(ctx context.Context, limit int64) context.Context {
	return context.WithValue(ctx, bodyLimitKey{}, limit)
}

// bodyLimitFrom returns the bound bodyLimitMiddleware set, zero when the
// request did not pass through it.
func bodyLimitFrom(ctx context.Context) int64 {
	limit, _ := ctx.Value(bodyLimitKey{}).(int64)

	return limit
}

// traceIDOf returns the trace id of the incoming request: the trace-id field of
// a well-formed traceparent, or a fresh one. A client-supplied value is used
// for correlation only and never for identity.
//
// The header is validated rather than trusted. W3C trace-context forbids the
// all-zero trace-id and version ff, and requires lowercase hex throughout; an
// unauthenticated caller sending any of them would otherwise put every log line
// it produced on one id of its choosing.
func traceIDOf(r *http.Request) string {
	if traceID, ok := traceparentID(r.Header.Get("Traceparent")); ok {
		return traceID
	}

	buf := make([]byte, traceIDLen)
	// rand.Read is documented never to return an error as of go 1.24.
	_, _ = rand.Read(buf)

	return hex.EncodeToString(buf)
}

// traceparentID validates a W3C traceparent and returns its trace-id.
func traceparentID(header string) (string, bool) {
	fields := strings.Split(header, "-")
	if len(fields) != traceparentFields {
		return "", false
	}

	version, traceID, parentID := fields[0], fields[1], fields[2]

	switch {
	case !lowerHexOf(version, 1) || version == "ff":
		return "", false
	case !lowerHexOf(traceID, traceIDLen) || isZeroHex(traceID):
		return "", false
	case !lowerHexOf(parentID, parentIDLen) || isZeroHex(parentID):
		return "", false
	case !lowerHexOf(fields[3], 1):
		return "", false
	}

	return traceID, true
}

// lowerHexOf reports whether s is exactly bytes octets of lowercase hex.
// hex.DecodeString accepts uppercase, which trace-context does not, and two
// spellings of one id are two ids in a log index.
func lowerHexOf(s string, bytes int) bool {
	if len(s) != hex.EncodedLen(bytes) {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

func isZeroHex(s string) bool { return strings.Trim(s, "0") == "" }
