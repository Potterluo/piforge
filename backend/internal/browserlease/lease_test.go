package browserlease

import (
	"errors"
	"testing"
	"time"
)

// fakeClock is the whole reason the manager takes a clock: expiry and the
// remaining time are the product behaviour, and neither should need a sleep.
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time          { return c.at }
func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newFake() *fakeClock {
	return &fakeClock{at: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func owner(session string) Owner {
	return Owner{UserID: "usr_dev", UserName: "dev", SessionID: session, SessionLabel: session}
}

// FR-11.3: the first takeover is granted, and a second principal is refused
// with the holder and the remaining time instead of silently sharing the page.
func TestClaimIsExclusiveAndNamesTheHolder(t *testing.T) {
	clock := newFake()
	m := New(30*time.Second, clock.now)

	first, err := m.Claim("sbx_1", owner("sess_a"))
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first.Owner.SessionID != "sess_a" {
		t.Fatalf("holder = %+v", first.Owner)
	}
	if got := first.Remaining(clock.now()); got != 30*time.Second {
		t.Fatalf("remaining = %v, want the full TTL", got)
	}

	clock.advance(10 * time.Second)
	_, err = m.Claim("sbx_1", owner("sess_b"))
	if err == nil {
		t.Fatal("a second session took over a browser somebody else is driving")
	}
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("refusal is not a *BusyError: %T %v", err, err)
	}
	if busy.Holder.Owner.SessionID != "sess_a" || busy.Holder.Owner.UserName != "dev" {
		t.Fatalf("refusal named the wrong holder: %+v", busy.Holder.Owner)
	}
	if busy.Remaining != 20*time.Second {
		t.Fatalf("remaining = %v, want 20s", busy.Remaining)
	}
	if busy.Error() == "" {
		t.Fatal("a busy refusal must explain itself")
	}

	// The holder itself may claim again (a retry, a second tab of the same
	// user+session): that is a renewal, not a conflict.
	again, err := m.Claim("sbx_1", owner("sess_a"))
	if err != nil {
		t.Fatalf("holder could not renew: %v", err)
	}
	if !again.ExpiresAt.After(first.ExpiresAt) {
		t.Fatalf("re-claim did not extend the lease: %v -> %v", first.ExpiresAt, again.ExpiresAt)
	}

	// Scopes are independent: another sandbox is another browser.
	if _, err := m.Claim("sbx_2", owner("sess_b")); err != nil {
		t.Fatalf("a different sandbox was refused: %v", err)
	}
}

// A patch that stops heartbeating (closed tab, dead network) must free the
// browser on its own; otherwise one crashed viewer locks everybody out.
func TestExpiryFreesTheBrowser(t *testing.T) {
	clock := newFake()
	m := New(30*time.Second, clock.now)

	if _, err := m.Claim("sbx_1", owner("sess_a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	clock.advance(29 * time.Second)
	if _, err := m.Claim("sbx_1", owner("sess_b")); err == nil {
		t.Fatal("the lease was stealable before its TTL elapsed")
	}

	clock.advance(2 * time.Second)
	if _, stillHeld := m.Holder("sbx_1"); stillHeld {
		t.Fatal("an expired lease is still reported as held")
	}
	second, err := m.Claim("sbx_1", owner("sess_b"))
	if err != nil {
		t.Fatalf("claim after expiry: %v", err)
	}
	if second.Owner.SessionID != "sess_b" {
		t.Fatalf("the expired holder kept the browser: %+v", second.Owner)
	}
}

// A heartbeat extends only for the owner: it must not be a back door that lets
// a refused client take over by polling.
func TestHeartbeatExtendsOnlyForTheOwner(t *testing.T) {
	clock := newFake()
	m := New(30*time.Second, clock.now)
	if _, err := m.Claim("sbx_1", owner("sess_a")); err != nil {
		t.Fatalf("claim: %v", err)
	}

	clock.advance(20 * time.Second)
	if _, ok := m.Renew("sbx_1", owner("sess_b")); ok {
		t.Fatal("a non-owner renewed the lease")
	}
	renewed, ok := m.Renew("sbx_1", owner("sess_a"))
	if !ok {
		t.Fatal("the owner could not heartbeat")
	}
	if got := renewed.Remaining(clock.now()); got != 30*time.Second {
		t.Fatalf("heartbeat did not extend: %v", got)
	}

	// After a missed TTL the heartbeat cannot resurrect the lease either.
	clock.advance(31 * time.Second)
	if _, ok := m.Renew("sbx_1", owner("sess_a")); ok {
		t.Fatal("an expired lease was renewed by a late heartbeat")
	}
}

// Releasing is explicit (switch back to observe) and only the owner can do it.
func TestReleaseIsExplicitAndOwned(t *testing.T) {
	clock := newFake()
	m := New(30*time.Second, clock.now)
	if _, err := m.Claim("sbx_1", owner("sess_a")); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if m.Release("sbx_1", owner("sess_b")) {
		t.Fatal("a non-owner released somebody else's takeover")
	}
	if _, held := m.Holder("sbx_1"); !held {
		t.Fatal("the non-owner release removed the lease anyway")
	}
	if !m.Release("sbx_1", owner("sess_a")) {
		t.Fatal("the owner could not release")
	}
	if _, held := m.Holder("sbx_1"); held {
		t.Fatal("release left the lease in place")
	}
	if _, err := m.Claim("sbx_1", owner("sess_b")); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
}

// Labels are the UI's "正在被 X 控制". An empty display name falls back to the
// id; a session label is included so two sessions of one person are still
// distinguishable.
func TestOwnerLabel(t *testing.T) {
	cases := map[string]struct {
		owner Owner
		want  string
	}{
		"display name":    {Owner{UserID: "usr_1", UserName: "Ada", SessionLabel: "fix login"}, "Ada（fix login）"},
		"no session":      {Owner{UserID: "usr_1", UserName: "Ada"}, "Ada"},
		"no display name": {Owner{UserID: "usr_1", SessionID: "sess_a", SessionLabel: "main"}, "usr_1（main）"},
	}
	for name, tc := range cases {
		if got := tc.owner.Label(); got != tc.want {
			t.Errorf("%s: Label() = %q, want %q", name, got, tc.want)
		}
	}
}
