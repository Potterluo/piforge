// Package browser drives the single Chrome instance a sandbox owns.
//
// The product rule (PRD §7.7) is one browser per sandbox, with its profile
// directory inside the workspace: the agent reaches it through Playwright MCP
// over CDP, and a human watches the same instance through a screencast. Sharing
// a Chrome between tenants would mean sharing cookies, which is the one thing a
// multi-tenant agent platform must never do.
//
// This file owns the process lifecycle and the small vocabulary of actions the
// UI can perform. The protocol client lives in cdp.go.
package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/piforge/piforge/backend/internal/isolate"
	"time"
)

// ErrNotConfigured means no Chrome binary was found, so the feature is
// unavailable rather than broken. The HTTP layer maps it to a 422 with a reason
// instead of a 500.
var ErrNotConfigured = errors.New("browser: no chrome binary is configured")

// ErrClosed means the manager was shut down before the browser could be handed
// out. It is a sentinel so a caller can tell "we are stopping" from "this
// sandbox would not start".
var ErrClosed = errors.New("browser: manager is closed")

// LaunchStage says how far a launch got before it failed. The difference
// matters to whoever reads the log or the panel: a missing binary is a
// configuration problem, devtools not answering is a machine problem, and a
// locked profile is somebody else's browser.
type LaunchStage string

const (
	// StageProfile is "the profile directory or its lock".
	StageProfile LaunchStage = "profile"
	// StageStart is "the process never started".
	StageStart LaunchStage = "start"
	// StageDevTools is "the process started but its devtools endpoint never
	// answered".
	StageDevTools LaunchStage = "devtools"
	// StageAttach is "devtools answered but a page session could not be made".
	StageAttach LaunchStage = "attach"
	// StageExited is "the browser quit before the session could be used".
	StageExited LaunchStage = "exited"
	// StageLaunch is a failure that did not come from a specific step, such as a
	// launcher that returned neither a session nor an error.
	StageLaunch LaunchStage = "launch"
)

// LaunchError is the failure of one attempt to start a sandbox's browser.
//
// It is a type rather than a string because the panel has to be able to say
// which step failed, and because callers must be able to tell a launch failure
// (retryable, maybe) from a usage error. Ensure returns it only after the
// in-flight entry for that sandbox is gone, so the next call starts a fresh
// attempt instead of replaying this error.
type LaunchError struct {
	SandboxID string
	Stage     LaunchStage
	Err       error
}

func (e *LaunchError) Error() string {
	return fmt.Sprintf("browser: sandbox %s: launch failed at %s: %v", e.SandboxID, e.Stage, e.Err)
}

func (e *LaunchError) Unwrap() error { return e.Err }

// pendingLaunch is one in-flight launch for one sandbox.
//
// It is a rendezvous, not a cache: Ensure publishes it before starting Chrome
// and deletes it when the attempt is over, whatever the outcome. A second
// caller waits on done and takes the first caller's session or error, which is
// what stops two Chromiums from being pointed at one profile.
type pendingLaunch struct {
	done    chan struct{}
	session *Session
	err     error
	// cancelled is set when Close/CloseAll arrives while the launch is running;
	// the browser that comes out of it is stopped instead of recorded.
	cancelled bool
}

// Config wires the manager.
type Config struct {
	// Binary is the Chrome/Chromium executable. Empty means the feature is off.
	Binary string
	// Headless runs without a window. Screencast works either way; headless is
	// the default because a sandbox usually has no display.
	Headless bool
	// Viewport is the emulated size the screencast is scaled from.
	Width, Height int
	// Quality is the JPEG quality of screencast frames (1-100).
	Quality int
	// Proxy is passed to Chrome as --proxy-server. A sandbox usually has no
	// direct route to the internet, and a browser that cannot resolve anything
	// looks exactly like a broken screencast.
	Proxy string
	// Launcher optionally wraps the browser's argv so it runs inside the same
	// process jail as the agent. Nil means run it directly.
	Launcher isolate.Launcher
	// Now is injectable for tests.
	Now func() time.Time
	// RemoteAPIKey is sent on the devtools handshake when Remote is used, because the
	// sandbox proxies CDP and guards it with the same key as its HTTP API.
	//
	// It is the static fallback; production sets RemoteKey instead, because each
	// sandbox has its own key (see internal/sandbox/key.go).
	RemoteAPIKey string
	// RemoteKey resolves the key of one sandbox's API. When it is set it wins over
	// RemoteAPIKey: a sandbox's CDP endpoint is guarded by that sandbox's key, and
	// presenting a deployment-wide one would either be rejected or — worse — be
	// the same value an attacker already has.
	RemoteKey func(ctx context.Context, sandboxID string) (string, error)
	// IdleTimeout is how long a browser may stay alive with no client attached
	// before the manager stops it. Zero means DefaultIdleTimeout; a negative
	// value disables the policy (the panel still lazy-mounts, but nothing is
	// reaped — only useful for a deployment that wants a warm browser).
	IdleTimeout time.Duration
	// Logger receives one line per browser the idle policy stops. Nil is quiet.
	Logger *slog.Logger
	// Remote resolves a CDP websocket for a sandbox instead of launching one.
	//
	// This is the AIO Sandbox integration: the browser lives in the sandbox container,
	// next to the files the agent edits, and the control plane only speaks CDP to it.
	// When it returns a URL, no process is started here.
	Remote func(ctx context.Context, sandboxID string) (wsURL string, debugHTTP string, err error)
}

