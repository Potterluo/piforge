package liveness

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRegistry models pi.Runtime's in-process registry.
type fakeRegistry struct {
	mu    sync.Mutex
	alive map[string]bool
}

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{alive: map[string]bool{}} }

func (r *fakeRegistry) Alive(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alive[sessionID]
}

func (r *fakeRegistry) set(sessionID string, alive bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[sessionID] = alive
}

// TestProcessFollowsTheRegistry is the local provider's rule: the in-process
// registry is the whole truth, and it never errors.
func TestProcessFollowsTheRegistry(t *testing.T) {
	ctx := context.Background()
	registry := newFakeRegistry()
	probe := NewProcess(registry)

	alive, err := probe.Alive(ctx, "ses_live")
	if err != nil {
		t.Fatalf("Alive (empty registry): %v", err)
	}
	if alive {
		t.Fatal("an empty registry reported a live session")
	}

	registry.set("ses_live", true)
	alive, err = probe.Alive(ctx, "ses_live")
	if err != nil {
		t.Fatalf("Alive: %v", err)
	}
	if !alive {
		t.Fatal("the registry says alive, the probe says dead")
	}

	registry.set("ses_live", false)
	alive, err = probe.Alive(ctx, "ses_live")
	if err != nil {
		t.Fatalf("Alive (stopped): %v", err)
	}
	if alive {
		t.Fatal("a stopped session was reported alive")
	}
}

// TestProcessWithoutARegistryIsAnError keeps "no authority" out of the "dead"
// bucket: an unwired probe must not be able to interrupt anything.
func TestProcessWithoutARegistryIsAnError(t *testing.T) {
	probe := NewProcess(nil)
	alive, err := probe.Alive(context.Background(), "ses_1")
	if err == nil {
		t.Fatal("a probe with no registry answered instead of erroring")
	}
	if alive {
		t.Fatal("a probe with no registry said alive")
	}
}

// TestCompositePrefersAnyProofOfLife is the restart scenario in one test: the
// new process's registry is empty, the sandbox still holds a fresh marker, and
// the composite must answer alive.
func TestCompositePrefersAnyProofOfLife(t *testing.T) {
	ctx := context.Background()
	registry := newFakeRegistry()
	notFound := ProbeFunc(func(context.Context, string) (bool, error) {
		return false, nil
	})
	sandboxAlive := ProbeFunc(func(context.Context, string) (bool, error) {
		return true, nil
	})

	probe := NewComposite(NewProcess(registry), sandboxAlive, notFound)
	alive, err := probe.Alive(ctx, "ses_remote")
	if err != nil {
		t.Fatalf("Alive: %v", err)
	}
	if !alive {
		t.Fatal("one authoritative source said alive and the composite said dead")
	}
}

// TestCompositeTurnsAnUnansweredSourceIntoUnknown: no source proves life, one
// cannot answer. The composite must report an error (unknown), never false
// (dead), because false interrupts a run.
func TestCompositeTurnsAnUnansweredSourceIntoUnknown(t *testing.T) {
	ctx := context.Background()
	registry := newFakeRegistry()
	unreachable := ProbeFunc(func(context.Context, string) (bool, error) {
		return false, errors.New("sandbox unreachable")
	})

	probe := NewComposite(NewProcess(registry), unreachable)
	alive, err := probe.Alive(ctx, "ses_remote")
	if err == nil {
		t.Fatal("an unreachable source was silently turned into an answer")
	}
	if alive {
		t.Fatal("an unanswered probe reported alive")
	}
	if !strings.Contains(err.Error(), "sandbox unreachable") {
		t.Fatalf("the composite dropped the underlying reason: %v", err)
	}
}

// TestCompositeIsDeadOnlyWhenEverySourceSaysDead is the one case that may
// interrupt.
func TestCompositeIsDeadOnlyWhenEverySourceSaysDead(t *testing.T) {
	ctx := context.Background()
	dead := ProbeFunc(func(context.Context, string) (bool, error) { return false, nil })
	probe := NewComposite(dead, dead)
	alive, err := probe.Alive(ctx, "ses_1")
	if err != nil {
		t.Fatalf("Alive: %v", err)
	}
	if alive {
		t.Fatal("every source said dead and the composite said alive")
	}
}

