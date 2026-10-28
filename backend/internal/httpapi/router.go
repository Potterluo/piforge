// Package httpapi is the REST + SSE surface of PiForge.
//
// It is stdlib net/http with Go 1.22 method-and-wildcard routing; there is no
// third-party router. Handlers own no business logic of their own: they
// authenticate, validate, call internal/store for persistence and
// contracts.AgentRuntime / contracts.Workspace / contracts.SandboxProvider for
// everything that touches an agent, a worktree or a machine.
//
// Two rules are load-bearing:
//
//   - Authorization is per-resource, not per-route. A non-admin may only touch
//     what they own, and "not yours" is reported as 404 not_found so a caller
//     cannot probe which ids exist (PRD §9.7).
//   - Every mutation passes RequireUser *and* RequireCSRF. GET/HEAD are exempt
//     because they are safe, which is also why the SSE stream is a plain GET.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/store"
)

// Provisioner attaches a working directory to a session. *provision.Service
// satisfies it; the interface lives here so this package keeps depending only
// on contracts.
type Provisioner interface {
	// EnsureWorktree makes sure the session has a worktree and returns it. It
	// is expected to be idempotent and to persist the resulting worktree_id on
	// the session itself.
	EnsureWorktree(ctx context.Context, userID string, project *contracts.Project, sess *contracts.Session) (*contracts.Worktree, error)
}

// TermManager is the subset of term.Manager the HTTP layer uses. Declaring it
// here keeps httpapi from importing internal/term and keeps tests able to
// substitute a fake.
type TermManager interface {
	Open(ctx context.Context, sandboxID, tabID, shell string) (*TermSession, error)
	Write(tabID string, p []byte) error
	Resize(tabID string, cols, rows int) error
	Close(tabID string) error
	Read(tabID string, fromOffset int64) ([]byte, int64, bool)
}

// TermSession is the subset of a terminal session the socket needs. The
// adapter in cmd/piforged converts term.Info into it so this package never has
// to import the terminal implementation.
type TermSession struct {
	TabID string
	Shell string
	PID   int
	Start int64
	End   int64
}

