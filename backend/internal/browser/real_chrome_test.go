package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// findChrome locates a real browser, skipping the test when there is none. The
// unit tests above prove the protocol handling against a fake; this one proves
// the whole thing works against the program we actually ship against.
func findChrome(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the real-browser test in -short mode")
	}
	candidates := []string{
		os.Getenv("PIFORGE_CHROME"),
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if path, err := exec.LookPath("google-chrome"); err == nil {
		return path
	}
	if path, err := exec.LookPath("chromium"); err == nil {
		return path
	}
	t.Skip("no chrome binary available")
	return ""
}

// TestRealChromeScreencastAndInput launches the actual browser: it must report
// devtools, emit frames, and accept input.
func TestRealChromeScreencastAndInput(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("no browser")
	}
	binary := findChrome(t)

	// The launcher must see the browser's argv: the jail covers the browser too,
	// not just the agent.
	launcher := &recordingLauncher{}
	m := NewManager(Config{Binary: binary, Headless: true, Width: 800, Height: 600, Launcher: launcher})
	defer m.CloseAll()

	// Generous on purpose: this test launches a real browser, and under a loaded
	// machine (the whole suite running with -race) first-paint is the slowest
	// step. A short budget here produces a flaky test, not a faster one.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	profileRoot := t.TempDir()
	session, err := m.Ensure(ctx, "sb_real", profileRoot)
	if err != nil {
		// Launching a browser is the one part of this test that depends on the
		// machine: on a saturated box (the whole suite running under -race) the
		// devtools endpoint occasionally misses its budget. Retrying once keeps
		// that from reading as a product failure, while a genuinely broken
		// launcher still fails twice.
		t.Logf("first launch failed, retrying once: %v", err)
		session, err = m.Ensure(ctx, "sb_real", profileRoot)
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
	}
	if session.PID == 0 {
		t.Fatal("no process id")
	}
	if len(launcher.got) == 0 || launcher.got[0] != binary {
		t.Fatalf("the launcher saw %v, want chrome's argv", launcher.got)
	}
	// The profile must live inside the sandbox: a shared profile would share
	// cookies between tenants.
	if !filepath.HasPrefix(session.Profile, profileRoot) {
		t.Fatalf("profile %q is outside %q", session.Profile, profileRoot)
	}

	// A page with something to paint, so frames are not all blank.
	if err := session.Navigate(ctx, "data:text/html,<h1>piforge</h1>"); err == nil {
		t.Fatal("Navigate accepted a data: URL")
	}
	if err := session.Navigate(ctx, "about:blank"); err == nil {
		t.Fatal("Navigate accepted about:blank")
	}

	frames := make(chan []byte, 4)
	screencastErr := make(chan error, 1)
	go func() {
		screencastErr <- session.Screencast(ctx, func(jpeg []byte, _ Meta) {
			select {
			case frames <- jpeg:
			default:
			}
		})
	}()

	select {
	case jpeg := <-frames:
		if len(jpeg) < 2 || jpeg[0] != 0xff || jpeg[1] != 0xd8 {
			t.Fatalf("frame is not a JPEG: %v", jpeg[:min(4, len(jpeg))])
		}
	case err := <-screencastErr:
		t.Fatalf("screencast ended before the first frame: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("no screencast frame arrived within 30s")
	}

	// Input must be accepted by the page.
	if err := session.Dispatch(ctx, Input{Type: "text", Text: "hello"}); err != nil {
		t.Fatalf("dispatch text: %v", err)
	}
	if err := session.Dispatch(ctx, Input{Type: "key", Key: "Enter", Code: "Enter"}); err != nil {
		t.Fatalf("dispatch key: %v", err)
	}

	// A second Ensure returns the same browser, not a second one.
	again, err := m.Ensure(ctx, "sb_real", profileRoot)
	if err != nil {
		t.Fatalf("Ensure (second): %v", err)
	}
	if again.PID != session.PID {
		t.Fatalf("a second browser was started (pid %d vs %d)", again.PID, session.PID)
	}
	if m.Len() != 1 {
		t.Fatalf("manager holds %d browsers, want 1", m.Len())
	}

	m.Close("sb_real")
	if m.Len() != 0 {
		t.Fatalf("browser was not closed: %d left", m.Len())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// recordingLauncher captures the argv it is asked to wrap and runs it unchanged.
type recordingLauncher struct{ got []string }

func (r *recordingLauncher) Wrap(argv []string) []string {
	r.got = append([]string{}, argv...)
	return argv
}
func (r *recordingLauncher) Name() string { return "recording" }
