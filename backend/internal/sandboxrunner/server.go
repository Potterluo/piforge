// Package sandboxrunner is the HTTP service that owns the Docker CLI for one
// PiForge deployment.
//
// The control plane must not have the daemon: /var/run/docker.sock is host root
// (docs/SANDBOX-DEPLOY.md §1), and the control plane is the process exposed to
// users and models. This service is the only process that runs `docker`, and it
// exposes the lifecycle as six JSON operations — no Docker API, no exec, no
// filesystem browsing:
//
//	POST   /v1/sandboxes                      {user_id, project_id, image, repo_url,
//	                                           branch, api_port, api_key, cpus,
//	                                           memory, network} → record
//	GET    /v1/sandboxes/{id}                 → record
//	POST   /v1/sandboxes/{id}/pause           → 204
//	POST   /v1/sandboxes/{id}/resume          → record
//	DELETE /v1/sandboxes/{id}                 → 204
//	POST   /v1/sandboxes/{id}/worktrees       {repo_dir, path, branch} → 204
//
// Every request needs `Authorization: Bearer <token>`; the comparison is
// constant-time and a missing or wrong token is 401 before any work happens.
//
// The container rules are not here. The DockerProvider below calls
// sandbox.Docker — the same type the in-process provider uses — so the per-pair
// workspace path, the mount check, the checkout through the sandbox API and the
// branch worktree as the sandbox user exist exactly once.
package sandboxrunner

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// Provider is the lifecycle the HTTP surface fronts. DockerProvider implements
// it over sandbox.Docker; the handler tests implement it in memory.
type Provider interface {
	Ensure(ctx context.Context, request sandbox.RunnerRequest) (sandbox.RunnerRecord, error)
	Get(ctx context.Context, sandboxID string) (sandbox.RunnerRecord, error)
	Pause(ctx context.Context, sandboxID string) error
	Resume(ctx context.Context, sandboxID string) (sandbox.RunnerRecord, error)
	Destroy(ctx context.Context, sandboxID string) error
	AddWorktree(ctx context.Context, sandboxID string, request sandbox.RunnerWorktreeRequest) error
}

// Config configures the HTTP surface.
type Config struct {
	// Token is the shared bearer token. Required: a runner without one is a
	// container-creating HTTP service open to its whole network.
	Token string
	// Provider performs the lifecycle. Required.
	Provider Provider
	// Logger receives one line per request. Defaults to slog.Default().
	Logger *slog.Logger
	// MaxBody bounds a request body (default 1 MiB; every body here is a small
	// JSON object).
	MaxBody int64
	// Timeout bounds one handler, so a wedged `docker` call cannot hold a
	// connection forever. Defaults to 20 minutes, above the client's own bounds.
	Timeout time.Duration
}

// Server is the runner's HTTP handler.
type Server struct {
	cfg     Config
	handler http.Handler
}

// New builds the handler.
func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("sandboxrunner: a token is required (RUNNER_TOKEN)")
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("sandboxrunner: a provider is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = 1 << 20
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Minute
	}

	server := &Server{cfg: cfg}
	mux := http.NewServeMux()
	// Method patterns (Go 1.22+) are the whole routing table: a wrong method gets
	// 405 and any other path is a 404, so "no other endpoints" is a property of
	// this list rather than of a dispatcher.
	mux.HandleFunc("POST /v1/sandboxes", server.handleEnsure)
	mux.HandleFunc("GET /v1/sandboxes/{id}", server.handleGet)
	mux.HandleFunc("POST /v1/sandboxes/{id}/pause", server.handlePause)
	mux.HandleFunc("POST /v1/sandboxes/{id}/resume", server.handleResume)
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", server.handleDestroy)
	mux.HandleFunc("POST /v1/sandboxes/{id}/worktrees", server.handleWorktree)
	// The one route that is a stream rather than a JSON object. It exists for the
	// agent's RPC channel and for nothing else; see attach.go and
	// docs/EXECUTION-BOUNDARY.md §2.
	mux.HandleFunc("POST /v1/sandboxes/{id}/attach", server.handleAttach)
	// Its counterpart for a process that is already there: stage 3's reattach,
	// which is how a restarted control plane reaches the agent the supervisor
	// kept alive. See reattach.go and docs/EXECUTION-BOUNDARY.md §6.
	mux.HandleFunc("POST /v1/sandboxes/{id}/reattach", server.handleReattach)
	server.handler = server.authenticate(server.logRequests(mux))
	return server, nil
}

