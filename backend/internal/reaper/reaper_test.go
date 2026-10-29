package reaper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// testClock is shared by the store and the reaper, so a sandbox's
// last_active_at and "now" move together deterministically.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeProvider records lifecycle calls without Docker or a directory.
type fakeProvider struct {
	mu         sync.Mutex
	states     map[string]contracts.SandboxState
	paused     []string
	destroyed  []string
	pauseErr   error
	destroyErr error
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{states: map[string]contracts.SandboxState{}}
}

func (f *fakeProvider) Ensure(_ context.Context, userID, projectID, _, _ string) (*contracts.Sandbox, error) {
	return &contracts.Sandbox{ID: "sbx_fake", UserID: userID, ProjectID: projectID, State: contracts.SandboxReady}, nil
}

func (f *fakeProvider) Get(_ context.Context, id string) (*contracts.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, ok := f.states[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &contracts.Sandbox{ID: id, State: state}, nil
}

func (f *fakeProvider) Pause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pauseErr != nil {
		return f.pauseErr
	}
	f.states[id] = contracts.SandboxPaused
	f.paused = append(f.paused, id)
	return nil
}

func (f *fakeProvider) Resume(_ context.Context, id string) (*contracts.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id] = contracts.SandboxReady
	return &contracts.Sandbox{ID: id, State: contracts.SandboxReady}, nil
}

func (f *fakeProvider) Destroy(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.states[id] = contracts.SandboxMissing
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *fakeProvider) Exec(context.Context, string, []string, string) (string, error) {
	return "", nil
}

func (f *fakeProvider) calls() (paused, destroyed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paused...), append([]string(nil), f.destroyed...)
}

func (f *fakeProvider) state(id string) contracts.SandboxState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[id]
}

type fakeSnapshots struct {
	mu        sync.Mutex
	calls     int
	err       error
	nilResult bool
}

func (f *fakeSnapshots) Take(_ context.Context, sb *contracts.Sandbox) (*store.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.nilResult {
		return nil, nil
	}
	return &store.Snapshot{
		ID: "snp_test", UserID: sb.UserID, SandboxID: sb.ID,
		Kind: "fs", ContentHash: "test", BlobKey: sb.ID + "/stamp/manifest.json",
	}, nil
}

func (f *fakeSnapshots) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type reaperEnv struct {
	t        *testing.T
	st       *store.Store
	clock    *testClock
	user     *contracts.User
	provider *fakeProvider
	snaps    *fakeSnapshots
	seq      int
}

