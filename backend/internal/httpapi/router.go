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
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/bootstrap"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/browserlease"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/diskquota"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/reconcile"
	"github.com/piforge/piforge/backend/internal/runtimedirs"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/tenantegress"
	"github.com/piforge/piforge/backend/internal/tenantlimits"
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
	// Info reports why a tab stopped, if it stopped abnormally. The relay uses
	// it to send an error frame instead of closing as if the shell had exited.
	Info(tabID string) (TermSession, bool)
	// CloseSandbox closes every tab opened against one sandbox key and reports
	// how many were closed. The PTY handler opens with the session's worktree
	// id as that key (pty_handlers.go), so callers pass a worktree id.
	CloseSandbox(sandboxID string) int
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
	// Err is set when the terminal stopped abnormally; empty means it exited.
	Err string
	// Retrying says the sandbox shell is being retried, and RetrySeq changes on
	// every transition so the relay reports each one once.
	Retrying bool
	RetrySeq int
	RetryWhy string
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
	// ParallelLimit caps how many branches of one session may run at once (FR-3.3).
	// cmd/piforged fills it from -parallel-limit / PIFORGE_PARALLEL_LIMIT and
	// refuses to start outside 1..MaxParallelLimit; the zero fallback only serves
	// hand-built test dependencies.
	ParallelLimit int
	// SandboxDiskQuota is the per-sandbox disk budget in bytes (FR-5.8), from
	// -sandbox-disk-quota / PIFORGE_SANDBOX_DISK_QUOTA. Zero disables enforcement
	// (the admin report still measures). A run whose sandbox root is over it is
	// refused before an agent starts.
	SandboxDiskQuota int64
	// TenantLimits is the per-tenant quota policy (internal/tenantlimits). The
	// admin status page reports each tenant's effective limits next to the
	// sandboxes it is using, so "who is at their ceiling" is answerable from the
	// product rather than from the deployment's configuration file. Nil means this
	// deployment configured no policy, which the page says out loud.
	TenantLimits *tenantlimits.Policy
	// SandboxNetworkMode is "shared" or "tenant": how sandbox network membership
	// is decided. It is reported because it is the difference between "tenants
	// cannot reach each other" and "tenants are separated by an API key", and an
	// operator has to be able to see which one this deployment runs.
	SandboxNetworkMode string
	// TenantEgress is the per-tenant outbound-network policy (internal/tenantegress).
	// It is reported next to the quota because "who can reach the internet" is the
	// other half of "what may this account do"; nil means the deployment
	// configured none, which is open and is named as such on the page.
	TenantEgress *tenantegress.Policy
	// SandboxEgressApplied is false for a placement with no container (-sandbox
	// local), where the mode is reported but is not a boundary.
	SandboxEgressApplied bool
	// DiskQuotaReportTTL is how long the admin screen may reuse a measurement.
	// Zero uses diskquota.DefaultReportTTL.
	DiskQuotaReportTTL time.Duration
	// MeasureSandbox measures one sandbox's workspace from *inside* the sandbox.
	//
	// It exists because a detached workspace is not a directory this process can
	// open: the reported root is /workspace, a path that only exists in the
	// container. Without this the admin figure was a walk of a nonexistent host
	// directory — 0 bytes, `over=false`, while the sandbox held 3 GiB — and the
	// run boundary had the same blind spot. Nil keeps the host walk, which is
	// right for a shared-workspace deployment and for hand-built test servers.
	MeasureSandbox diskquota.Measurer
	// DiskWatchInterval is how often the periodic disk check measures a sandbox.
	// Zero uses the package default (5s). Only used when the quota is enabled and
	// MeasureSandbox is set.
	DiskWatchInterval time.Duration
	// StopAgent kills one session's agent process *inside its sandbox*.
	//
	// Runtime.Stop is not enough for a container placement: the agent is owned by
	// a detached supervisor inside the sandbox that deliberately survives a
	// closed control-plane stream (that is how a restarted control plane
	// reattaches to the same process). So "stop the agent" has to reach the
	// sandbox, and the place that decides it is the disk watchdog: an agent
	// writing past the quota has to stop writing, not just stop being watched.
	// Nil leaves the watchdog with the runtime stop alone, which is correct for
	// the local placement (the agent is this process's child).
	StopAgent func(ctx context.Context, sandboxID, sessionID string) error
	// ProbeSandbox answers "can this sandbox still run a command" with one
	// trivial command through the sandbox's own API, bounded to a few seconds.
	//
	// It is the liveness signal the sandbox watchdog needs
	// (sandbox_watch.go): a fork storm that exhausts the pids cgroup leaves the
	// container unable to spawn anything, so the only honest probe is "fork and
	// run something". Nil disables the watchdog and the recover endpoint, with a
	// log line saying so — a watchdog that cannot probe must not pretend.
	ProbeSandbox func(ctx context.Context, sandboxID string) error
	// SandboxWatchInterval is how often the watchdog probes one sandbox. Zero
	// uses the package default (10s). Only used when ProbeSandbox is set.
	SandboxWatchInterval time.Duration
	// SandboxProbeTimeout bounds one probe as the watchdog applies it (it also
	// bounds the sandbox's endpoint resolution). Zero uses 5s.
	SandboxProbeTimeout time.Duration
	// AgentIsolated reports that this deployment runs every agent inside a
	// sandbox container (a docker or runner placement; stage 4 of
	// docs/EXECUTION-BOUNDARY.md made that the only placement for them). It is a
	// fact about the deployment, not a switch: the panels need it to answer "can
	// this MCP server reach the agent at all?", and cmd/piforged derives it from
	// the provider.
	AgentIsolated bool

	// DefaultProvider is the provider a session falls back to when it pins no
	// model. The runtime resolves it the same way, so the explanation of a failed
	// run has to use the same fallback or it reports the wrong provider.
	DefaultProvider string
	Sandbox         contracts.SandboxProvider
	// SkillsDir is where uploaded skill bundles live, one directory per skill.
	// Empty means uploads are refused with a typed error.
	SkillsDir string
	// SkillMaxBytes caps the total bytes one skill import may expand on disk
	// (FR-5.8), from -skill-max-bytes / PIFORGE_SKILL_MAX_BYTES. Zero uses
	// DefaultSkillMaxBytes; an archive that would exceed it is refused whole,
	// before a skill row exists.
	SkillMaxBytes int64
	// SnapshotDir is where a session's frozen skill snapshots live
	// (<dataDir>/skill-snapshots). Deleting a session or a project removes the
	// matching directories; empty means this deployment never creates one (a
	// copy mechanism writes no snapshot), so there is nothing to remove.
	SnapshotDir string
	// AgentDir is where per-session agent configuration lives
	// (<dataDir>/pi-agents/<session>/models.json and mcp.json). Deleting a
	// session or a project removes the matching directory; empty means this
	// deployment was not wired for the cascade (hand-built test dependencies).
	AgentDir string
	// SandboxSnapshotDir is the idle reaper's manifest root
	// (<dataDir>/snapshots/<sandbox>/<timestamp>/manifest.json). A project
	// delete removes the directories of the sandboxes it owned.
	SandboxSnapshotDir string
	// DataDir is the deployment's data directory. It is what
	// GET /v1/admin/status measures per runtime directory; empty reports an
	// empty storage block rather than scanning the process's working directory.
	DataDir string
	// ArtifactsDir is the control plane's own artifacts root (for example
	// <dataDir>/artifacts). Browser screenshots are written under
	// <ArtifactsDir>/<session>/screenshots, never into the checkout: a capture
	// must not appear as the user's untracked work. Empty falls back to a
	// directory under the OS temp dir so hand-built test dependencies still
	// work, but a deployment always sets it.
	ArtifactsDir string
	// ScreenshotCapture overrides the sandbox browser for a screenshot. Nil is
	// the normal path (Acquire + ScreenshotTab); tests use it to exercise the
	// handler's persistence and serving without launching Chrome.
	ScreenshotCapture func(ctx context.Context, sandboxID, profileRoot, tabID string) ([]byte, error)
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
	// SetupDefaults is the prefill a deployment documents through
	// PIFORGE_BOOTSTRAP_PROVIDER. It is non-secret by construction (name,
	// endpoint, model) and empty unless the operator set the variable.
	SetupDefaults bootstrap.Defaults
	// BrowserTakeoverTTL is how long a takeover lease lives without a
	// heartbeat (FR-11.3). Zero uses the default. A closed panel stops
	// heartbeating, so this is also how long a crashed viewer can keep the
	// browser before somebody else may take it.
	BrowserTakeoverTTL time.Duration
	// Reconciler repairs a session that claims to be running while no agent
	// process serves it. The delete guard calls it before trusting a "running"
	// row, so a project pinned by a crashed run is deletable in the same
	// request. Nil keeps the old behaviour (the persisted row alone decides);
	// cmd/piforged always wires one.
	Reconciler *reconcile.Reconciler
	// Probe is the liveness authority the deployment trusts. ReattachLiveSessions
	// asks it before reconnecting to a session, so "running in the database" is
	// not on its own a reason to spawn or attach to an agent.
	Probe contracts.LivenessProbe
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
	// taps watches the terminal for typed test commands (FR-12.5). One per tab,
	// kept across socket reconnects that the PTY itself survives.
	tapsMu sync.Mutex
	taps   map[string]*testTap
	// artifacts reads and writes ArtifactsDir. It is deliberately not the
	// session's Workspace: in a remote-placement deployment that interface
	// speaks to the sandbox, and artifacts belong to the control plane.
	artifacts *fsx.Service
	// leases makes takeover exclusive per sandbox (FR-11.3). In memory: a
	// restart means nobody is holding the wheel, which is the honest state.
	leases *browserlease.Manager
	// storage caches the per-directory usage scan for the admin page. The page
	// polls every 15s; walking every sandbox on each poll would make an idle
	// screen a disk scan. The scan is cheap enough once a minute.
	storageMu sync.Mutex
	storageAt time.Time
	storage   []runtimedirs.Usage
	// diskQuota measures sandboxes against the FR-5.8 budget and caches the
	// reports the admin screen reads.
	diskQuota *diskquota.Guardian
	// diskMu guards diskWatchState, the per-sandbox strike counter the periodic
	// disk watchdog keeps (sandbox_disk_watch.go).
	diskMu         sync.Mutex
	diskWatchState map[string]diskWatch
	// probeMu guards the liveness watchdog's bookkeeping (sandbox_watch.go):
	// the last verdict per sandbox, the consecutive-failure counter, and the set
	// of sandboxes a recovery is running for. It is one mutex because the three
	// are read together by the status endpoint.
	probeMu       sync.Mutex
	probeState    map[string]sandboxProbeVerdict
	probeStrikes  map[string]int
	recovering    map[string]bool
}