// Deps is everything the router needs. The zero value is not usable: Store and
// Auth are required and NewRouter panics without them (a wiring bug at
// startup, not a request-time condition).
//
// Runtime, Workspace and Sandbox are interfaces from internal/contracts, so
// this package never imports internal/pi, internal/gitx, internal/fsx or
// internal/sandbox — those are owned by other packages and can be swapped for
// fakes in tests.
type Deps struct {
	Store     *store.Store
	Auth      *auth.Authenticator
	Runtime   contracts.AgentRuntime
	Workspace contracts.Workspace
	// DefaultProvider is the provider a session falls back to when it pins no
	// model. The runtime resolves it the same way, so the explanation of a failed
	// run has to use the same fallback or it reports the wrong provider.
	DefaultProvider string
	Sandbox         contracts.SandboxProvider
	// SkillsDir is where uploaded skill bundles live, one directory per skill.
	// Empty means uploads are refused with a typed error.
	SkillsDir string
	// WebUpstream is the Next.js server to forward page requests to. It is how a
	// container serves one port: the API stays here, the UI is proxied. Empty
	// means "API only", which is what the e2e suite and local development use.
	WebUpstream string
	// Secrets seals and opens provider credentials. When nil, creating a
	// provider with a key is refused rather than storing it in the clear.
	Secrets *crypto.Box
	// LLMGateway is mounted at /gw/llm/. It authenticates with its own signed
	// token, so it is deliberately outside the cookie auth chain.
	LLMGateway http.Handler
	// Browser owns one Chrome per sandbox. Nil disables the browser panel with
	// a typed 422 rather than a hanging socket.
	Browser *browser.Manager
	// Term backs the terminal panel. When nil the PTY endpoint returns
	// sandbox_unavailable instead of opening a socket it cannot serve.
	Term TermManager
	// Provisioner attaches a working directory to a session. When nil,
	// sessions simply have no worktree and the workspace endpoints report
	// sandbox_unavailable.
	Provisioner Provisioner
	// Now defaults to time.Now.
	Now func() time.Time
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// server holds the resolved dependencies plus the SSE tuning knobs.
type server struct {
	deps Deps
	log  *slog.Logger
	now  func() time.Time
	hub  *hub
	// ssePoll is how often an open event stream re-checks the tail of the log.
	// A 200ms poll is indistinguishable from push for chat rendering and keeps
	// the SSE path free of a fan-out goroutine per client.
	ssePoll time.Duration
	// sseKeepalive is the comment interval; proxies drop idle connections at
	// 30-60s, and the PRD asks for 15s.
	sseKeepalive time.Duration
}

// NewRouter builds the complete HTTP handler, including the recover middleware.
func NewRouter(d Deps) http.Handler {
	s := newServer(d)
	return s.recoverer(s.routes())
}

// newServer normalises Deps and is what tests use to tune SSE timings.
func newServer(d Deps) *server {
	if d.Store == nil {
		panic("httpapi: Deps.Store is required")
	}
	if d.Auth == nil {
		panic("httpapi: Deps.Auth is required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &server{
		deps:         d,
		log:          d.Logger,
		now:          d.Now,
		hub:          newHub(d.Store, d.Runtime, d.Logger),
		ssePoll:      200 * time.Millisecond,
		sseKeepalive: 15 * time.Second,
	}
}

// routes registers every endpoint. Route-level middleware is attached here so
// that the auth requirement is visible next to the path it protects.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Liveness. No auth on purpose: the load balancer has no session.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Auth. Login is the only unauthenticated mutation (there is no session to
	// protect yet, and no CSRF cookie to echo).
	mux.HandleFunc("POST /v1/auth/login", s.handleLogin)
	mux.Handle("POST /v1/auth/logout", s.authed(s.handleLogout))
	mux.Handle("GET /v1/auth/me", s.authed(s.handleMe))

	// Projects.
	mux.Handle("GET /v1/projects", s.authed(s.handleListProjects))
	mux.Handle("POST /v1/projects", s.mutatingBearer(s.handleCreateProject))
	mux.Handle("GET /v1/projects/{id}", s.authed(s.handleGetProject))
	mux.Handle("POST /v1/projects/{id}/sessions", s.mutatingBearer(s.handleCreateSession))

	// Sessions.
	mux.Handle("GET /v1/sessions", s.authed(s.handleListSessions))
	mux.Handle("GET /v1/sessions/compare", s.authed(s.handleCompareSessions))
	mux.Handle("GET /v1/sessions/{id}", s.authed(s.handleGetSession))
	mux.Handle("PATCH /v1/sessions/{id}", s.mutatingBearer(s.handlePatchSession))
	mux.Handle("GET /v1/sessions/{id}/status", s.authed(s.handleSessionStatus))
	mux.Handle("DELETE /v1/sessions/{id}", s.mutatingBearer(s.handleDeleteSession))
	mux.Handle("GET /v1/sessions/{id}/export", s.authed(s.handleExportSession))
	mux.Handle("GET /v1/sessions/{id}/stats", s.authed(s.handleSessionStats))
	mux.Handle("GET /v1/sessions/{id}/logs", s.authed(s.handleSessionLogs))
	mux.Handle("GET /v1/sessions/{id}/pty", s.authed(s.handleSessionPTY))
	mux.Handle("GET /v1/sessions/{id}/tooling", s.authed(s.handleSessionTooling))
	mux.Handle("GET /v1/sessions/{id}/browser", s.authed(s.handleSessionBrowser))
	mux.Handle("GET /v1/sessions/{id}/browser/tabs", s.authed(s.handleListBrowserTabs))
	mux.Handle("POST /v1/sessions/{id}/browser/tabs", s.mutatingBearer(s.handleOpenBrowserTab))
	mux.Handle("DELETE /v1/sessions/{id}/browser/tabs/{tab}", s.mutatingBearer(s.handleCloseBrowserTab))
	mux.Handle("POST /v1/sessions/{id}/browser/screenshot", s.mutatingBearer(s.handleBrowserScreenshot))
	mux.Handle("POST /v1/sessions/{id}/messages", s.mutatingBearer(s.handlePostMessage))
	mux.Handle("POST /v1/sessions/{id}/interrupt", s.mutatingBearer(s.handleInterrupt))
	mux.Handle("GET /v1/sessions/{id}/events", s.authed(s.handleEvents))
	mux.Handle("GET /v1/sessions/{id}/entries", s.authed(s.handleEntries))
	mux.Handle("GET /v1/sessions/{id}/tree", s.authed(s.handleTree))
	mux.Handle("GET /v1/sessions/{id}/fork-points", s.authed(s.handleForkPoints))
	mux.Handle("POST /v1/sessions/{id}/fork", s.mutatingBearer(s.handleFork))
	mux.Handle("POST /v1/sessions/{id}/clone", s.mutatingBearer(s.handleCloneSession))
	mux.Handle("POST /v1/sessions/{id}/adopt", s.mutatingBearer(s.handleAdoptBranch))
	mux.Handle("POST /v1/sessions/{id}/sidecar", s.mutatingBearer(s.handleSidecar))
	mux.Handle("POST /v1/sessions/{id}/promote", s.mutatingBearer(s.handlePromoteSidecar))
	mux.Handle("POST /v1/sessions/{id}/compact", s.mutatingBearer(s.handleCompact))

	// Workspace: files, search, git. All delegate to Deps.Workspace.
	mux.Handle("GET /v1/sessions/{id}/files/tree", s.authed(s.handleFileTree))
	mux.Handle("GET /v1/sessions/{id}/files", s.authed(s.handleReadFile))
	mux.Handle("PUT /v1/sessions/{id}/files", s.mutatingBearer(s.handleWriteFile))
	mux.Handle("DELETE /v1/sessions/{id}/files", s.mutatingBearer(s.handleDeleteFile))
	mux.Handle("GET /v1/sessions/{id}/search", s.authed(s.handleSearch))
	mux.Handle("GET /v1/sessions/{id}/git/status", s.authed(s.handleGitStatus))
	mux.Handle("GET /v1/sessions/{id}/git/diff", s.authed(s.handleGitDiff))
	mux.Handle("GET /v1/sessions/{id}/git/log", s.authed(s.handleGitLog))
	mux.Handle("POST /v1/sessions/{id}/git/stage", s.mutatingBearer(s.handleGitStage))
	mux.Handle("POST /v1/sessions/{id}/git/unstage", s.mutatingBearer(s.handleGitUnstage))
	mux.Handle("POST /v1/sessions/{id}/git/commit", s.mutatingBearer(s.handleGitCommit))
	mux.Handle("POST /v1/sessions/{id}/git/branch", s.mutatingBearer(s.handleGitBranch))
	mux.Handle("POST /v1/sessions/{id}/git/checkout", s.mutatingBearer(s.handleGitCheckout))

	// Providers.
	mux.Handle("GET /v1/api-keys", s.authed(s.handleListAPIKeys))
	mux.Handle("POST /v1/api-keys", s.mutating(s.handleCreateAPIKey))
	mux.Handle("DELETE /v1/api-keys/{id}", s.mutating(s.handleRevokeAPIKey))
	mux.Handle("GET /v1/providers", s.authed(s.handleListProviders))
	mux.Handle("POST /v1/providers", s.mutatingBearer(s.handleCreateProvider))
	mux.Handle("PATCH /v1/providers/{id}", s.mutatingBearer(s.handleUpdateProvider))
	mux.Handle("DELETE /v1/providers/{id}", s.mutatingBearer(s.handleDeleteProvider))
	mux.Handle("POST /v1/providers/{id}/test", s.mutatingBearer(s.handleTestProvider))

	// Skills (PRD §5.8).
	mux.Handle("GET /v1/skills", s.authed(s.handleListSkills))
	mux.Handle("POST /v1/skills", s.mutatingBearer(s.handleCreateSkill))
	mux.Handle("DELETE /v1/skills/{id}", s.mutatingBearer(s.handleDeleteSkill))
	mux.Handle("PUT /v1/skills/{id}/bundle", s.mutatingBearer(s.handleUploadSkillBundle))
	// The bundle is content, not configuration: reading it back lets the editor
	// show what is actually on disk.
	mux.Handle("GET /v1/skills/{id}/bundle", s.authed(s.handleReadSkillBundle))

	// MCP servers (PRD §5.9).
	mux.Handle("GET /v1/mcp/servers", s.authed(s.handleListMCPServers))
	mux.Handle("POST /v1/mcp/servers", s.mutatingBearer(s.handleCreateMCPServer))
	mux.Handle("DELETE /v1/mcp/servers/{id}", s.mutatingBearer(s.handleDeleteMCPServer))

	// Admin.
	mux.Handle("GET /v1/admin/status", s.authed(s.handleAdminStatus))
	mux.Handle("GET /v1/features", s.authed(s.handleFeatures))
	mux.Handle("GET /v1/admin/users/{id}/features", s.authed(s.handleAdminUserFeatures))
	mux.Handle("POST /v1/admin/users/{id}/features", s.mutatingBearer(s.handleAdminSetUserFeature))
	mux.Handle("POST /v1/admin/support-access", s.mutatingBearer(s.handleCreateSupportAccess))
	mux.Handle("GET /v1/admin/support-access/{id}/transcript", s.authed(s.handleSupportTranscript))
	mux.Handle("GET /v1/support-notices", s.authed(s.handleSupportNotices))
	mux.Handle("POST /v1/support-notices/{id}/seen", s.mutatingBearer(s.handleSupportNoticeSeen))
	mux.Handle("GET /v1/admin/providers", s.authed(s.handleAdminListProviders))
	mux.Handle("POST /v1/admin/providers", s.mutatingBearer(s.handleAdminUpsertProvider))
	mux.Handle("DELETE /v1/admin/providers/{id}", s.mutatingBearer(s.handleAdminDeleteProvider))
	mux.Handle("GET /v1/admin/users", s.authed(s.handleAdminUsers))
	mux.Handle("POST /v1/admin/users", s.mutatingBearer(s.handleAdminCreateUser))
	mux.Handle("PATCH /v1/admin/users/{id}", s.mutatingBearer(s.handleAdminUpdateUser))

	// Anything else is a JSON 404 rather than Go's text/plain default, so the
	// client's error parser always has the same shape to read.
	// The model gateway authenticates with a signed per-session token rather
	// than the session cookie, so it bypasses the middleware chain entirely.
	if s.deps.LLMGateway != nil {
		mux.Handle("/gw/llm/", s.deps.LLMGateway)
	}

	if strings.TrimSpace(s.deps.WebUpstream) != "" {
		web, werr := s.newWebProxy(s.deps.WebUpstream)
		if werr != nil {
			panic("httpapi: invalid WebUpstream: " + werr.Error())
		}
		mux.HandleFunc("/", s.handleWebOrNotFound(web))
	} else {
		mux.HandleFunc("/", s.handleNotFound)
	}
	return mux
}

// authed wraps a handler that needs a resolved user.
//
// An `Authorization: Bearer pfk_...` header is accepted too: a script driving the API
// is the point of FR-13.9, and it carries no cookie for CSRF to protect.
func (s *server) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, presented := s.bearerUser(r); presented {
			if user == nil {
				s.fail(w, r, apiErr(contracts.ErrUnauthenticated, "这个 API key 无效或已被吊销"))
				return
			}
			h(w, r.WithContext(auth.WithUser(r.Context(), user)))
			return
		}
		s.deps.Auth.RequireUser(h).ServeHTTP(w, r)
	})
}