func newReaperEnv(t *testing.T) *reaperEnv {
	t.Helper()
	clock := &testClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	st, err := store.OpenWithNow(context.Background(), ":memory:", clock.Now)
	if err != nil {
		t.Fatalf("store.OpenWithNow: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(context.Background(), store.NewUser{
		Email: "reaper@piforge.local", DisplayName: "reaper", PasswordHash: "hash",
		Role: contracts.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return &reaperEnv{
		t: t, st: st, clock: clock, user: user,
		provider: newFakeProvider(), snaps: &fakeSnapshots{},
	}
}

// sandbox creates one project + sandbox row whose last_active_at is exactly
// lastActive, and registers the same state with the fake provider.
func (e *reaperEnv) sandbox(state contracts.SandboxState, lastActive time.Time) *contracts.Sandbox {
	e.t.Helper()
	now := e.clock.Now()
	e.clock.Set(lastActive)
	e.seq++
	project, err := e.st.CreateProject(context.Background(), e.user.ID, store.NewProject{
		Name: fmt.Sprintf("project-%d", e.seq), DefaultBranch: "main",
	})
	if err != nil {
		e.t.Fatalf("CreateProject: %v", err)
	}
	id := fmt.Sprintf("sbx_%d", e.seq)
	sb, err := e.st.CreateSandbox(context.Background(), e.user.ID, store.NewSandbox{
		ID: id, ProjectID: project.ID, State: state, Provider: "fake", RootDir: "/tmp/" + id,
	})
	if err != nil {
		e.t.Fatalf("CreateSandbox: %v", err)
	}
	e.provider.states[id] = state
	e.clock.Set(now)
	if !sb.LastActiveAt.Equal(lastActive) {
		e.t.Fatalf("fixture last_active_at = %v, want %v", sb.LastActiveAt, lastActive)
	}
	return sb
}

func (e *reaperEnv) reaper(pauseAfter, reclaimAfter time.Duration) *Reaper {
	return New(Config{
		Store: e.st, Sandbox: e.provider, Snapshots: e.snaps,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        e.clock.Now,
		PauseAfter: pauseAfter, ReclaimAfter: reclaimAfter,
	})
}

func (e *reaperEnv) row(id string) *contracts.Sandbox {
	e.t.Helper()
	sb, err := e.st.Sandbox(context.Background(), e.user.ID, id)
	if err != nil {
		e.t.Fatalf("load sandbox %s: %v", id, err)
	}
	return sb
}

func TestReaperPausesOnlyReadyPastThreshold(t *testing.T) {
	e := newReaperEnv(t)
	now := e.clock.Now()
	oldReady := e.sandbox(contracts.SandboxReady, now.Add(-20*24*time.Hour))
	freshReady := e.sandbox(contracts.SandboxReady, now.Add(-time.Minute))
	exactReady := e.sandbox(contracts.SandboxReady, now.Add(-15*time.Minute))
	oldBusy := e.sandbox(contracts.SandboxBusy, now.Add(-20*24*time.Hour))
	oldPaused := e.sandbox(contracts.SandboxPaused, now.Add(-20*24*time.Hour))

	res, err := e.reaper(15*time.Minute, 0).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Paused != 1 || res.PauseFailures != 0 {
		t.Fatalf("result = %+v, want 1 pause", res)
	}
	paused, _ := e.provider.calls()
	if len(paused) != 1 || paused[0] != oldReady.ID {
		t.Fatalf("provider paused = %v, want [%s]", paused, oldReady.ID)
	}
	if got := e.row(oldReady.ID); got.State != contracts.SandboxPaused {
		t.Errorf("old ready row = %q, want PAUSED", got.State)
	}
	// The reaper's write is not activity: the idle clock must not move, or a
	// paused sandbox could never reach the reclaim threshold.
	if got := e.row(oldReady.ID); !got.LastActiveAt.Equal(now.Add(-20 * 24 * time.Hour)) {
		t.Errorf("last_active_at moved to %v", got.LastActiveAt)
	}
	if got := e.row(freshReady.ID); got.State != contracts.SandboxReady {
		t.Errorf("fresh ready row = %q, want READY", got.State)
	}
	if got := e.row(exactReady.ID); got.State != contracts.SandboxReady {
		t.Errorf("exactly-at-threshold row = %q, want READY (older-than is strict)", got.State)
	}
	if got := e.row(oldBusy.ID); got.State != contracts.SandboxBusy {
		t.Errorf("busy row = %q, want BUSY", got.State)
	}
	if got := e.row(oldPaused.ID); got.State != contracts.SandboxPaused {
		t.Errorf("already paused row = %q, want PAUSED", got.State)
	}
	if e.snaps.count() != 0 {
		t.Errorf("pause took %d snapshots, want 0", e.snaps.count())
	}
}

func TestReaperReclaimsOnlyPausedPastThreshold(t *testing.T) {
	e := newReaperEnv(t)
	now := e.clock.Now()
	oldPaused := e.sandbox(contracts.SandboxPaused, now.Add(-20*24*time.Hour))
	freshPaused := e.sandbox(contracts.SandboxPaused, now.Add(-time.Minute))
	oldReady := e.sandbox(contracts.SandboxReady, now.Add(-20*24*time.Hour))

	res, err := e.reaper(0, 14*24*time.Hour).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Reclaimed != 1 || res.SnapshotFailures != 0 || res.DestroyFailures != 0 {
		t.Fatalf("result = %+v, want 1 reclaim", res)
	}
	_, destroyed := e.provider.calls()
	if len(destroyed) != 1 || destroyed[0] != oldPaused.ID {
		t.Fatalf("provider destroyed = %v, want [%s]", destroyed, oldPaused.ID)
	}
	if e.snaps.count() != 1 {
		t.Fatalf("snapshots = %d, want 1", e.snaps.count())
	}
	if got := e.row(oldPaused.ID); got.State != contracts.SandboxMissing {
		t.Errorf("reclaimed row = %q, want MISSING", got.State)
	}
	if got := e.row(oldPaused.ID); !got.LastActiveAt.Equal(now.Add(-20 * 24 * time.Hour)) {
		t.Errorf("last_active_at moved to %v", got.LastActiveAt)
	}
	if got := e.row(freshPaused.ID); got.State != contracts.SandboxPaused {
		t.Errorf("fresh paused row = %q, want PAUSED", got.State)
	}
	if got := e.row(oldReady.ID); got.State != contracts.SandboxReady {
		t.Errorf("ready row = %q, want READY (reclaim never touches READY)", got.State)
	}
}

func TestReaperSnapshotFailureLeavesSandboxUntouched(t *testing.T) {
	e := newReaperEnv(t)
	now := e.clock.Now()
	sb := e.sandbox(contracts.SandboxPaused, now.Add(-20*24*time.Hour))
	e.snaps.err = errors.New("disk full")

	res, err := e.reaper(0, 14*24*time.Hour).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.SnapshotFailures != 1 || res.Reclaimed != 0 {
		t.Fatalf("result = %+v, want one snapshot failure and no reclaim", res)
	}
	if _, destroyed := e.provider.calls(); len(destroyed) != 0 {
		t.Fatalf("destroy called %v after a failed snapshot; must never happen", destroyed)
	}
	if got := e.provider.state(sb.ID); got != contracts.SandboxPaused {
		t.Errorf("provider state = %q, want PAUSED", got)
	}
	row := e.row(sb.ID)
	if row.State != contracts.SandboxPaused || row.Message != "" {
		t.Errorf("row = %+v, want unchanged PAUSED row", row)
	}
	if !row.LastActiveAt.Equal(now.Add(-20 * 24 * time.Hour)) {
		t.Errorf("last_active_at moved to %v", row.LastActiveAt)
	}

	// The failure is not sticky: the next tick retries and succeeds.
	e.snaps.err = nil
	res, err = e.reaper(0, 14*24*time.Hour).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if res.Reclaimed != 1 || e.snaps.count() != 2 {
		t.Fatalf("retry result = %+v, snapshots = %d", res, e.snaps.count())
	}
	if got := e.row(sb.ID); got.State != contracts.SandboxMissing {
		t.Errorf("row after retry = %q, want MISSING", got.State)
	}
}

func TestReaperNilSnapshotIsNotPermissionToDestroy(t *testing.T) {
	e := newReaperEnv(t)
	sb := e.sandbox(contracts.SandboxPaused, e.clock.Now().Add(-20*24*time.Hour))
	e.snaps.nilResult = true

	res, err := e.reaper(0, 14*24*time.Hour).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.SnapshotFailures != 1 || res.Reclaimed != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, destroyed := e.provider.calls(); len(destroyed) != 0 {
		t.Fatalf("destroy called after a nil snapshot: %v", destroyed)
	}
	if got := e.row(sb.ID); got.State != contracts.SandboxPaused {
		t.Errorf("row = %q, want PAUSED", got.State)
	}
}

func TestReaperDestroyFailureKeepsPausedForRetry(t *testing.T) {
	e := newReaperEnv(t)
	now := e.clock.Now()
	sb := e.sandbox(contracts.SandboxPaused, now.Add(-20*24*time.Hour))
	e.provider.destroyErr = errors.New("docker daemon busy")

	res, err := e.reaper(0, 14*24*time.Hour).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.DestroyFailures != 1 || res.Reclaimed != 0 {
		t.Fatalf("result = %+v, want one destroy failure", res)
	}
	if got := e.row(sb.ID); got.State != contracts.SandboxPaused {
		t.Errorf("row = %q, want PAUSED after failed destroy", got.State)
	}
	if got := e.provider.state(sb.ID); got != contracts.SandboxPaused {
		t.Errorf("provider state = %q, want PAUSED", got)
	}

	e.provider.destroyErr = nil
	res, err = e.reaper(0, 14*24*time.Hour).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if res.Reclaimed != 1 {
		t.Fatalf("retry result = %+v", res)
	}
	if got := e.row(sb.ID); got.State != contracts.SandboxMissing {
		t.Errorf("row after retry = %q, want MISSING", got.State)
	}
}

func TestReaperIsIdempotentWhenRunTwice(t *testing.T) {
	e := newReaperEnv(t)
	now := e.clock.Now()
	ready := e.sandbox(contracts.SandboxReady, now.Add(-20*time.Minute))
	paused := e.sandbox(contracts.SandboxPaused, now.Add(-20*24*time.Hour))
	r := e.reaper(15*time.Minute, 14*24*time.Hour)

	first, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if first.Paused != 1 || first.Reclaimed != 1 {
		t.Fatalf("first result = %+v, want 1 pause + 1 reclaim", first)
	}
	second, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if second != (Result{}) {
		t.Fatalf("second result = %+v, want zero (already PAUSED/MISSING)", second)
	}
	if e.snaps.count() != 1 {
		t.Errorf("snapshots = %d, want 1", e.snaps.count())
	}
	pausedIDs, destroyed := e.provider.calls()
	if len(pausedIDs) != 1 || len(destroyed) != 1 {
		t.Errorf("provider calls paused=%v destroyed=%v, want one each", pausedIDs, destroyed)
	}
	if got := e.row(ready.ID); got.State != contracts.SandboxPaused {
		t.Errorf("ready row = %q, want PAUSED", got.State)
	}
	if got := e.row(paused.ID); got.State != contracts.SandboxMissing {
		t.Errorf("paused row = %q, want MISSING", got.State)
	}
}

func TestReaperDisabledChangesNothing(t *testing.T) {
	e := newReaperEnv(t)
	now := e.clock.Now()
	ready := e.sandbox(contracts.SandboxReady, now.Add(-30*24*time.Hour))
	paused := e.sandbox(contracts.SandboxPaused, now.Add(-30*24*time.Hour))
	r := e.reaper(0, 0)

	res, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res != (Result{}) {
		t.Fatalf("result = %+v, want zero when disabled", res)
	}
	if pausedIDs, destroyed := e.provider.calls(); len(pausedIDs)+len(destroyed) != 0 {
		t.Errorf("provider calls paused=%v destroyed=%v, want none", pausedIDs, destroyed)
	}
	if got := e.row(ready.ID); got.State != contracts.SandboxReady {
		t.Errorf("ready row = %q, want READY", got.State)
	}
	if got := e.row(paused.ID); got.State != contracts.SandboxPaused {
		t.Errorf("paused row = %q, want PAUSED", got.State)
	}
	// Start is a no-op too, and must not block on a closed context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Start(ctx)
	if e.snaps.count() != 0 {
		t.Errorf("disabled reaper took %d snapshots", e.snaps.count())
	}
}

func TestReaperPauseFailureLeavesRowReady(t *testing.T) {
	e := newReaperEnv(t)
	ready := e.sandbox(contracts.SandboxReady, e.clock.Now().Add(-20*time.Minute))
	e.provider.pauseErr = errors.New("container runtime unavailable")

	res, err := e.reaper(15*time.Minute, 0).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.PauseFailures != 1 || res.Paused != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := e.row(ready.ID); got.State != contracts.SandboxReady {
		t.Errorf("row = %q, want READY after a failed pause", got.State)
	}
}
