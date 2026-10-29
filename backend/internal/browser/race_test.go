package browser

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests are about the launch handshake, not about Chrome. A launch is
// replaced by a function so the interesting states — one attempt in flight,
// failure, two sandboxes at once — can be produced deterministically without a
// browser on the machine.

// fakeSession is a Session that is alive without a process behind it.
func fakeSession(sandboxID string) *Session {
	return &Session{
		SandboxID: sandboxID,
		Profile:   "/fake/" + sandboxID,
		PID:       int(atomic.AddInt32(&fakePIDs, 1)),
		Started:   time.Now(),
		stopped:   make(chan struct{}),
		tabs:      map[string]*cdp{},
	}
}

var fakePIDs int32

// TestConcurrentEnsureLaunchesASandboxOnce is the defect, in one place: eight
// callers ask for the same sandbox's browser while the first launch is still
// running. One process is started, one session is produced, and every caller
// gets it. Before the per-sandbox single-flight, each caller launched its own
// Chromium onto the same profile; Chromium's singleton let one win and the
// others reported "devtools never became ready".
func TestConcurrentEnsureLaunchesASandboxOnce(t *testing.T) {
	const callers = 8

	m := NewManager(Config{Binary: "chrome"})
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
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sessions[i], errs[i] = m.Ensure(context.Background(), "sb_race", profile)
		}(i)
	}
	close(start)

	// Let the one launch start, then give the other callers time to reach Ensure.
	// A caller that arrives after the launch finished is still correct — it
	// reuses the cached session — so this only has to be long enough to make the
	// piling-up case the common one.
	waitFor(t, func() bool { return atomic.LoadInt32(&launches) == 1 }, "the launch to start")
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&launches); got != 1 {
		t.Fatalf("launches = %d, want 1 with %d concurrent callers", got, callers)
	}
	for i := range sessions {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if sessions[i] != sessions[0] {
			t.Fatalf("caller %d got a different session than caller 0", i)
		}
		if sessions[i].dead() {
			t.Fatalf("caller %d was handed a dead session", i)
		}
	}
	if m.Len() != 1 {
		t.Fatalf("manager holds %d browsers, want 1", m.Len())
	}
}

// TestEveryCallerOfAFailedLaunchGetsTheTypedError covers the other half of the
// race: when the launch that the others are waiting on still fails, they must
// get a typed failure rather than a session attached to a browser that is not
// there. No failed attempt is recorded, so the sandbox is not poisoned.
func TestEveryCallerOfAFailedLaunchGetsTheTypedError(t *testing.T) {
	const callers = 3

	m := NewManager(Config{Binary: "chrome"})
	t.Cleanup(m.CloseAll)

	var failing atomic.Bool
	failing.Store(true)
	var launches int32
	release := make(chan struct{})
	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		atomic.AddInt32(&launches, 1)
		<-release
		if failing.Load() {
			return nil, &LaunchError{
				SandboxID: sandboxID,
				Stage:     StageDevTools,
				Err:       errors.New("devtools never became ready: connection refused"),
			}
		}
		return fakeSession(sandboxID), nil
	}

	profile := t.TempDir()
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = m.Ensure(context.Background(), "sb_flaky", profile)
		}(i)
	}
	close(start)
	waitFor(t, func() bool { return atomic.LoadInt32(&launches) >= 1 }, "the first launch to start")
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		var launchErr *LaunchError
		if !errors.As(err, &launchErr) {
			t.Fatalf("caller %d got %v, want a *LaunchError", i, err)
		}
		if launchErr.SandboxID != "sb_flaky" || launchErr.Stage != StageDevTools {
			t.Fatalf("caller %d got %+v", i, launchErr)
		}
	}
	if _, ok := m.Get("sb_flaky"); ok {
		t.Fatal("a failed launch was recorded as a live session")
	}

	// The rendezvous is not a cache: the next call launches again and succeeds.
	failing.Store(false)
	session, err := m.Ensure(context.Background(), "sb_flaky", profile)
	if err != nil {
		t.Fatalf("the call after a failed launch: %v", err)
	}
	if session == nil || m.Len() != 1 {
		t.Fatalf("after the retry: session=%v len=%d", session, m.Len())
	}
}

