package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// concurrency is the branch-execution policy (PRD FR-3.1–3.3).
//
// The default is one at a time: branches of the same session usually share a purpose,
// and two agents editing two checkouts of one repository at once is a way to make a
// person lose track of which attempt produced what. Parallel running exists, but it is
// something you turn on.
const (
	// DefaultParallelLimit is the FR-3.3 default; UnlockableParallelLimit is what an
	// operator may raise it to.
	DefaultParallelLimit    = 3
	UnlockableParallelLimit = 5
)

// concurrentSiblings counts the sessions in the same branch tree that are running now,
// and returns the ones that are, so the caller can pause them.
func (s *server) concurrentSiblings(ctx context.Context, userID string, sess *contracts.Session) []string {
	rootID := sess.RootID
	if rootID == "" {
		rootID = sess.ID
	}
	siblings, err := s.deps.Store.ListSessions(ctx, userID, store.SessionFilter{RootID: rootID, Limit: 50})
	if err != nil {
		return nil
	}
	running := make([]string, 0, 2)
	for _, sibling := range siblings {
		if sibling.ID == sess.ID {
			continue
		}
		if status, serr := s.deps.Runtime.Status(ctx, sibling.ID); serr == nil && status.Running {
			running = append(running, sibling.ID)
		}
	}
	return running
}

// enforceConcurrency applies FR-3.1–3.3 before a prompt starts a run.
//
// It returns a non-empty message when the prompt must be refused (the parallel limit
// is reached). Otherwise it may have paused siblings, and says so, because a run that
// silently stops another run needs a visible reason.
func (s *server) enforceConcurrency(w http.ResponseWriter, r *http.Request, userID string, sess *contracts.Session) (paused []string, ok bool) {
	running := s.concurrentSiblings(r.Context(), userID, sess)
	if len(running) == 0 {
		return nil, true
	}

	parallel, _ := s.deps.Store.FeatureEnabled(r.Context(), userID, string(store.FeatureParallel))
	if !parallel {
		// FR-3.1: one at a time. Pause the others first rather than letting two runs
		// race in silence.
		for _, id := range running {
			ctx, cancel := detach(r.Context(), 10*time.Second)
			_, err := s.deps.Runtime.Interrupt(ctx, id)
			cancel()
			if err == nil {
				paused = append(paused, id)
			}
		}
		return paused, true
	}

	// FR-3.3: an explicit limit, with the reason spelled out rather than a bare 429.
	limit := s.parallelLimit()
	if len(running) >= limit {
		s.fail(w, r, withStatus(http.StatusTooManyRequests, contracts.ErrConflict,
			"并行上限是 "+itoa(limit)+" 个分支，先让其中一个跑完"))
		return nil, false
	}
	return nil, true
}

func (s *server) parallelLimit() int {
	limit := s.deps.ParallelLimit
	if limit <= 0 {
		return DefaultParallelLimit
	}
	if limit > UnlockableParallelLimit {
		return UnlockableParallelLimit
	}
	return limit
}

// itoa avoids pulling strconv into this file's imports for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