// mutatingBearer is mutating, except that a bearer token skips CSRF: there is no
// cookie for an attacker's page to ride.
func (s *server) mutatingBearer(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, presented := s.bearerUser(r); presented {
			if user == nil {
				s.fail(w, r, apiErr(contracts.ErrUnauthenticated, "这个 API key 无效或已被吊销"))
				return
			}
			h(w, r.WithContext(auth.WithUser(r.Context(), user)))
			return
		}
		s.deps.Auth.RequireUser(s.deps.Auth.RequireCSRF(h)).ServeHTTP(w, r)
	})
}

// mutating wraps a handler that changes state: user plus double-submit CSRF.
func (s *server) mutating(h http.HandlerFunc) http.Handler {
	return s.deps.Auth.RequireUser(s.deps.Auth.RequireCSRF(h))
}

// recoverer turns a handler panic into 500 without tearing down the server.
// It must not append a second response, which is why the wrapper tracks
// whether the header went out already (an SSE stream, for instance).
func (s *server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &trackedWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				// net/http's own sentinel for "the client went away mid-response".
				// ReverseProxy and the server raise it deliberately, so it is not
				// a bug: log nothing, answer nothing, and let the connection die.
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					return
				}
				s.log.ErrorContext(r.Context(), "handler panic",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())))
				if !tw.wrote {
					writeErr(tw, apiErr(contracts.ErrInternal, "internal error"))
				}
			}
		}()
		next.ServeHTTP(tw, r)
	})
}