// Handler returns the http.Handler to serve.
func (s *Server) Handler() http.Handler { return s.handler }

// authenticate enforces the shared bearer token before anything is decoded or
// created.
//
// The comparison is over SHA-256 digests, so it takes the same time whatever the
// presented token's length is: a plain constant-time compare of differently sized
// slices returns 0 immediately, which leaks the token's length.
func (s *Server) authenticate(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(s.cfg.Token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "a bearer token is required")
			return
		}
		got := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "the bearer token does not match")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken parses a "Bearer <token>" Authorization header.
func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

func (s *Server) handleEnsure(w http.ResponseWriter, r *http.Request) {
	var request sandbox.RunnerRequest
	if !s.decode(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.UserID) == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if strings.TrimSpace(request.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	record, err := s.cfg.Provider.Ensure(ctx, request)
	if err != nil {
		s.fail(r, w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	record, err := s.cfg.Provider.Get(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(r, w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	if err := s.cfg.Provider.Pause(ctx, r.PathValue("id")); err != nil {
		s.fail(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	record, err := s.cfg.Provider.Resume(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(r, w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleDestroy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	if err := s.cfg.Provider.Destroy(ctx, r.PathValue("id")); err != nil {
		s.fail(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWorktree(w http.ResponseWriter, r *http.Request) {
	var request sandbox.RunnerWorktreeRequest
	if !s.decode(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.RepoDir) == "" {
		writeError(w, http.StatusBadRequest, "repo_dir is required")
		return
	}
	if strings.TrimSpace(request.Path) == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if strings.TrimSpace(request.Branch) == "" {
		writeError(w, http.StatusBadRequest, "branch is required")
		return
	}
	// `git worktree add -b` would read a leading dash as an option. The provider
	// rejects it too, but a client mistake should read as 400 rather than as a
	// failed git command.
	if strings.HasPrefix(strings.TrimSpace(request.Branch), "-") {
		writeError(w, http.StatusBadRequest, "branch must not start with a dash")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	if err := s.cfg.Provider.AddWorktree(ctx, r.PathValue("id"), request); err != nil {
		s.fail(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decode reads one JSON object, bounded, and answers 400 itself when it cannot.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBody)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	// One object per request: trailing data means the caller sent something this
	// operation does not understand, and ignoring it would silently drop fields.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "the body must contain exactly one JSON object")
		return false
	}
	return true
}

// fail maps a provider error onto a status. The mapping is the whole contract the
// client branches on: 404 means "this sandbox does not exist" (sandbox.ErrNotFound),
// 400 means the request was wrong, everything else is the runner's problem.
func (s *Server) fail(r *http.Request, w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, sandbox.ErrInvalidID):
		status = http.StatusBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		status = http.StatusServiceUnavailable
	}
	if status >= 500 {
		s.cfg.Logger.Error("sandbox runner request failed",
			"method", r.Method, "path", r.URL.Path, "status", status, "error", err.Error())
	} else if status == http.StatusNotFound {
		// A missing sandbox is a normal answer (the reaper and Get both ask about
		// sandboxes that may be gone), not a rejected request.
		s.cfg.Logger.Info("sandbox runner has no such sandbox",
			"method", r.Method, "path", r.URL.Path, "error", err.Error())
	} else {
		s.cfg.Logger.Warn("sandbox runner rejected a request",
			"method", r.Method, "path", r.URL.Path, "status", status, "error", err.Error())
	}
	writeError(w, status, err.Error())
}

// logRequests records one line per request, including the status the handler
// chose. It is the runner's only audit trail, so it stays on at info level.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.cfg.Logger.Info("sandbox runner request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"remote", r.RemoteAddr)
	})
}

// statusRecorder remembers the status code for the log line.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Hijack forwards the connection upgrade the attach route needs. Without it the
// recorder would hide http.Hijacker and attach could only answer 500.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("sandboxrunner: the response writer cannot hijack")
	}
	return hijacker.Hijack()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
