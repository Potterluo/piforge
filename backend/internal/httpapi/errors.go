package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/store"
)

// maxBodyBytes bounds JSON request bodies. Slightly generous because
// POST /messages may carry base64 images.
const maxBodyBytes = 12 << 20 // 12 MiB

// statusByCode is the single table that maps a contracts error code to its
// HTTP status (PRD §9.7). Keeping it in one place is what lets the frontend
// rely on the pairing.
var statusByCode = map[string]int{
	contracts.ErrInvalidArgument: 400,
	contracts.ErrNoModel:         400,
	contracts.ErrUnauthenticated: 401,
	contracts.ErrForbidden:       403,
	contracts.ErrNotFound:        404,
	contracts.ErrSessionBusy:     409,
	contracts.ErrBrowserBusy:     409,
	contracts.ErrNeedFullResync:  409,
	contracts.ErrBranchConflict:  409,
	contracts.ErrConflict:        409,
	contracts.ErrSharedProvider:  409,
	contracts.ErrFileTooLarge:    413,
	contracts.ErrSandboxUnavail:  422, // 422 is the PRD's default for this code
	contracts.ErrSandboxPaused:   423,
	// The sandbox checkout is over its disk budget (FR-5.8). 507 is the status
	// for "insufficient storage" and the only one that does not read as "retry
	// and it will work": nothing changes until something is deleted.
	contracts.ErrSandboxDiskQuota: 507,
	contracts.ErrRateLimited:      429,
	contracts.ErrUpstreamOverload: 503,
	// A forge that answered 5xx or could not be reached is a bad gateway from
	// the product's point of view; its 4xx answers are mirrored by the handler.
	contracts.ErrForge:    502,
	contracts.ErrInternal: 500,
}