// TestTwoSandboxesLaunchInParallel is the other side of the requirement: the
// lock is per sandbox. A global launch lock would still pass the tests above,
// and would make every sandbox wait for every other one.
func TestTwoSandboxesLaunchInParallel(t *testing.T) {
	m := NewManager(Config{Binary: "chrome"})
	t.Cleanup(m.CloseAll)

	arrived := make(chan string, 2)
	release := make(chan struct{})
	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		arrived <- sandboxID
		<-release
		return fakeSession(sandboxID), nil
	}

	profile := t.TempDir()
	results := make(chan error, 2)
	for _, id := range []string{"sb_one", "sb_two"} {
		go func(id string) {
			_, err := m.Ensure(context.Background(), id, profile)
			results <- err
		}(id)
	}

	// Both launches must be in flight at the same time, which is only possible if
	// the first one does not hold a lock the second one needs.
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-arrived:
			seen[id] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %v started while the other launch was in flight; the lock is not per sandbox", seen)
		}
	}
	close(release)

	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("Ensure: %v", err)
		}
	}
	if m.Len() != 2 {
		t.Fatalf("manager holds %d browsers, want 2", m.Len())
	}
}

// A session whose browser has exited must not be handed out again: the next
// call replaces it instead of returning a dead handle.
func TestADeadSessionIsReplacedNotReused(t *testing.T) {
	m := NewManager(Config{Binary: "chrome"})
	t.Cleanup(m.CloseAll)

	var launches int32
	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		atomic.AddInt32(&launches, 1)
		return fakeSession(sandboxID), nil
	}

	profile := t.TempDir()
	first, err := m.Ensure(context.Background(), "sb_dead", profile)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	first.Stop()

	second, err := m.Ensure(context.Background(), "sb_dead", profile)
	if err != nil {
		t.Fatalf("Ensure after the browser died: %v", err)
	}
	if second == first {
		t.Fatal("Ensure returned the dead session")
	}
	if got := atomic.LoadInt32(&launches); got != 2 {
		t.Fatalf("launches = %d, want 2 (one replacement)", got)
	}
}

// TestABrokenDevtoolsConnectionIsReplaced covers the other "dead browser" shape:
// the process is still running but its devtools socket is gone, so every
// command fails. Before dead() considered the connection, Ensure handed the
// same broken handle back forever, which a user saw as the panel answering
// "浏览器暂时不可用" on every reconnect.
func TestABrokenDevtoolsConnectionIsReplaced(t *testing.T) {
	fake := newFakeCDP(t)
	client, err := dialCDP(context.Background(), fake.wsURL, nil)
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	client.close()
	waitFor(t, client.gone, "the devtools connection to close")

	m := NewManager(Config{Binary: "chrome"})
	t.Cleanup(m.CloseAll)

	var launches int32
	m.start = func(_ context.Context, sandboxID, _ string) (*Session, error) {
		atomic.AddInt32(&launches, 1)
		return fakeSession(sandboxID), nil
	}

	broken := &Session{
		SandboxID: "sb_broken",
		Started:   time.Now(),
		client:    client,
		stopped:   make(chan struct{}),
		tabs:      map[string]*cdp{},
	}
	m.mu.Lock()
	m.sessions["sb_broken"] = broken
	m.mu.Unlock()

	if got, ok := m.Get("sb_broken"); ok {
		t.Fatalf("Get returned a session whose devtools connection ended: %v", got)
	}

	session, err := m.Ensure(context.Background(), "sb_broken", t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if session == broken {
		t.Fatal("Ensure reused the broken session")
	}
	if got := atomic.LoadInt32(&launches); got != 1 {
		t.Fatalf("launches = %d, want 1 replacement", got)
	}
	select {
	case <-broken.stopped:
	default:
		t.Fatal("the broken session was not stopped; its process would hold the profile")
	}
}

// waitFor polls a condition until it holds or the test gives up.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