// Session is one running browser inside one sandbox.
type Session struct {
	SandboxID string
	Profile   string
	WSURL     string
	PID       int
	Started   time.Time

	// remote means this session attached to a browser it does not own.
	remote   bool
	apiKey   string
	proc     *exec.Cmd
	client   *cdp
	stopOnce sync.Once
	stopped  chan struct{}

	// Tab state (PRD FR-11.7): one devtools connection per extra page.
	tabMu     sync.Mutex
	tabs      map[string]*cdp
	debugHTTP string
	// defaultTab is the target id of the page the browser opened with. It is a
	// target id, not a CDP session id: pages are named by target id on the wire,
	// so the tab list, the panel and every tab-scoped call mean the same thing.
	defaultTab string
	// activeTab is the page the panel is showing. Screenshots follow it, which
	// is the difference between "a file of the page in front of me" and "a file
	// of whatever target Chrome attached to first" (FR-11.8).
	activeTab string
}

// Snapshot is what the API reports about a browser.
type Snapshot struct {
	SandboxID string `json:"sandbox_id"`
	Profile   string `json:"profile"`
	PID       int    `json:"pid"`
	Started   string `json:"started"`
	URL       string `json:"url"`
}

// DefaultIdleTimeout is the shipped idle policy: a sandbox browser that no
// viewer has been attached to for a minute is stopped. It is long enough that
// switching tabs inside the panel, or reloading the page, does not pay for a
// fresh Chromium; short enough that a session left open overnight does not pin
// a browser forever.
const DefaultIdleTimeout = 60 * time.Second

// maxLaunchTime bounds one launch when the caller that started it goes away.
// waitForDevTools has its own 30s budget, so this only has to cover a browser
// that gets past that and then hangs in the handshake.
const maxLaunchTime = 60 * time.Second

// Manager owns the browsers of this process, keyed by sandbox.
type Manager struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*Session
	// launches holds the in-flight launch of each sandbox. It is keyed by sandbox
	// on purpose: two sandboxes must start in parallel, only two launches of the
	// same sandbox must not.
	launches map[string]*pendingLaunch
	closed   bool

	// clients counts the callers currently attached to a sandbox's browser: a
	// viewer socket, a tab list, a screenshot. A browser is a candidate for the
	// idle policy only when its count is zero, which is what makes "switching
	// away" measurable instead of guessed from websocket close timing.
	clients map[string]int
	// lastRelease is when the last client detached. It is deliberately later
	// than Started: a browser that a script asked for and nobody watched is
	// still swept, one IdleTimeout after the operation ended.
	lastRelease map[string]time.Time
	idle        time.Duration
	log         *slog.Logger

	reapOnce     sync.Once
	reapStop     chan struct{}
	reapDone     chan struct{}
	stopReapOnce sync.Once

	// start launches one browser. It is a field so the launch serialisation can be
	// tested without a browser; NewManager wires the real launch.
	start func(ctx context.Context, sandboxID, profileRoot string) (*Session, error)
}

// NewManager builds a manager. A manager with no binary is still usable: every
// Ensure returns ErrNotConfigured, which is the honest answer.
func NewManager(cfg Config) *Manager {
	if cfg.Width <= 0 {
		cfg.Width = 1280
	}
	if cfg.Height <= 0 {
		cfg.Height = 800
	}
	if cfg.Quality <= 0 {
		cfg.Quality = 60
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	m := &Manager{
		cfg:         cfg,
		sessions:    map[string]*Session{},
		launches:    map[string]*pendingLaunch{},
		clients:     map[string]int{},
		lastRelease: map[string]time.Time{},
		idle:        cfg.IdleTimeout,
		log:         cfg.Logger,
	}
	if m.idle == 0 {
		m.idle = DefaultIdleTimeout
	}
	// A negative timeout is the documented way to disable the policy; keeping it
	// negative would make every comparison true, so normalise it to zero.
	if m.idle < 0 {
		m.idle = 0
	}
	m.start = m.launch
	return m
}

// StartIdleReaper starts the background sweep that enforces Config.IdleTimeout.
//
// It is explicit rather than automatic in NewManager for one reason: a manager
// built in a test must not grow a goroutine that outlives the test, and the
// production wiring is a single call in cmd/piforged. Calling it twice is a
// no-op, and a manager with the policy disabled does nothing.
func (m *Manager) StartIdleReaper() {
	if m.idle <= 0 {
		return
	}
	m.reapOnce.Do(func() {
		m.reapStop = make(chan struct{})
		m.reapDone = make(chan struct{})
		interval := m.idle / 2
		if interval < time.Second {
			interval = time.Second
		}
		if interval > 5*time.Second {
			interval = 5 * time.Second
		}
		go func() {
			defer close(m.reapDone)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-m.reapStop:
					return
				case <-ticker.C:
					m.SweepIdle(m.cfg.Now())
				}
			}
		}()
	})
}

// IdleTimeout reports the effective policy, which is what a status page and a
// test both need to print instead of re-deriving the default.
func (m *Manager) IdleTimeout() time.Duration { return m.idle }