// TestCompositeWithNoSourcesIsUnknown keeps a misconfigured deployment safe.
func TestCompositeWithNoSourcesIsUnknown(t *testing.T) {
	probe := NewComposite(nil, nil)
	if _, err := probe.Alive(context.Background(), "ses_1"); err == nil {
		t.Fatal("a composite with no sources answered instead of erroring")
	}
}

// TestDescribeNamesTheAuthority is what the startup log prints, and an operator
// reading "composite[process-registry sandbox-marker(sandbox-api)]" knows which
// two things answered.
func TestDescribeNamesTheAuthority(t *testing.T) {
	registry := newFakeRegistry()
	process := NewProcess(registry)
	if got := Describe(process); got != "process-registry" {
		t.Fatalf("Describe(process) = %q", got)
	}
	sandbox := NewSandbox(SandboxConfig{Reader: &fakeMarkerReader{}})
	if got := Describe(sandbox); got != "sandbox-marker(fake)" {
		t.Fatalf("Describe(sandbox) = %q", got)
	}
	composite := NewComposite(process, sandbox)
	if got := Describe(composite); got != "composite[process-registry sandbox-marker(fake)]" {
		t.Fatalf("Describe(composite) = %q", got)
	}
	if got := Describe(nil); got != "none" {
		t.Fatalf("Describe(nil) = %q", got)
	}
}

// fakeMarkerReader is the minimal MarkerReader: a fixed answer or a fixed error.
type fakeMarkerReader struct {
	state MarkerState
	err   error
	calls int
}

func (f *fakeMarkerReader) Describe() string { return "fake" }

func (f *fakeMarkerReader) Marker(context.Context, string) (MarkerState, error) {
	f.calls++
	if f.err != nil {
		return MarkerState{}, f.err
	}
	return f.state, nil
}

// TestSandboxMarkerLease is the rule the probe exists for: a fresh marker is a
// live agent, a stale one is a dead agent, and a reader failure is unknown
// rather than dead.
func TestSandboxMarkerLease(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		state     MarkerState
		err       error
		ttl       time.Duration
		wantAlive bool
		wantErr   bool
	}{
		{name: "fresh", state: MarkerState{Present: true, ModTime: now.Add(-5 * time.Second)}, wantAlive: true},
		{name: "exactly at the lease end", state: MarkerState{Present: true, ModTime: now.Add(-DefaultTTL)}, wantAlive: true},
		{name: "stale", state: MarkerState{Present: true, ModTime: now.Add(-DefaultTTL - time.Second)}, wantAlive: false},
		{name: "absent", state: MarkerState{}, wantAlive: false},
		{name: "present but undated", state: MarkerState{Present: true}, wantErr: true},
		{name: "future", state: MarkerState{Present: true, ModTime: now.Add(time.Minute)}, wantAlive: true},
		{name: "reader error", err: errors.New("sandbox unreachable"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := NewSandbox(SandboxConfig{
				Reader: &fakeMarkerReader{state: tc.state, err: tc.err},
				Now:    func() time.Time { return now },
			})
			alive, err := probe.Alive(context.Background(), "ses_lease")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Alive = (%v, nil), want an error (unknown)", alive)
				}
				return
			}
			if err != nil {
				t.Fatalf("Alive: %v", err)
			}
			if alive != tc.wantAlive {
				t.Fatalf("Alive = %v, want %v", alive, tc.wantAlive)
			}
		})
	}
}

// TestSandboxMarkerPathStaysInsideTheWorkspace: the id comes from the database,
// and a marker must never be able to point outside the sandbox it belongs to.
func TestSandboxMarkerPathStaysInsideTheWorkspace(t *testing.T) {
	good, err := MarkerPath("/data/sandboxes/u/p", "ses_01MABC")
	if err != nil {
		t.Fatalf("MarkerPath: %v", err)
	}
	if filepath.Base(good) != "ses_01MABC" || !strings.Contains(good, filepath.FromSlash(MarkerDir)) {
		t.Fatalf("MarkerPath = %q", good)
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "ses 1", strings.Repeat("a", 129)} {
		if _, err := MarkerPath("/data/sandboxes/u/p", bad); err == nil {
			t.Fatalf("MarkerPath accepted %q", bad)
		}
	}
}
