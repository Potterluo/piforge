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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/piforge/piforge/backend/internal/isolate"
	"time"
)

// ErrNotConfigured means no Chrome binary was found, so the feature is
// unavailable rather than broken. The HTTP layer maps it to a 422 with a reason
// instead of a 500.
var ErrNotConfigured = errors.New("browser: no chrome binary is configured")

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
	tabMu      sync.Mutex
	tabs       map[string]*cdp
	debugHTTP  string
	defaultTab string
}

// Snapshot is what the API reports about a browser.
type Snapshot struct {
	SandboxID string `json:"sandbox_id"`
	Profile   string `json:"profile"`
	PID       int    `json:"pid"`
	Started   string `json:"started"`
	URL       string `json:"url"`
}

// Manager owns the browsers of this process, keyed by sandbox.
type Manager struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*Session
	closed   bool
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
	return &Manager{cfg: cfg, sessions: map[string]*Session{}}
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
func (m *Manager) Ensure(ctx context.Context, sandboxID, profileRoot string) (*Session, error) {
	// A remote browser needs no local binary: the "configured" question is whether there
	// is a browser at all, and a sandbox container answers it by existing.
	if !m.Configured() && m.cfg.Remote == nil {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(sandboxID) == "" {
		return nil, errors.New("browser: sandbox id is required")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("browser: manager is closed")
	}
	if existing, ok := m.sessions[sandboxID]; ok && !existing.dead() {
		m.mu.Unlock()
		return existing, nil
	}
	m.mu.Unlock()

	session, err := m.launch(ctx, sandboxID, profileRoot)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		session.Stop()
		return nil, errors.New("browser: manager is closed")
	}
	// Another request may have won the race; prefer the live one.
	if existing, ok := m.sessions[sandboxID]; ok && !existing.dead() {
		m.mu.Unlock()
		session.Stop()
		return existing, nil
	}
	m.sessions[sandboxID] = session
	m.mu.Unlock()
	return session, nil
}

// launch starts chrome and connects to its devtools endpoint.
func (m *Manager) launch(ctx context.Context, sandboxID, profileRoot string) (*Session, error) {
	profile := filepath.Join(profileRoot, ".piforge-browser")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, fmt.Errorf("browser: create profile dir: %w", err)
	}

	// Pick a free port rather than a fixed one, so two sandboxes on one host
	// cannot fight over 9222.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("browser: reserve a devtools port: %w", err)
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
		return nil, errors.New("browser: empty command")
	}
	// A remote browser is attached to, not started: the sandbox owns the process, and
	// killing it here would kill the user's browser.
	if m.cfg.Remote != nil {
		wsURL, debugHTTP, rerr := m.cfg.Remote(ctx, sandboxID)
		if rerr == nil && wsURL != "" {
			return m.attachRemote(ctx, sandboxID, profile, wsURL, debugHTTP)
		}
		if rerr != nil && !m.Configured() {
			// There is no local binary to fall back to, and "exec: no command" would
			// blame this process instead of the sandbox that did not answer.
			return nil, fmt.Errorf("browser: sandbox %s: %w", sandboxID, rerr)
		}
	}

	//nolint:gosec // the binary path is operator configuration, not user input
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("browser: start chrome: %w", err)
	}

	// 30s, not 15: a loaded machine can take far longer than 15s to bring Chrome's
	// devtools endpoint up, and the failure mode — "connection refused" mapped to a
	// 500 — looks like a product bug rather than a slow start.
	wsURL, err := waitForDevTools(ctx, port, 30*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	client, err := dialCDP(ctx, wsURL, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	// A browser-level connection can only talk to Target.*; Page and Input need
	// a page session.
	if err := client.attachToPage(ctx); err != nil {
		client.close()
		_ = cmd.Process.Kill()
		return nil, err
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
		defaultTab: client.sessionID,
	}
	// Watch the process so a crash shows up as a dead session, not a hang.
	go func() {
		_ = cmd.Wait()
		session.Stop()
	}()

	return session, nil
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

func (s *Session) dead() bool {
	select {
	case <-s.stopped:
		return true
	default:
		return false
	}
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
			if s.proc != nil && s.proc.Process != nil {
				done := make(chan struct{})
				go func() {
					_, _ = s.proc.Process.Wait()
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
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

// Navigate points the browser at a URL.
func (s *Session) Navigate(ctx context.Context, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return errors.New("browser: url is required")
	}
	lower := strings.ToLower(url)
	// The agent must not be able to make the browser read local files or hit
	// internal metadata endpoints through a javascript: / file: URL.
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fmt.Errorf("browser: %q is not an http(s) url", url)
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
func (m *Manager) Close(sandboxID string) bool {
	m.mu.Lock()
	session, ok := m.sessions[sandboxID]
	delete(m.sessions, sandboxID)
	m.mu.Unlock()
	if ok {
		session.Stop()
	}
	return ok
}

// CloseAll shuts every browser down.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	sessions := m.sessions
	m.sessions = map[string]*Session{}
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

// decodeBase64 decodes a frame payload, returning nil on malformed input so a
// single bad frame cannot kill the screencast.
func decodeBase64(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return data
}

// Screenshot captures the viewport as PNG.
//
// It is the one action that turns "I can see it in the panel" into "the agent can
// read it": the caller writes the bytes into the checkout, where the file tree and
// the agent both see them (PRD FR-11.8).
func (s *Session) Screenshot(ctx context.Context) ([]byte, error) {
	frame, err := captureOn(ctx, s.client)
	if err == nil {
		return frame, nil
	}
	// A remote page can be replaced under us (a navigation, a sandbox restarting its
	// browser). Re-attaching once is cheaper than making the caller reload the panel, and
	// it is the difference between "screenshot is broken" and "the page moved".
	if s.remote && strings.Contains(err.Error(), "not attached") {
		if aerr := s.client.attachToPage(ctx); aerr == nil {
			return captureOn(ctx, s.client)
		}
	}
	return nil, err
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
		defaultTab: client.sessionID,
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