// Acquire hands out the sandbox's browser together with the release function
// the caller must run when it is done with it.
//
// Every path that talks to the browser goes through here, not Ensure: the idle
// policy's whole input is "is anyone attached", and a viewer socket that opened
// without registering would look exactly like an abandoned browser. The
// reservation happens *before* the launch so a slow start cannot be swept out
// from under the caller that asked for it.
func (m *Manager) Acquire(ctx context.Context, sandboxID, profileRoot string) (*Session, func(), error) {
	if err := m.reserve(sandboxID); err != nil {
		return nil, nil, err
	}
	session, err := m.Ensure(ctx, sandboxID, profileRoot)
	if err != nil {
		m.release(sandboxID)
		return nil, nil, err
	}
	var once sync.Once
	return session, func() { once.Do(func() { m.release(sandboxID) }) }, nil
}

func (m *Manager) reserve(sandboxID string) error {
	if strings.TrimSpace(sandboxID) == "" {
		return errors.New("browser: sandbox id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.clients[sandboxID]++
	delete(m.lastRelease, sandboxID)
	return nil
}

func (m *Manager) release(sandboxID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients[sandboxID] > 1 {
		m.clients[sandboxID]--
		return
	}
	delete(m.clients, sandboxID)
	m.lastRelease[sandboxID] = m.cfg.Now()
}

// SweepIdle stops every browser that has had no client for longer than the idle
// timeout, and returns how many it stopped. now is a parameter so the policy can
// be tested against a fake clock instead of a sleep.
//
// A browser that never had a client is measured from its Start, so a launch
// nobody ended up watching is still collected. The stops run concurrently: a
// deployment that accumulated hundreds of leaked browsers must drain in seconds
// (the shutdown of one hung Chromium is bounded by Stop's own waits), not one
// two-second timeout at a time.
func (m *Manager) SweepIdle(now time.Time) int {
	if m.idle <= 0 {
		return 0
	}
	type victim struct {
		id      string
		session *Session
		since   time.Time
	}
	var victims []victim

	m.mu.Lock()
	for sandboxID, session := range m.sessions {
		if m.clients[sandboxID] > 0 {
			continue
		}
		since := session.Started
		if released, ok := m.lastRelease[sandboxID]; ok && released.After(since) {
			since = released
		}
		if now.Sub(since) < m.idle {
			continue
		}
		delete(m.sessions, sandboxID)
		delete(m.lastRelease, sandboxID)
		victims = append(victims, victim{id: sandboxID, session: session, since: since})
	}
	m.mu.Unlock()

	if len(victims) == 0 {
		return 0
	}
	var wg sync.WaitGroup
	for _, v := range victims {
		wg.Add(1)
		go func(v victim) {
			defer wg.Done()
			v.session.Stop()
			if m.log != nil {
				// Exactly one line per stopped browser: this is the observable the
				// leak ticket asks for, and a per-frame or per-poll log would bury it.
				m.log.Info("stopped an idle sandbox browser",
					"sandbox", v.id,
					"idle_for", now.Sub(v.since).Round(time.Second).String(),
					"idle_timeout", m.idle.String())
			}
		}(v)
	}
	wg.Wait()
	return len(victims)
}

// Configured reports whether a binary is available.
func (m *Manager) Configured() bool { return strings.TrimSpace(m.cfg.Binary) != "" }

// Available reports whether this manager can produce a browser at all: a local
// binary, or a Remote resolver that attaches to one inside a sandbox. The HTTP
// layer gates the panel on this, not on Configured: with -sandbox-browser remote
// there is no local binary by design, and requiring one rejected every request
// for a browser the container was already running.
func (m *Manager) Available() bool { return m.Configured() || m.cfg.Remote != nil }

// Ensure returns the sandbox's browser, starting it when necessary.
//
// profileRoot is the directory the profile lives in — always inside the
// sandbox, so a paused sandbox takes the browser state with it and a deleted
// sandbox deletes the cookies.
//
// Launches are serialised per sandbox, not globally. Two callers that race for
// the same sandbox's browser (a panel that reconnects while a tab is opening,
// two viewers of the same session) used to start two Chromiums on one profile:
// Chromium's profile singleton lets one win and makes the other's devtools
// endpoint never appear, which the user saw as "the browser panel is stuck".
// The second caller now waits for the first attempt and reuses whatever it
// produced. Different sandboxes still launch in parallel.
func (m *Manager) Ensure(ctx context.Context, sandboxID, profileRoot string) (*Session, error) {
	// A remote browser needs no local binary: the "configured" question is whether there
	// is a browser at all, and a sandbox container answers it by existing.
	if !m.Configured() && m.cfg.Remote == nil {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(sandboxID) == "" {
		return nil, errors.New("browser: sandbox id is required")
	}

	var stale *Session
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if existing, ok := m.sessions[sandboxID]; ok {
		if !existing.dead() {
			m.mu.Unlock()
			return existing, nil
		}
		// A session that cannot answer is not reused. It is also not simply
		// forgotten: a Chromium process can outlive its devtools connection and
		// still hold the profile, so the replacement launch would find a live
		// singleton lock. It is stopped below, once the in-flight entry makes
		// every other caller wait.
		delete(m.sessions, sandboxID)
		stale = existing
	}
	if pending, inFlight := m.launches[sandboxID]; inFlight {
		m.mu.Unlock()
		return m.awaitLaunch(ctx, sandboxID, pending)
	}
	pending := &pendingLaunch{done: make(chan struct{})}
	m.launches[sandboxID] = pending
	m.mu.Unlock()

	if stale != nil {
		stale.Stop()
	}

	// The attempt outlives the caller that happened to start it: another panel may
	// be waiting on it, and tearing a shared browser down because one websocket
	// closed is how the remaining viewer gets stuck. The bound keeps a hung launch
	// from pinning the sandbox forever.
	launchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), maxLaunchTime)
	session, err := m.start(launchCtx, sandboxID, profileRoot)
	cancel()

	return m.finishLaunch(sandboxID, pending, session, err)
}

// awaitLaunch waits for the launch that is already running for this sandbox.
//
// A failure is passed on as the launcher produced it: the waiters asked for the
// same browser, so the honest answer is the same typed error. A caller whose
// own context ends stops waiting without disturbing the launch — the browser it
// was waiting for is still recorded, and its next call reuses it.
func (m *Manager) awaitLaunch(ctx context.Context, sandboxID string, pending *pendingLaunch) (*Session, error) {
	select {
	case <-pending.done:
		if pending.err != nil {
			return nil, pending.err
		}
		if pending.session == nil {
			return nil, &LaunchError{
				SandboxID: sandboxID,
				Stage:     StageLaunch,
				Err:       errors.New("the launch finished without a session"),
			}
		}
		return pending.session, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// finishLaunch records one attempt's result and wakes its waiters.
//
// The in-flight entry is removed whatever the outcome. It is a rendezvous, not
// a cache: a failed launch costs one error, not a sandbox that can never start
// again.
func (m *Manager) finishLaunch(sandboxID string, pending *pendingLaunch, session *Session, err error) (*Session, error) {
	m.mu.Lock()
	delete(m.launches, sandboxID)
	if err == nil && (m.closed || pending.cancelled) {
		err = ErrClosed
	}
	if err == nil && (session == nil || session.dead()) {
		err = &LaunchError{
			SandboxID: sandboxID,
			Stage:     StageExited,
			Err:       errors.New("the browser exited before it could be used"),
		}
	}
	if err != nil {
		// Every failure leaving Ensure is typed, so a caller can tell a launch
		// failure from a bad request. A context that ended is not a launch
		// failure: pass it through so errors.Is still works.
		var launchErr *LaunchError
		if !errors.As(err, &launchErr) && !errors.Is(err, ErrClosed) &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			err = &LaunchError{SandboxID: sandboxID, Stage: StageLaunch, Err: err}
		}
		pending.err = err
	} else {
		m.sessions[sandboxID] = session
		pending.session = session
	}
	m.mu.Unlock()

	if err != nil {
		if session != nil {
			// Never leave a started browser unrecorded: nothing could stop it later.
			session.Stop()
		}
		close(pending.done)
		return nil, err
	}
	close(pending.done)
	return session, nil
}

// launch starts chrome and connects to its devtools endpoint.
func (m *Manager) launch(ctx context.Context, sandboxID, profileRoot string) (*Session, error) {
	profile := filepath.Join(profileRoot, ".piforge-browser")
	// A remote browser is attached to, not started: the sandbox owns the process
	// and the profile directory. Preparing the profile here would create a
	// directory on the control plane's disk that no browser ever opens — and with
	// a detached workspace (docs/EXECUTION-BOUNDARY.md stage 2) profileRoot is a
	// container path, so "preparing" it would either fail or, worse, create
	// /workspace on this machine. The lock handling below is local-browser work.
	remote := m.cfg.Remote != nil
	if !remote {
		// A lock left by a browser that is gone is cleared here, inside the
		// serialised launch, so two attempts can never both decide it is stale.
		if err := prepareProfile(profile); err != nil {
			return nil, &LaunchError{SandboxID: sandboxID, Stage: StageProfile, Err: err}
		}
	}

	// Pick a free port rather than a fixed one, so two sandboxes on one host
	// cannot fight over 9222.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, &LaunchError{SandboxID: sandboxID, Stage: StageStart,
			Err: fmt.Errorf("reserve a devtools port: %w", err)}
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-features=Translate,MediaRouter",
		fmt.Sprintf("--window-size=%d,%d", m.cfg.Width, m.cfg.Height),
		"about:blank",
	}
	if m.cfg.Headless {
		args = append([]string{"--headless=new", "--disable-gpu"}, args...)
	}
	// Chromium refuses to start as root with its own sandbox enabled, which is
	// how it runs inside a container. The container is the sandbox in that
	// deployment, so this is the boundary Chromium is giving up, not the product's.
	if runningAsRoot() {
		args = append(args, "--no-sandbox")
	}
	// A container's /dev/shm is 64 MiB by default; Chromium crashes rendering a
	// normal page into it.
	args = append(args, "--disable-dev-shm-usage")
	if proxy := strings.TrimSpace(m.cfg.Proxy); proxy != "" {
		args = append([]string{"--proxy-server=" + proxy}, args...)
	}

	argv := append([]string{m.cfg.Binary}, args...)
	if m.cfg.Launcher != nil {
		argv = m.cfg.Launcher.Wrap(argv)
	}
	if len(argv) == 0 {
		return nil, &LaunchError{SandboxID: sandboxID, Stage: StageStart,
			Err: errors.New("empty command")}
	}
	// A remote browser is attached to, not started: the sandbox owns the process, and
	// killing it here would kill the user's browser.
	if remote {
		wsURL, debugHTTP, rerr := m.cfg.Remote(ctx, sandboxID)
		if rerr == nil && wsURL != "" {
			session, aerr := m.attachRemote(ctx, sandboxID, profile, wsURL, debugHTTP)
			if aerr != nil {
				return nil, &LaunchError{SandboxID: sandboxID, Stage: StageAttach, Err: aerr}
			}
			return session, nil
		}
		if rerr != nil && !m.Configured() {
			// There is no local binary to fall back to, and "exec: no command" would
			// blame this process instead of the sandbox that did not answer.
			return nil, &LaunchError{SandboxID: sandboxID, Stage: StageAttach,
				Err: fmt.Errorf("sandbox %s: %w", sandboxID, rerr)}
		}
	}

	//nolint:gosec // the binary path is operator configuration, not user input
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, &LaunchError{SandboxID: sandboxID, Stage: StageStart,
			Err: fmt.Errorf("start chrome: %w", err)}
	}

	// 30s, not 15: a loaded machine can take far longer than 15s to bring Chrome's
	// devtools endpoint up, and the failure mode — "connection refused" mapped to a
	// 500 — looks like a product bug rather than a slow start.
	wsURL, err := waitForDevTools(ctx, port, 30*time.Second)
	if err != nil {
		abandon(cmd)
		return nil, &LaunchError{SandboxID: sandboxID, Stage: StageDevTools, Err: err}
	}

	client, err := dialCDP(ctx, wsURL, nil)
	if err != nil {
		abandon(cmd)
		return nil, &LaunchError{SandboxID: sandboxID, Stage: StageDevTools, Err: err}
	}
	// A browser-level connection can only talk to Target.*; Page and Input need
	// a page session.
	if err := client.attachToPage(ctx); err != nil {
		client.close()
		abandon(cmd)
		return nil, &LaunchError{SandboxID: sandboxID, Stage: StageAttach, Err: err}
	}

	session := &Session{
		SandboxID:  sandboxID,
		Profile:    profile,
		WSURL:      wsURL,
		PID:        cmd.Process.Pid,
		Started:    m.cfg.Now(),
		proc:       cmd,
		client:     client,
		stopped:    make(chan struct{}),
		tabs:       map[string]*cdp{},
		debugHTTP:  fmt.Sprintf("http://127.0.0.1:%d", port),
		defaultTab: client.target(),
	}
	// Watch the process so a crash shows up as a dead session, not a hang.
	go func() {
		_ = cmd.Wait()
		session.Stop()
	}()

	// Prove the page session still answers before handing the browser out. A
	// Chromium that lost its profile to another instance can exit right after the
	// handshake, and a Session attached to it is exactly the "panel is stuck"
	// state: it exists, it is in the cache, and every action hangs. Asking the
	// page a question it always answers is cheaper than making the user find out.
	var history struct {
		CurrentIndex int `json:"currentIndex"`
	}
	if err := client.call(ctx, "Page.getNavigationHistory", nil, &history); err != nil {
		session.Stop()
		return nil, &LaunchError{SandboxID: sandboxID, Stage: StageExited,
			Err: fmt.Errorf("the browser did not survive its own startup: %w", err)}
	}

	return session, nil
}

