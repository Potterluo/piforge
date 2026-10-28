package pi

import (
	"context"
	"time"
)

// Idle reaping (PRD FR-5.x).
//
// A session keeps its agent process alive so the next prompt is instant. Nothing in
// that sentence bounds how many processes a long-running deployment ends up holding:
// every session ever opened keeps one until the process dies, which on a busy box is
// hundreds of children doing nothing. Reaping is the missing half of "keep it warm".
//
// The rule is deliberately dull: an agent that has not been touched for the TTL is
// stopped. The session is untouched — Pi's transcript is on disk, and the next prompt
// starts a fresh process on the same file, which is the same path a second fork or a
// server restart already takes.

// touch marks a session as used. Hot paths call it; it is one atomic store.
func (s *session) touch() { s.lastUsed.Store(time.Now().UnixNano()) }

// idleSince reports how long the session has been untouched.
func (s *session) idleSince(now time.Time) time.Duration {
	last := s.lastUsed.Load()
	if last == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, last))
}

// ReapIdle stops agents untouched for longer than ttl and reports how many went.
//
// It never stops a session with a run in flight: a run is work in progress, and
// killing it would lose the answer somebody is waiting for. That check is the reason
// this lives in the runtime rather than in a timer somewhere above it.
func (r *Runtime) ReapIdle(ctx context.Context, ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	now := time.Now()

	r.mu.Lock()
	victims := make([]string, 0, 4)
	for id, s := range r.sessions {
		if s.idleSince(now) < ttl {
			continue
		}
		// Ask the normalizer first: a run that started after the idle check must not
		// be cut in half. The normalizer's state is the same one /status reports.
		if s.norm != nil && s.norm.State().Running {
			continue
		}
		victims = append(victims, id)
	}
	r.mu.Unlock()

	for _, id := range victims {
		_ = r.Stop(ctx, id)
	}
	return len(victims)
}

// StartIdleReaper runs ReapIdle on a timer until ctx is done.
func (r *Runtime) StartIdleReaper(ctx context.Context, ttl, every time.Duration) {
	if ttl <= 0 || every <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.ReapIdle(ctx, ttl)
		}
	}
}
