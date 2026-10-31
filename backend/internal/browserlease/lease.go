// Package browserlease hands out an exclusive claim on one sandbox browser.
//
// The browser belongs to a sandbox, so two sessions of one project (and any
// future shared sandbox) are looking at the *same* page. Watching is free —
// every viewer sees the same frames — but driving is not: a click from one
// person while another is typing produces a page neither of them can explain.
// This package makes driving exclusive with a lease that expires on its own.
//
// The decision is in memory on purpose: control is about who holds the wheel
// right now, and a control-plane restart means nobody is holding it. Persisting
// a takeover would resurrect a claim from a process that no longer exists.
package browserlease

import (
	"fmt"
	"sync"
	"time"
)

// Owner is who is driving. It is user *and* session because one person can have
// two sessions in one sandbox, and "my other tab" must not be able to steal the
// browser from the session that actually took it over.
type Owner struct {
	UserID       string
	UserName     string
	SessionID    string
	SessionLabel string
}

// Same identifies the principal, ignoring the display fields.
func (o Owner) Same(other Owner) bool {
	return o.UserID != "" && o.UserID == other.UserID && o.SessionID == other.SessionID
}

// Label is what the UI calls the holder. The session label is included only
// when it says something the user name does not (two sessions of one person).
func (o Owner) Label() string {
	name := o.UserName
	if name == "" {
		name = o.UserID
	}
	if o.SessionLabel != "" {
		return fmt.Sprintf("%s（%s）", name, o.SessionLabel)
	}
	return name
}

// Lease is a granted claim. ExpiresAt is part of the answer, not an internal
// detail: the refusal has to tell the caller how long the wait is.
type Lease struct {
	Owner     Owner
	ExpiresAt time.Time
}

// Remaining reports how long the lease still has at now, never negative.
func (l Lease) Remaining(now time.Time) time.Duration {
	left := l.ExpiresAt.Sub(now)
	if left < 0 {
		return 0
	}
	return left
}

// BusyError is the typed refusal: someone else holds the lease, and here is who
// and for how much longer.
type BusyError struct {
	Holder    Lease
	Remaining time.Duration
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("the browser is controlled by %s for another %s",
		e.Holder.Owner.Label(), e.Remaining.Round(time.Second))
}

// Manager owns the leases. Safe for concurrent use.
type Manager struct {
	ttl time.Duration
	now func() time.Time

	mu    sync.Mutex
	lease map[string]Lease
}

// New builds a manager. A clock is injected so expiry can be tested without
// sleeping; callers that do not care pass time.Now.
func New(ttl time.Duration, now func() time.Time) *Manager {
	if ttl <= 0 {
		ttl = 45 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &Manager{ttl: ttl, now: now, lease: map[string]Lease{}}
}

// TTL is how long a lease lives without a heartbeat.
func (m *Manager) TTL() time.Duration { return m.ttl }

// Claim grants the lease for scope, renews it when the same owner already holds
// it, and refuses with *BusyError when somebody else does. An expired lease is
// not a holder: the previous client stopped heartbeating, which is exactly what
// a closed tab looks like.
func (m *Manager) Claim(scope string, owner Owner) (Lease, error) {
	if scope == "" {
		return Lease{}, fmt.Errorf("browserlease: scope is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if held, ok := m.lease[scope]; ok && held.ExpiresAt.After(now) {
		if !held.Owner.Same(owner) {
			return Lease{}, &BusyError{Holder: held, Remaining: held.Remaining(now)}
		}
		held.Owner = owner
		held.ExpiresAt = now.Add(m.ttl)
		m.lease[scope] = held
		return held, nil
	}
	lease := Lease{Owner: owner, ExpiresAt: now.Add(m.ttl)}
	m.lease[scope] = lease
	return lease, nil
}

// Renew extends a lease the caller already holds. It reports false when the
// lease is gone or belongs to somebody else — a heartbeat is never a takeover.
func (m *Manager) Renew(scope string, owner Owner) (Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	held, ok := m.lease[scope]
	if !ok || !held.ExpiresAt.After(now) || !held.Owner.Same(owner) {
		return Lease{}, false
	}
	held.ExpiresAt = now.Add(m.ttl)
	m.lease[scope] = held
	return held, true
}

// Release frees the lease when the caller is its owner. A non-owner cannot free
// somebody else's takeover by disconnecting.
func (m *Manager) Release(scope string, owner Owner) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	held, ok := m.lease[scope]
	if !ok {
		return false
	}
	if owner.UserID != "" || owner.SessionID != "" {
		if !held.Owner.Same(owner) {
			return false
		}
	}
	delete(m.lease, scope)
	return true
}

// Holder reports the live holder of scope. An expired lease is removed here so
// a reader never sees a holder who is already gone.
func (m *Manager) Holder(scope string) (Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	held, ok := m.lease[scope]
	if !ok {
		return Lease{}, false
	}
	if !held.ExpiresAt.After(m.now()) {
		delete(m.lease, scope)
		return Lease{}, false
	}
	return held, true
}