// abandon stops a browser this process started and reaps it, so a failed launch
// cannot leave a process (or a zombie) for the next attempt to trip over.
//
// It uses Process.Wait rather than Cmd.Wait because the success path already
// installed a goroutine that owns Cmd.Wait: a second Cmd.Wait on the same
// command would return "Wait was already called" and leave the reaping to the
// other goroutine.
func abandon(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// waitForDevTools polls Chrome's HTTP endpoint until it reports a websocket URL.
func waitForDevTools(ctx context.Context, port int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := &http.Client{Timeout: 2 * time.Second}

	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(150 * time.Millisecond)
			continue
		}
		var body struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if decodeErr == nil && body.WebSocketDebuggerURL != "" {
			return body.WebSocketDebuggerURL, nil
		}
		lastErr = decodeErr
		time.Sleep(150 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timed out waiting for devtools")
	}
	return "", fmt.Errorf("browser: devtools never became ready: %w", lastErr)
}

// dead reports whether the session can still be used.
//
// A browser whose process exited is dead, and so is one whose devtools
// connection ended: the second case used to be invisible here, so the manager
// kept handing out a Session that could never answer again (a panel that
// reconnected saw "browser busy" forever). Treating both as dead is what makes
// the next Ensure start a fresh browser instead of reusing a broken handle.
func (s *Session) dead() bool {
	select {
	case <-s.stopped:
		return true
	default:
	}
	return s.client != nil && s.client.gone()
}

