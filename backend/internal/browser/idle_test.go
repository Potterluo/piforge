package browser

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The idle policy exists because the panel used to mount on every session page.
// These tests drive the manager with a fake clock and a fake launch, so the two
// product rules are checkable without a Chrome:
//
//   - concurrent viewers of one sandbox get exactly one browser;
//   - a browser with no attached client is stopped after the timeout, and one
//     that is being watched is not.

func TestConcurrentAcquireLaunchesOneBrowserPerSandbox(t *testing.T) {
	const callers = 8
	m := NewManager(Config{Binary: "chrome", IdleTimeout: time.Minute})
	t.Cleanup(m.CloseAll)

	var launches, inFlight int32
	release := make(chan struct{})
	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		if n := atomic.AddInt32(&inFlight, 1); n > 1 {
			t.Errorf("%d launches ran at once for one sandbox", n)
		}
		atomic.AddInt32(&launches, 1)
		<-release
		atomic.AddInt32(&inFlight, -1)
		return fakeSession(sandboxID), nil
	}

	profile := t.TempDir()
	sessions := make([]*Session, callers)
	releases := make([]func(), callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sessions[i], releases[i], errs[i] = m.Acquire(context.Background(), "sb_panel", profile)
		}(i)
	}
	close(start)

	waitFor(t, func() bool { return atomic.LoadInt32(&launches) == 1 }, "the launch to start")
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&launches); got != 1 {
		t.Fatalf("launches = %d, want 1 with %d concurrent panel selections", got, callers)
	}
	for i := range sessions {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if sessions[i] != sessions[0] {
			t.Fatalf("caller %d got a different session than caller 0", i)
		}
		if releases[i] == nil {
			t.Fatalf("caller %d got no release function", i)
		}
	}
	if m.Len() != 1 {
		t.Fatalf("manager holds %d browsers, want 1", m.Len())
	}

	// Everyone detaches: the browser is still running until the policy decides.
	for _, release := range releases {
		release()
	}
	if m.Len() != 1 {
		t.Fatalf("releasing clients stopped the browser immediately; the policy decides")
	}
}

func TestIdlePolicyStopsOnlyUnwatchedBrowsers(t *testing.T) {
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now := base
	var logBuf bytes.Buffer
	m := NewManager(Config{
		Binary:      "chrome",
		IdleTimeout: 30 * time.Second,
		Now:         func() time.Time { return now },
		Logger:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	})
	t.Cleanup(m.CloseAll)
	if got := m.IdleTimeout(); got != 30*time.Second {
		t.Fatalf("idle timeout = %s, want 30s", got)
	}

	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		session := fakeSession(sandboxID)
		session.Started = now
		return session, nil
	}

	session, release, err := m.Acquire(context.Background(), "sb_idle", t.TempDir())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// A viewer is attached: the policy must not take the browser away mid-use,
	// even long after the threshold, because switching a tab back must not pay
	// for a fresh Chromium.
	if stopped := m.SweepIdle(base.Add(10 * time.Minute)); stopped != 0 {
		t.Fatalf("swept %d browsers while one was attached", stopped)
	}
	if session.dead() {
		t.Fatal("the attached browser was stopped")
	}

	release()
	// Not yet: the threshold has not passed since the last client left.
	if stopped := m.SweepIdle(base.Add(29 * time.Second)); stopped != 0 {
		t.Fatalf("swept %d browsers before the idle threshold", stopped)
	}
	if session.dead() {
		t.Fatal("the browser was stopped before the idle threshold")
	}
	if m.Len() != 1 {
		t.Fatalf("manager holds %d browsers, want 1", m.Len())
	}

	// A brand-new attach within the window resets the clock: leaving the panel
	// open and coming back must not race the reaper.
	now = base.Add(10 * time.Minute)
	if _, again, err := m.Acquire(context.Background(), "sb_idle", t.TempDir()); err != nil {
		t.Fatalf("re-Acquire: %v", err)
	} else {
		again()
	}
	if stopped := m.SweepIdle(base.Add(10*time.Minute + 20*time.Second)); stopped != 0 {
		t.Fatalf("swept %d browsers inside the window opened by a new attach", stopped)
	}

	// Past the threshold, measured from the *last* release.
	if stopped := m.SweepIdle(base.Add(10*time.Minute + 31*time.Second)); stopped != 1 {
		t.Fatalf("swept %d browsers, want 1", stopped)
	}
	if !session.dead() {
		t.Fatal("the idle browser is still alive")
	}
	if m.Len() != 0 {
		t.Fatalf("manager still holds %d browsers", m.Len())
	}
	if ids := m.SandboxIDs(); len(ids) != 0 {
		t.Fatalf("SandboxIDs = %v, want empty", ids)
	}

	logged := logBuf.String()
	if strings.Count(logged, "stopped an idle sandbox browser") != 1 {
		t.Fatalf("want exactly one idle-stop log line, got:\n%s", logged)
	}
	if !strings.Contains(logged, "sandbox=sb_idle") {
		t.Fatalf("idle-stop log does not name the sandbox:\n%s", logged)
	}
}

// A browser that nobody ever attached to is still collected, measured from its
// launch. This is the shape of the old leak seen from the server: a page load
// asked for tabs, got a browser, and the request ended.
func TestIdlePolicyStopsABrowserThatWasNeverWatched(t *testing.T) {
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	m := NewManager(Config{Binary: "chrome", IdleTimeout: time.Minute, Now: func() time.Time { return base }})
	t.Cleanup(m.CloseAll)
	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		session := fakeSession(sandboxID)
		session.Started = base
		return session, nil
	}

	if _, release, err := m.Acquire(context.Background(), "sb_never", t.TempDir()); err != nil {
		t.Fatalf("Acquire: %v", err)
	} else {
		release()
	}
	if stopped := m.SweepIdle(base.Add(30 * time.Second)); stopped != 0 {
		t.Fatalf("swept %d browsers early", stopped)
	}
	if stopped := m.SweepIdle(base.Add(61 * time.Second)); stopped != 1 {
		t.Fatalf("swept %d browsers, want 1", stopped)
	}
}

func TestSandboxIDsListsRunningBrowsers(t *testing.T) {
	m := NewManager(Config{Binary: "chrome", IdleTimeout: time.Minute})
	t.Cleanup(m.CloseAll)
	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		return fakeSession(sandboxID), nil
	}
	for _, id := range []string{"sb_b", "sb_a"} {
		_, release, err := m.Acquire(context.Background(), id, t.TempDir())
		if err != nil {
			t.Fatalf("Acquire(%s): %v", id, err)
		}
		release()
	}
	ids := m.SandboxIDs()
	if len(ids) != 2 || ids[0] != "sb_a" || ids[1] != "sb_b" {
		t.Fatalf("SandboxIDs = %v, want [sb_a sb_b]", ids)
	}
}