// NewRouter builds the complete HTTP handler, including the recover middleware.
func NewRouter(d Deps) http.Handler {
	return NewServer(d).Handler()
}

// Server is the HTTP surface plus the operations that are not request-shaped.
//
// It exists for the boot-time passes that need the same wiring the handlers use
// — today that is ReattachLiveSessions, which reconnects this process to agents
// that outlived the previous one (docs/EXECUTION-BOUNDARY.md stage 3). Returning
// only an http.Handler would leave those passes to rebuild the store lookup, the
// session spec and the hub's single-subscription rule somewhere else.
type Server struct {
	handler http.Handler
	s       *server
}

// NewServer builds the handler and keeps the server for the boot-time passes.
func NewServer(d Deps) *Server {
	s := newServer(d)
	return &Server{handler: s.recoverer(s.routes()), s: s}
}

// Handler returns the http.Handler to serve.
func (sv *Server) Handler() http.Handler { return sv.handler }

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
	s := &server{
		deps:         d,
		log:          d.Logger,
		now:          d.Now,
		hub:          newHub(d.Store, d.Runtime, d.Logger),
		ssePoll:      200 * time.Millisecond,
		sseKeepalive: 15 * time.Second,
		// No git backend: artifacts are plain files, and this service must not
		// pretend a checkout exists under the data directory.
		artifacts: fsx.New(fsx.Config{}),
	}
	// FR-5.8: the budget is configuration (cmd/piforged resolves the flag), and
	// the guardian is the one place that measures. A test server leaves it at the
	// zero value, which is disabled.
	s.diskQuota = diskquota.New(diskquota.Config{
		QuotaBytes: d.SandboxDiskQuota,
		ReportTTL:  d.DiskQuotaReportTTL,
		Now:        s.now,
	})
	s.diskWatchState = map[string]diskWatch{}
	s.probeState = map[string]sandboxProbeVerdict{}
	s.probeStrikes = map[string]int{}
	s.recovering = map[string]bool{}
	s.leases = browserlease.New(d.BrowserTakeoverTTL, s.now)
	return s
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
	// First-run state: one boolean instead of every screen re-deriving it from
	// the provider list (QA-16's fresh volume answered 400 no_model everywhere).
	mux.Handle("GET /v1/setup", s.authed(s.handleSetup))

	// Projects.
	mux.Handle("GET /v1/projects", s.authed(s.handleListProjects))
	mux.Handle("POST /v1/projects", s.mutatingBearer(s.handleCreateProject))
	mux.Handle("GET /v1/projects/{id}", s.authed(s.handleGetProject))
	mux.Handle("PATCH /v1/projects/{id}", s.mutatingBearer(s.handlePatchProject))
	mux.Handle("DELETE /v1/projects/{id}", s.mutatingBearer(s.handleDeleteProject))
	mux.Handle("POST /v1/projects/{id}/sessions", s.mutatingBearer(s.handleCreateSession))

	// Sessions.
	mux.Handle("GET /v1/sessions", s.authed(s.handleListSessions))
	mux.Handle("GET /v1/sessions/compare", s.authed(s.handleCompareSessions))
	mux.Handle("GET /v1/sessions/{id}", s.authed(s.handleGetSession))
	mux.Handle("PATCH /v1/sessions/{id}", s.mutatingBearer(s.handlePatchSession))
	mux.Handle("GET /v1/sessions/{id}/status", s.authed(s.handleSessionStatus))
	// What happened and what survived after a run ended without a result
	// (stopped / restarted / agent gone). 继续 composes its message through
	// POST /messages with continue_interrupted.
	mux.Handle("GET /v1/sessions/{id}/recovery", s.authed(s.handleSessionRecovery))
	mux.Handle("DELETE /v1/sessions/{id}", s.mutatingBearer(s.handleDeleteSession))
	mux.Handle("GET /v1/sessions/{id}/export", s.authed(s.handleExportSession))
	mux.Handle("GET /v1/sessions/{id}/stats", s.authed(s.handleSessionStats))
	mux.Handle("GET /v1/sessions/{id}/test-runs", s.authed(s.handleTestRuns))
	mux.Handle("GET /v1/sessions/{id}/logs", s.authed(s.handleSessionLogs))
	mux.Handle("GET /v1/sessions/{id}/pty", s.authed(s.handleSessionPTY))
	mux.Handle("GET /v1/sessions/{id}/tooling", s.authed(s.handleSessionTooling))
	mux.Handle("GET /v1/sessions/{id}/skills", s.authed(s.handleSessionSkills))
	mux.Handle("GET /v1/sessions/{id}/browser", s.authed(s.handleSessionBrowser))
	mux.Handle("GET /v1/sessions/{id}/browser/tabs", s.authed(s.handleListBrowserTabs))
	mux.Handle("POST /v1/sessions/{id}/browser/tabs", s.mutatingBearer(s.handleOpenBrowserTab))
	mux.Handle("DELETE /v1/sessions/{id}/browser/tabs/{tab}", s.mutatingBearer(s.handleCloseBrowserTab))
	mux.Handle("POST /v1/sessions/{id}/browser/screenshot", s.mutatingBearer(s.handleBrowserScreenshot))
	mux.Handle("GET /v1/sessions/{id}/browser/mode", s.authed(s.handleBrowserMode))
	mux.Handle("POST /v1/sessions/{id}/browser/mode", s.mutatingBearer(s.handleBrowserMode))
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
	// Control-plane artifacts (browser screenshots live outside the checkout).
	mux.Handle("GET /v1/sessions/{id}/artifacts", s.authed(s.handleArtifactFile))
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
	mux.Handle("GET /v1/sessions/{id}/sandbox", s.authed(s.handleSandboxState))
	mux.Handle("GET /v1/sessions/{id}/sandbox/snapshots", s.authed(s.handleSandboxSnapshots))
	mux.Handle("POST /v1/sessions/{id}/sandbox/pause", s.mutatingBearer(s.handleSandboxPause))
	mux.Handle("POST /v1/sessions/{id}/sandbox/resume", s.mutatingBearer(s.handleSandboxResume))
	// One-click repair for a sandbox that stopped answering (sandbox_watch.go).
	// It is the button behind 「沙箱无响应，正在恢复」, and it runs the same
	// recovery the watchdog does, synchronously, so the click has an answer.
	mux.Handle("POST /v1/sessions/{id}/sandbox/recover", s.mutatingBearer(s.handleSandboxRecover))
	mux.Handle("POST /v1/sessions/{id}/git/hunk", s.mutatingBearer(s.handleGitHunk))
	mux.Handle("POST /v1/sessions/{id}/git/discard", s.mutatingBearer(s.handleGitDiscardFile))
	mux.Handle("GET /v1/sessions/{id}/git/remote", s.authed(s.handleGitRemote))
	mux.Handle("POST /v1/sessions/{id}/git/push", s.mutatingBearer(s.handleGitPush))
	mux.Handle("POST /v1/sessions/{id}/git/pull", s.mutatingBearer(s.handleGitPull))
	// Opening a pull request: preview what would be sent, then push + create
	// (FR-12.6).
	mux.Handle("GET /v1/sessions/{id}/git/pr", s.authed(s.handlePRPreview))
	mux.Handle("POST /v1/sessions/{id}/git/pr", s.mutatingBearer(s.handleCreatePR))

	// Where this project opens pull requests. The token is write-only.
	mux.Handle("PUT /v1/projects/{id}/forge", s.mutatingBearer(s.handleSetProjectForge))

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
	mux.Handle("POST /v1/skills/import", s.mutatingBearer(s.handleImportSkills))
	// A zip upload is the other half of FR-14.4: a collection that is not a git
	// repository still has to be installable in one action.
	mux.Handle("POST /v1/skills/import/zip", s.mutatingBearer(s.handleImportSkillsZip))
	mux.Handle("PATCH /v1/skills/{id}", s.mutatingBearer(s.handlePatchSkill))
	mux.Handle("DELETE /v1/skills/{id}", s.mutatingBearer(s.handleDeleteSkill))
	mux.Handle("PUT /v1/skills/{id}/bundle", s.mutatingBearer(s.handleUploadSkillBundle))
	// The bundle is content, not configuration: reading it back lets the editor
	// show what is actually on disk.
	mux.Handle("GET /v1/skills/{id}/bundle", s.authed(s.handleReadSkillBundle))

	// MCP servers (PRD §5.9).
	mux.Handle("GET /v1/mcp/servers", s.authed(s.handleListMCPServers))
	mux.Handle("POST /v1/mcp/servers", s.mutatingBearer(s.handleCreateMCPServer))
	mux.Handle("DELETE /v1/mcp/servers/{id}", s.mutatingBearer(s.handleDeleteMCPServer))
	// Stage 3's revocable credential. The sandbox holds /gw/mcp/<token> instead
	// of a user's MCP credential, and this is how the deployment takes it away
	// (docs/EXECUTION-BOUNDARY.md §4).
	mux.Handle("DELETE /v1/sessions/{id}/mcp-token", s.mutatingBearer(s.handleRevokeMCPTokens))

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
	mux.Handle("PATCH /v1/admin/providers/{id}", s.mutatingBearer(s.handleAdminUpdateProvider))
	mux.Handle("DELETE /v1/admin/providers/{id}", s.mutatingBearer(s.handleAdminDeleteProvider))
	mux.Handle("GET /v1/admin/users", s.authed(s.handleAdminUsers))
	mux.Handle("POST /v1/admin/users", s.mutatingBearer(s.handleAdminCreateUser))
	mux.Handle("PATCH /v1/admin/users/{id}", s.mutatingBearer(s.handleAdminUpdateUser))
	// The audit trail: tool_calls read across tenants, for an operator. It is the
	// one cross-tenant read besides the user list, and it is admin-only for the
	// same reason.
	mux.Handle("GET /v1/admin/audit", s.authed(s.handleAdminAudit))

	// Anything else is a JSON 404 rather than Go's text/plain default, so the
	// client's error parser always has the same shape to read.
	// The model gateway authenticates with a signed per-session token rather
	// than the session cookie, so it bypasses the middleware chain entirely.
	if s.deps.LLMGateway != nil {
		mux.Handle("/gw/llm/", s.deps.LLMGateway)
	}
	// The MCP broker. Same shape as the model gateway: the path token is the
	// credential, so it sits outside the cookie chain. Unlike the gateway it has no
	// dependency to be nil, because the store it needs is required anyway.
	mux.Handle(MCPGatewayPrefix, http.HandlerFunc(s.handleMCPGateway))

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