// Stop shuts the browser down. It is safe to call more than once and from
// several goroutines.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopped)
		s.closeTabs()
		// Ask the browser to close before killing it. Chromium flushes its profile
		// (cookies, storage) on a normal shutdown; a bare Kill loses whatever was
		// written since the last flush, which is how a login in the panel failed to
		// survive into the next session (PRD FR-11.9).
		if s.client != nil {
			// A remote browser belongs to the sandbox: closing it would close somebody
			// else's window, and the next viewer would find it gone.
			if !s.remote {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				var ignored struct{}
				_ = s.client.call(ctx, "Browser.close", nil, &ignored)
				cancel()
			}
			// Give it a moment to exit on its own before taking the process down.
			// The wait after the kill matters as much as the one before: a browser
			// that is only *dying* still answers on its profile socket, and the
			// replacement launch runs immediately after Stop. Letting the OS reap
			// it here keeps that launch from reading the dying process as a live
			// singleton lock.
			if s.proc != nil && s.proc.Process != nil {
				exited := make(chan struct{})
				go func() {
					_, _ = s.proc.Process.Wait()
					close(exited)
				}()
				select {
				case <-exited:
				case <-time.After(2 * time.Second):
					_ = s.proc.Process.Kill()
					select {
					case <-exited:
					case <-time.After(2 * time.Second):
					}
				}
			}
			s.client.close()
		}
		if s.proc != nil && s.proc.Process != nil {
			_ = s.proc.Process.Kill()
		}
	})
}

