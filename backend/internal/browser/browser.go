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
}

// Session is one running browser inside one sandbox.
type Session struct {
	SandboxID string
	Profile   string
	WSURL     string
	PID       int
	Started   time.Time

	proc     *exec.Cmd
	client   *cdp
	stopOnce sync.Once
	stopped  chan struct{}
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

// Ensure returns the sandbox's browser, starting it when necessary.
//
// profileRoot is the directory the profile lives in — always inside the
// sandbox, so a paused sandbox takes the browser state with it and a deleted
// sandbox deletes the cookies.
func (m *Manager) Ensure(ctx context.Context, sandboxID, profileRoot string) (*Session, error) {
	if !m.Configured() {
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
	//nolint:gosec // the binary path is operator configuration, not user input
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("browser: start chrome: %w", err)
	}

	wsURL, err := waitForDevTools(ctx, port, 15*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	client, err := dialCDP(ctx, wsURL)
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
		SandboxID: sandboxID,
		Profile:   profile,
		WSURL:     wsURL,
		PID:       cmd.Process.Pid,
		Started:   m.cfg.Now(),
		proc:      cmd,
		client:    client,
		stopped:   make(chan struct{}),
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
		// Ask the browser to close before killing it. Chromium flushes its profile
		// (cookies, storage) on a normal shutdown; a bare Kill loses whatever was
		// written since the last flush, which is how a login in the panel failed to
		// survive into the next session (PRD FR-11.9).
		if s.client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var ignored struct{}
			_ = s.client.call(ctx, "Browser.close", nil, &ignored)
			cancel()
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
	if err := s.client.call(ctx, "Page.enable", nil, nil); err != nil {
		return err
	}
	params := map[string]any{
		"format":        "jpeg",
		"quality":       60,
		"maxWidth":      1280,
		"maxHeight":     800,
		"everyNthFrame": 1,
	}
	if err := s.client.call(ctx, "Page.startScreencast", params, nil); err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.client.call(stopCtx, "Page.stopScreencast", nil, nil)
	}()

	type frame struct {
		Data      string `json:"data"`
		SessionID int64  `json:"sessionId"`
		Metadata  Meta   `json:"metadata"`
	}

	for {
		event, err := s.client.next(ctx)
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
		if err := s.client.call(ctx, "Page.screencastFrameAck",
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
			if err := s.client.call(ctx, "Input.dispatchMouseEvent", map[string]any{
				"type": eventType, "x": in.X, "y": in.Y,
				"button": button, "clickCount": 1,
			}, nil); err != nil {
				return err
			}
		}
		return nil
	case "scroll":
		return s.client.call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseWheel", "x": in.X, "y": in.Y,
			"deltaX": in.DeltaX, "deltaY": in.DeltaY,
		}, nil)
	case "text":
		return s.client.call(ctx, "Input.insertText", map[string]any{"text": in.Text}, nil)
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
		if err := s.client.call(ctx, "Input.dispatchKeyEvent", payload, nil); err != nil {
			return err
		}
		payload["type"] = "keyUp"
		delete(payload, "text")
		return s.client.call(ctx, "Input.dispatchKeyEvent", payload, nil)
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
	var out struct {
		Data string `json:"data"`
	}
	err := s.client.call(ctx, "Page.captureScreenshot", map[string]any{
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
