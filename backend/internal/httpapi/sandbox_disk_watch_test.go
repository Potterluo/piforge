package httpapi

import (
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/diskquota"
)

// The watchdog's cadence is the part of the fix that the 3.27x overshoot
// measurement argued for, so it is pinned here: a run in flight is measured at
// the fast interval, an idle sandbox at the slow one, and a measurement schedules
// the next one instead of measuring on every tick.
func TestDiskWatchCadenceIsAdaptive(t *testing.T) {
	s := &server{}
	s.diskWatchState = map[string]diskWatch{}

	if got := s.diskWatchInterval(true); got != diskquota.ActiveWatchInterval {
		t.Fatalf("active interval = %s, want %s", got, diskquota.ActiveWatchInterval)
	}
	if got := s.diskWatchInterval(false); got != diskquota.IdleWatchInterval {
		t.Fatalf("idle interval = %s, want %s", got, diskquota.IdleWatchInterval)
	}
	if diskquota.ActiveWatchInterval >= diskquota.IdleWatchInterval {
		t.Fatalf("the active interval (%s) must be shorter than the idle one (%s)",
			diskquota.ActiveWatchInterval, diskquota.IdleWatchInterval)
	}
}

// The scheduling helpers are what make the adaptive interval real: without them
// every tick would measure every sandbox and the interval would be a comment.
func TestDiskWatchScheduling(t *testing.T) {
	s := &server{}
	s.diskWatchState = map[string]diskWatch{}
	now := time.Now()

	// A sandbox nobody has seen is due immediately.
	if !s.diskWatchDue("sb_1", now) {
		t.Fatal("a sandbox with no state is not due")
	}

	// After a measurement it is not due until its interval elapses.
	s.diskWatchSchedule("sb_1", now.Add(2*time.Second))
	if s.diskWatchDue("sb_1", now.Add(1999*time.Millisecond)) {
		t.Fatal("a sandbox was due before its interval had elapsed")
	}
	if !s.diskWatchDue("sb_1", now.Add(2*time.Second)) {
		t.Fatal("a sandbox was not due when its interval had elapsed")
	}

	// A reset forgets the strikes but keeps the queue position, so a sandbox that
	// went under budget does not get measured on the very next tick.
	s.diskWatchState["sb_1"] = diskWatch{strikes: 3, lastBytes: 1 << 30, nextAt: now}
	s.diskWatchReset("sb_1", now.Add(10*time.Second))
	state := s.diskWatchState["sb_1"]
	if state.strikes != 0 || state.lastBytes != 0 {
		t.Fatalf("reset left strikes=%d lastBytes=%d, want 0 and 0", state.strikes, state.lastBytes)
	}
	if s.diskWatchDue("sb_1", now.Add(9*time.Second)) {
		t.Fatal("a reset sandbox was due before its new interval had elapsed")
	}
}

// The enforcer's interval and the platform's are a pair: the platform loop is the
// slow authoritative one, so it must never be the *only* thing standing between a
// burst and the disk. This pins the relationship the docs state.
func TestPlatformWatchIsSlowerThanTheEnforcer(t *testing.T) {
	if diskquota.ActiveWatchInterval <= 250*time.Millisecond {
		t.Fatalf("the platform's active interval (%s) is not slower than the in-sandbox enforcer's 250ms; "+
			"one of the two numbers changed and the docs no longer describe the system",
			diskquota.ActiveWatchInterval)
	}
}