// trackedWriter remembers whether a response has started so a panic recovery
// does not corrupt the stream, and forwards Unwrap so
// http.ResponseController can still reach Flush on the real writer.
type trackedWriter struct {
	http.ResponseWriter
	wrote  bool
	status int
}

func (t *trackedWriter) WriteHeader(code int) {
	if !t.wrote {
		t.wrote = true
		t.status = code
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackedWriter) Write(b []byte) (int, error) {
	if !t.wrote {
		t.wrote = true
		t.status = http.StatusOK
	}
	return t.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController find Flush/SetWriteDeadline on the
// underlying writer (SSE depends on it).
func (t *trackedWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// ---------------------------------------------------------------------------
// Small request helpers
// ---------------------------------------------------------------------------

// pathID returns the {id} wildcard.
func pathID(r *http.Request) string { return r.PathValue("id") }

// queryInt reads an integer query parameter, returning def when absent or
// unparsable. Query parameters are user input, so a bad value is a default
// rather than an error — pagination should not 400 because of "?limit=abc".
func queryInt(r *http.Request, name string, def int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// queryInt64 reads an int64 query parameter.
func queryInt64(r *http.Request, name string, def int64) int64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// queryBool reports whether a query parameter is set to a truthy value.
func queryBool(r *http.Request, name string) bool {
	switch r.URL.Query().Get(name) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