// Snapshot describes the session for the API.
func (s *Session) Snapshot() Snapshot {
	return Snapshot{
		SandboxID: s.SandboxID,
		Profile:   s.Profile,
		PID:       s.PID,
		Started:   s.Started.UTC().Format(time.RFC3339),
		URL:       s.WSURL,
	}
}

// Screencast starts streaming frames to onFrame and blocks until ctx is done or
// the browser goes away.
//
// Chrome stops sending frames unless every frame is acknowledged, so the ack is
// sent here rather than left to the caller: forgetting it looks exactly like a
// frozen page.
func (s *Session) Screencast(ctx context.Context, onFrame func(jpeg []byte, meta Meta)) error {
	return s.screencastOn(ctx, s.client, onFrame)
}

// screencastOn is Screencast for a specific connection, so a tab can stream
// without disturbing the default page.
func (s *Session) screencastOn(ctx context.Context, client *cdp, onFrame func(jpeg []byte, meta Meta)) error {
	if err := client.call(ctx, "Page.enable", nil, nil); err != nil {
		return err
	}
	params := map[string]any{
		"format":        "jpeg",
		"quality":       60,
		"maxWidth":      1280,
		"maxHeight":     800,
		"everyNthFrame": 1,
	}
	// Take the screencast over rather than refusing to start.
	//
	// A viewer that goes away without its stop reaching Chrome — a page reload closes
	// the WebSocket, a proxy drops it, the tab is closed mid-frame — leaves Chrome
	// streaming to nobody, and every later attempt failed with "Screencast is already
	// active". Stopping first is harmless when nothing is running and is the only way
	// back when something is.
	_ = client.call(ctx, "Page.stopScreencast", nil, nil)
	if err := client.call(ctx, "Page.startScreencast", params, nil); err != nil {
		// Chrome can still be tearing the previous cast down; one short retry covers the
		// window without pretending the failure did not happen.
		time.Sleep(150 * time.Millisecond)
		_ = client.call(ctx, "Page.stopScreencast", nil, nil)
		if retry := client.call(ctx, "Page.startScreencast", params, nil); retry != nil {
			return fmt.Errorf("browser: start screencast: %w (after retry: %v)", err, retry)
		}
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.call(stopCtx, "Page.stopScreencast", nil, nil)
	}()

	type frame struct {
		Data      string `json:"data"`
		SessionID int64  `json:"sessionId"`
		Metadata  Meta   `json:"metadata"`
	}

	for {
		event, err := client.next(ctx)
		if err != nil {
			return err
		}
		if event.Method != "Page.screencastFrame" {
			continue
		}
		var f frame
		if json.Unmarshal(event.Params, &f) != nil {
			continue
		}
		if err := client.call(ctx, "Page.screencastFrameAck",
			map[string]any{"sessionId": f.SessionID}, nil); err != nil {
			return err
		}
		onFrame(decodeBase64(f.Data), f.Metadata)
	}
}

// Meta is Chrome's description of a frame.
type Meta struct {
	DeviceWidth     float64 `json:"deviceWidth"`
	DeviceHeight    float64 `json:"deviceHeight"`
	PageScaleFactor float64 `json:"pageScaleFactor"`
	OffsetTop       float64 `json:"offsetTop"`
	ScrollOffsetX   float64 `json:"scrollOffsetX"`
	ScrollOffsetY   float64 `json:"scrollOffsetY"`
	Timestamp       float64 `json:"timestamp"`
}

// ValidateNavigationURL is the one place the browser's navigation policy lives.
//
// http(s) is the normal case. `about:blank` is allowed because a blank tab is a
// real page the UI offers (新标签页) and the previous refusal made the URL bar
// unable to reach it at all. Everything else — file:, javascript:, data: — stays
// refused: the agent must not be able to make the browser read local files or
// execute script through a URL scheme.
//
// An empty string means "the blank page", matching OpenTab's default.
func ValidateNavigationURL(url string) error {
	url = strings.TrimSpace(url)
	if url == "" || strings.EqualFold(url, "about:blank") {
		return nil
	}
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fmt.Errorf("browser: %q is not an http(s) url", url)
	}
	return nil
}

// Navigate points the browser at a URL.
func (s *Session) Navigate(ctx context.Context, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return errors.New("browser: url is required")
	}
	if err := ValidateNavigationURL(url); err != nil {
		return err
	}
	return s.client.call(ctx, "Page.navigate", map[string]any{"url": url}, nil)
}

