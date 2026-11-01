package quotaenforcer

import (
	"testing"
	"time"
)

// The decision is the whole contract of the enforcer, so it is pinned as a pure
// function: a table of readings in, an action out.
func TestDecide(t *testing.T) {
	const limit = 2 << 30
	p := Policy{LimitBytes: limit, Root: "/workspace"}

	cases := []struct {
		name    string
		policy  Policy
		used    int64
		over    int
		grew    bool
		want    Action
		because string
	}{
		{
			name: "under budget is never an action", policy: p, used: limit - 1,
			over: 5, grew: true, want: ActionNone,
		},
		{
			name: "exactly at the budget is still under it", policy: p, used: limit,
			over: 0, grew: true, want: ActionNone,
		},
		{
			name: "the first reading over freezes, it does not kill", policy: p, used: limit + 1,
			over: 1, grew: true, want: ActionFreeze,
			because: "SIGSTOP is reversible and stops the bytes immediately",
		},
		{
			name: "still over but not growing stays frozen, never killed", policy: p, used: limit + 1,
			over: 60, grew: false, want: ActionFreeze,
			because: "a frozen writer cannot be the one growing the tree",
		},
		{
			name: "a new writer after the escalation threshold is killed", policy: p, used: limit * 2,
			over: DefaultKillAfter, grew: true, want: ActionKill,
		},
		{
			name: "one reading before the threshold is still a freeze", policy: p, used: limit * 2,
			over: DefaultKillAfter - 1, grew: true, want: ActionFreeze,
		},
		{
			name: "an explicit threshold is honoured", policy: Policy{LimitBytes: limit, KillAfter: 2},
			used: limit + 1, over: 2, grew: true, want: ActionKill,
		},
		{
			name: "no limit means no enforcement", policy: Policy{LimitBytes: 0}, used: limit * 100,
			over: 99, grew: true, want: ActionNone,
		},
		{
			name: "a zero reading count is treated as the first", policy: p, used: limit + 1,
			over: 0, grew: false, want: ActionFreeze,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.policy, tc.used, tc.over, tc.grew)
			if got != tc.want {
				reason := tc.because
				if reason == "" {
					reason = "the table says so"
				}
				t.Fatalf("Decide(used=%d, over=%d, grew=%v) = %s, want %s (%s)",
					tc.used, tc.over, tc.grew, got, tc.want, reason)
			}
		})
	}
}

// The interval is where the cost lives: cheap while the walk is cheap, backed off
// when the tree is large enough that walking it stops being free.
func TestNextInterval(t *testing.T) {
	cases := []struct {
		name string
		base time.Duration
		walk time.Duration
		want time.Duration
	}{
		{"a fast walk keeps the base interval", 250 * time.Millisecond, 500 * time.Microsecond, 250 * time.Millisecond},
		{"a walk at the slow threshold is still free", 250 * time.Millisecond, SlowWalk, 250 * time.Millisecond},
		{"a slow walk backs off to three times its cost", time.Second, 400 * time.Millisecond, 1200 * time.Millisecond},
		{"the base interval is a floor", 250 * time.Millisecond, 100 * time.Millisecond, 300 * time.Millisecond},
		{"the back-off is capped", time.Second, 10 * time.Second, MaxInterval},
		{"a zero base uses the default", 0, time.Millisecond, DefaultInterval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextInterval(tc.base, tc.walk); got != tc.want {
				t.Fatalf("NextInterval(%s, %s) = %s, want %s", tc.base, tc.walk, got, tc.want)
			}
		})
	}
}

// The defaults are part of the claim the docs make about the overshoot, so they
// are pinned: a longer default interval would silently widen the measured bound.
func TestDefaultsAreTheDocumentedOnes(t *testing.T) {
	if DefaultInterval != 100*time.Millisecond {
		t.Fatalf("DefaultInterval = %s, want 100ms (the measured overshoot depends on it)", DefaultInterval)
	}
	if MaxInterval != 5*time.Second {
		t.Fatalf("MaxInterval = %s, want 5s", MaxInterval)
	}
	if DefaultKillAfter != 12 {
		t.Fatalf("DefaultKillAfter = %d, want 12", DefaultKillAfter)
	}
}

// A policy with no limit is refused rather than looping forever on a no-op: the
// hook that starts it computes the limit from RLIMIT_FSIZE, and a zero there means
// the container was started without a disk cap, which is a deployment mistake
// worth saying out loud.
func TestRunRefusesAZeroLimit(t *testing.T) {
	if err := Run(nil, Policy{Root: "/workspace"}, nil); err == nil {
		t.Fatal("Run with no limit did not fail")
	}
}