// statusFor returns the HTTP status for a contracts error code. Unknown codes
// are a server bug and become 500.
func statusFor(code string) int {
	if status, ok := statusByCode[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// statusErr attaches an explicit status to an API error when the default
// mapping is not the one the caller wants (for example "the workspace is not
// provisioned" still reports sandbox_unavailable, but with 503).
type statusErr struct {
	status int
	api    *contracts.APIError
}

func (e *statusErr) Error() string { return e.api.Error() }
func (e *statusErr) Unwrap() error { return e.api }

// withStatus builds an error that renders as api but with an explicit status.
func withStatus(status int, code, message string) error {
	return withStatusDetails(status, code, message, nil)
}

// withStatusDetails is withStatus plus machine-readable details, for refusals the
// UI has to explain (the parallel limit names both the cap and what is running).
func withStatusDetails(status int, code, message string, details map[string]any) error {
	return &statusErr{status: status, api: &contracts.APIError{Code: code, Message: message, Details: details}}
}

// apiErr is a shorthand for a typed API error.
func apiErr(code, format string, args ...any) error {
	return &contracts.APIError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// classify turns any internal error into the wire form plus the status to send.
//
// This is the only translation point between "what went wrong" and "what the
// client sees", so it is where the security rules live:
//   - store.ErrNotFound (used for cross-user access) becomes 404, never 403;
//   - unknown errors become a generic 500 whose text is logged, not returned.
func classify(err error) (int, *contracts.APIError) {
	if err == nil {
		return http.StatusInternalServerError, &contracts.APIError{
			Code: contracts.ErrInternal, Message: "internal error",
		}
	}

	var se *statusErr
	if errors.As(err, &se) {
		return se.status, se.api
	}

	var api *contracts.APIError
	if errors.As(err, &api) {
		return statusFor(api.Code), api
	}

	switch {
	case errors.Is(err, store.ErrNotFound):
		// A user never asked for "not found": they asked for a thing that is gone
		// or was never theirs, and the message should say so in their language.
		return http.StatusNotFound, &contracts.APIError{Code: contracts.ErrNotFound, Message: "找不到这个对象，可能已被删除"}
	case errors.Is(err, store.ErrForbidden):
		return http.StatusForbidden, &contracts.APIError{Code: contracts.ErrForbidden, Message: userMessage(err)}
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, &contracts.APIError{Code: contracts.ErrConflict, Message: userMessage(err)}
	case errors.Is(err, store.ErrInvalid):
		return http.StatusBadRequest, &contracts.APIError{Code: contracts.ErrInvalidArgument, Message: userMessage(err)}
	// Filesystem rejections are client errors, not bugs: a path that escapes
	// the worktree is exactly the "invalid_argument" the caller sent, and
	// answering 500 would make an attack indistinguishable from an outage.
	case errors.Is(err, fsx.ErrEscape):
		return http.StatusBadRequest, &contracts.APIError{Code: contracts.ErrInvalidArgument, Message: "path escapes the workspace"}
	case errors.Is(err, fsx.ErrNotExist):
		return http.StatusNotFound, &contracts.APIError{Code: contracts.ErrNotFound, Message: "no such file or directory"}
	case errors.Is(err, fsx.ErrIsDir):
		return http.StatusBadRequest, &contracts.APIError{Code: contracts.ErrInvalidArgument, Message: "path is a directory"}
	case errors.Is(err, auth.ErrInvalidCredentials):
		return http.StatusUnauthorized, &contracts.APIError{Code: contracts.ErrUnauthenticated, Message: "invalid email or password"}
	case errors.Is(err, auth.ErrNoSession):
		return http.StatusUnauthorized, &contracts.APIError{Code: contracts.ErrUnauthenticated, Message: "authentication required"}
	case errors.Is(err, auth.ErrAccountDisabled):
		return http.StatusForbidden, &contracts.APIError{Code: contracts.ErrForbidden, Message: "account is disabled"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away (a navigation, an aborted fetch). Nothing will
		// read the body, and this is not a server fault, so it must not be
		// reported as a 500 — that turns normal navigation into alarming logs.
		return statusClientClosed, &contracts.APIError{Code: contracts.ErrClientClosed, Message: "client closed the request"}
	}
	return http.StatusInternalServerError, &contracts.APIError{Code: contracts.ErrInternal, Message: "internal error"}
}

// statusClientClosed is the convention for "the caller is gone": no standard
// status exists, and reporting 500 would misclassify an ordinary navigation as
// a server bug.
const statusClientClosed = 499

// errorEnvelope is the wire shape of every failure: PRD §9.1 wraps the typed
// contracts.APIError in an "error" object, and web/lib/types.ts ApiErrorBody
// reads body.error.code. Writing the bare APIError would make every failure
// look like code "internal" to the client, so the envelope is not optional.
type errorEnvelope struct {
	Error *contracts.APIError `json:"error"`
}

// writeErr writes the contracts.APIError wire shape with the status that
// belongs to its code. Handlers never write error bodies themselves.
func writeErr(w http.ResponseWriter, err error) {
	status, body := classify(err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: body})
}

// fail logs server-side failures and writes the client-facing error. 4xx are
// expected traffic and stay at info level; 5xx are bugs and are logged with
// the full wrapped error.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, _ := classify(err)
	if status >= 500 {
		s.log.ErrorContext(r.Context(), "request failed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.String("error", err.Error()))
	} else {
		s.log.DebugContext(r.Context(), "request rejected",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.String("error", err.Error()))
	}
	writeErr(w, err)
}

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// ok is the {"ok": true} envelope the panels use for mutating workspace calls.
type okBody struct {
	OK bool `json:"ok"`
}

func writeOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, okBody{OK: true}) }

// detach returns a context that survives the client hanging up, bounded by
// timeout.
//
// Long-lived side effects belong to the session, not to the HTTP request that
// triggered them: if the browser navigates away mid-request, the agent process
// must still start and the prompt must still be delivered. The timeout keeps a
// wedged runtime from pinning the request goroutine forever.
func detach(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// decodeJSON decodes a bounded JSON body. Unknown fields are rejected so a
// typo in the client fails loudly instead of being silently ignored.
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return apiErr(contracts.ErrInvalidArgument, "request body is required")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return apiErr(contracts.ErrInvalidArgument, "request body is required")
		}
		return apiErr(contracts.ErrInvalidArgument, "invalid JSON body: %v", err)
	}
	return nil
}

// decodeOptionalJSON decodes a JSON body, tolerating an absent one: a missing
// body leaves v zeroed rather than failing. Used by endpoints where every
// field is optional.
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return apiErr(contracts.ErrInvalidArgument, "could not read body: %v", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apiErr(contracts.ErrInvalidArgument, "invalid JSON body: %v", err)
	}
	return nil
}

// internalPrefixes are the package names that show up at the front of a wrapped
// error and mean nothing to a user. `store: create user "x": conflict` reached
// the UI verbatim before this existed.
var internalPrefixes = []string{
	"store: ", "fsx: ", "gitx: ", "sandbox: ", "term: ", "pi: ", "llmgw: ", "auth: ", "pimodel: ",
}

// userMessage strips the wrapping an internal package adds so the message reads
// as a sentence about the request rather than a stack trace.
func userMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	for _, prefix := range internalPrefixes {
		message = strings.TrimPrefix(message, prefix)
		for strings.HasPrefix(message, prefix) {
			message = strings.TrimPrefix(message, prefix)
		}
	}
	// A trailing ", conflict" / ": invalid argument" is the sentinel we matched
	// on; repeating it to the user adds nothing.
	for _, suffix := range []string{": conflict", ": invalid argument", ": not found", ": forbidden"} {
		message = strings.TrimSuffix(message, suffix)
	}
	if message == "" {
		return "the request could not be completed"
	}
	return message
}