// Resize makes the page render at the viewer's size.
//
// The browser starts at a fixed window size, but the panel changes size whenever
// the sidebar collapses or the window is resized. Without this the page keeps
// rendering at 1280x800 and the viewer either letterboxes it or stretches it,
// which is why a responsive site would not actually adapt.
func (s *Session) Resize(ctx context.Context, width, height int) error {
	if width < 120 || height < 120 {
		return fmt.Errorf("browser: refusing to resize to %dx%d", width, height)
	}
	// Cap it: a 4K panel should not make the sandbox render a 4K page.
	if width > 2560 {
		width = 2560
	}
	if height > 1600 {
		height = 1600
	}
	return s.client.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             width,
		"height":            height,
		"deviceScaleFactor": 1,
		"mobile":            false,
	}, nil)
}

// Input is one user action from the viewer.
type Input struct {
	// Type is click|move|key|text|scroll.
	Type string  `json:"type"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	// Button is left|right|middle for click.
	Button string `json:"button,omitempty"`
	// Key/Code/Text are used by key and text.
	Key  string `json:"key,omitempty"`
	Code string `json:"code,omitempty"`
	Text string `json:"text,omitempty"`
	// DeltaX/DeltaY are used by scroll.
	DeltaX float64 `json:"delta_x,omitempty"`
	DeltaY float64 `json:"delta_y,omitempty"`
}

// Dispatch forwards one input event to the page.
func (s *Session) Dispatch(ctx context.Context, in Input) error {
	return s.dispatchOn(ctx, s.client, in)
}

// dispatchOn sends one event through a specific connection.
func (s *Session) dispatchOn(ctx context.Context, client *cdp, in Input) error {
	switch in.Type {
	case "move":
		return s.client.call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseMoved", "x": in.X, "y": in.Y,
		}, nil)
	case "click":
		button := in.Button
		if button == "" {
			button = "left"
		}
		for _, eventType := range []string{"mousePressed", "mouseReleased"} {
			if err := client.call(ctx, "Input.dispatchMouseEvent", map[string]any{
				"type": eventType, "x": in.X, "y": in.Y,
				"button": button, "clickCount": 1,
			}, nil); err != nil {
				return err
			}
		}
		return nil
	case "scroll":
		return client.call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseWheel", "x": in.X, "y": in.Y,
			"deltaX": in.DeltaX, "deltaY": in.DeltaY,
		}, nil)
	case "text":
		return client.call(ctx, "Input.insertText", map[string]any{"text": in.Text}, nil)
	case "key":
		// keyDown then keyUp, which is what a real key press produces. `text`
		// is only set for printable characters; sending it for Enter or an
		// arrow would insert the raw name into the page.
		payload := map[string]any{
			"type": "keyDown", "key": in.Key, "code": in.Code,
		}
		if in.Text != "" {
			payload["text"] = in.Text
		}
		if err := client.call(ctx, "Input.dispatchKeyEvent", payload, nil); err != nil {
			return err
		}
		payload["type"] = "keyUp"
		delete(payload, "text")
		return client.call(ctx, "Input.dispatchKeyEvent", payload, nil)
	default:
		return fmt.Errorf("browser: unknown input type %q", in.Type)
	}
}

// Close shuts a sandbox's browser down and forgets it.
//
// When a launch is still in flight for that sandbox it is marked cancelled: the
// attempt finishes, the browser it produced is stopped instead of recorded, and
// its caller learns the sandbox was closed. Otherwise the browser would appear
// in the manager after the caller had already closed it.
func (m *Manager) Close(sandboxID string) bool {
	m.mu.Lock()
	session, ok := m.sessions[sandboxID]
	delete(m.sessions, sandboxID)
	delete(m.clients, sandboxID)
	delete(m.lastRelease, sandboxID)
	if pending, inFlight := m.launches[sandboxID]; inFlight {
		pending.cancelled = true
		ok = true
	}
	m.mu.Unlock()
	if session != nil {
		session.Stop()
	}
	return ok
}

// CloseAll shuts every browser down.
func (m *Manager) CloseAll() {
	m.stopReapOnce.Do(func() {
		if m.reapStop != nil {
			close(m.reapStop)
			<-m.reapDone
		}
	})
	m.mu.Lock()
	m.closed = true
	sessions := m.sessions
	m.sessions = map[string]*Session{}
	m.clients = map[string]int{}
	m.lastRelease = map[string]time.Time{}
	m.mu.Unlock()
	for _, session := range sessions {
		session.Stop()
	}
}

// Get returns the sandbox's live browser, if any.
func (m *Manager) Get(sandboxID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sandboxID]
	if !ok || session.dead() {
		return nil, false
	}
	return session, true
}

// Len reports how many browsers are running.
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// SandboxIDs lists the sandboxes that currently have a browser. It is what
// makes the admin status specific: a count alone cannot say whether the browser
// the operator is looking at belongs to the session they are debugging.
func (m *Manager) SandboxIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for sandboxID := range m.sessions {
		ids = append(ids, sandboxID)
	}
	sort.Strings(ids)
	return ids
}

// decodeBase64 decodes a frame payload, returning nil on malformed input so a
// single bad frame cannot kill the screencast.
func decodeBase64(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return data
}

// Screenshot captures the page the panel is showing as PNG.
//
// It is the one action that turns "I can see it in the panel" into "the agent can
// read it": the caller writes the bytes into the checkout, where the file tree and
// the agent both see them (PRD FR-11.8).
//
// "The page the panel is showing" is the active tab, not the target Chrome
// attached to when it launched: those differ as soon as a second page exists
// (PRD FR-11.7), and capturing the launch target is how the endpoint wrote a
// white `about:blank` while the panel showed a real page.
func (s *Session) Screenshot(ctx context.Context) ([]byte, error) {
	return s.ScreenshotTab(ctx, "")
}

// ScreenshotTab captures one page, or the active one when tabID is empty.
//
// The target is brought to the front first. Chrome only composites the active
// page: `Page.captureScreenshot` on a background tab does not answer at all in
// the container's headless Chromium, which is what the panel's second
// consecutive capture used to hit — a 20-second hang reported as "browser busy".
// Activating the target makes the capture deterministic no matter how many pages
// are open.
func (s *Session) ScreenshotTab(ctx context.Context, tabID string) ([]byte, error) {
	if strings.TrimSpace(tabID) == "" {
		tabID = s.ActiveTab()
	}
	client, err := s.tabClient(ctx, tabID)
	if err != nil {
		return nil, err
	}
	target := tabID
	if target == "" {
		target = s.DefaultTabID()
	}
	if target != "" {
		var ignored struct{}
		if err := s.client.call(ctx, "Target.activateTarget", map[string]any{"targetId": target}, &ignored); err != nil {
			// Not fatal: a browser that will not focus a page can still capture
			// the one that is already in front.
			slog.Debug("could not activate the page before a screenshot", "target", target, "error", err.Error())
		}
	}

	png, err := captureOn(ctx, client)
	if err != nil {
		// A remote page can be replaced under us (a navigation, a sandbox
		// restarting its browser). Re-attaching once is cheaper than making the
		// caller reload the panel, and it is the difference between "screenshot
		// is broken" and "the page moved".
		if !s.remote || !strings.Contains(err.Error(), "not attached") {
			return nil, err
		}
		if aerr := s.client.attachToPage(ctx); aerr != nil {
			return nil, err
		}
		if png, err = captureOn(ctx, client); err != nil {
			return nil, err
		}
	}
	if cerr := inspectCapture(png, pageURLOn(ctx, client)); cerr != nil {
		return nil, cerr
	}
	return png, nil
}

// pageURLOn reads the address of the page a connection is attached to. An
// unknown URL is returned empty rather than as an error: it only weakens the
// blank check, which still inspects the pixels.
func pageURLOn(ctx context.Context, client *cdp) string {
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := client.call(ctx, "Page.getNavigationHistory", nil, &history); err != nil {
		return ""
	}
	if history.CurrentIndex < 0 || history.CurrentIndex >= len(history.Entries) {
		return ""
	}
	return strings.TrimSpace(history.Entries[history.CurrentIndex].URL)
}

// captureOn takes a screenshot over a given connection (one tab).
func captureOn(ctx context.Context, client *cdp) ([]byte, error) {
	// Page must be enabled on this session before it can capture: a fresh attach has the
	// domain off, and Chrome answers "Not attached to an active page" rather than saying so.
	_ = client.call(ctx, "Page.enable", nil, nil)

	var out struct {
		Data string `json:"data"`
	}
	err := client.call(ctx, "Page.captureScreenshot", map[string]any{
		"format":                "png",
		"fromSurface":           true,
		"captureBeyondViewport": false,
	}, &out)
	if err != nil {
		return nil, fmt.Errorf("browser: capture screenshot: %w", err)
	}
	if out.Data == "" {
		return nil, errors.New("browser: the page returned an empty screenshot")
	}
	raw, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return nil, fmt.Errorf("browser: decode screenshot: %w", err)
	}
	return raw, nil
}

// attachRemote builds a Session over a browser that is already running elsewhere.
//
// The tab bookkeeping is the same as the local path — a browser-level connection can only
// talk to Target.*, so a page session is attached here too. What differs is ownership:
// Stop() closes the connection and leaves the process alone.
func (m *Manager) attachRemote(ctx context.Context, sandboxID, profile, wsURL, debugHTTP string) (*Session, error) {
	apiKey, err := m.remoteKey(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	client, err := dialCDP(ctx, wsURL, remoteHeaders(apiKey))
	if err != nil {
		return nil, err
	}
	if err := client.attachToPage(ctx); err != nil {
		client.close()
		return nil, err
	}
	return &Session{
		SandboxID:  sandboxID,
		Profile:    profile,
		WSURL:      wsURL,
		Started:    m.cfg.Now(),
		remote:     true,
		apiKey:     apiKey,
		client:     client,
		stopped:    make(chan struct{}),
		tabs:       map[string]*cdp{},
		debugHTTP:  debugHTTP,
		defaultTab: client.target(),
	}, nil
}

// remoteKey resolves the key the sandbox's CDP proxy expects. The per-sandbox
// resolver wins; the static value is the fallback for a configured one.
func (m *Manager) remoteKey(ctx context.Context, sandboxID string) (string, error) {
	if m.cfg.RemoteKey != nil {
		key, err := m.cfg.RemoteKey(ctx, sandboxID)
		if err != nil {
			return "", fmt.Errorf("browser: sandbox %s: %w", sandboxID, err)
		}
		return key, nil
	}
	return m.cfg.RemoteAPIKey, nil
}

// remoteHeaders are the headers a remote devtools handshake needs.
func remoteHeaders(apiKey string) http.Header {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	header := http.Header{}
	header.Set("X-AIO-API-Key", apiKey)
	return header
}
