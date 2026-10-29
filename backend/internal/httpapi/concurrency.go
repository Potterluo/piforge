package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// concurrency is the branch-execution policy (PRD FR-3.1–3.3).
//
// The default is one at a time: branches of the same session usually share a purpose,
// and two agents editing two checkouts of one repository at once is a way to make a
// person lose track of which attempt produced what. Parallel running exists, but it is
// something you turn on, and it has a cap an operator sets at startup.
const (
	// DefaultParallelLimit is the FR-3.3 default (the PRD's "3"), MaxParallelLimit
	// is the documented ceiling ("5", extended to 10 when the knob was wired up).
	DefaultParallelLimit = 3
	MaxParallelLimit     = 10
)

// ResolveParallelLimit turns the -parallel-limit flag and PIFORGE_PARALLEL_LIMIT
// into the number that is actually enforced (FR-3.3).
//
// The flag wins over the environment when it was passed explicitly; otherwise the
// environment supplies the value, and its absence means the documented default.
// Validation is deliberately strict: a 0 or an absurd value is a startup error,
// not a silent fall back to 3. An operator who typed a number must be able to
// trust that the deployment runs with it.
func ResolveParallelLimit(flagValue int, flagWasSet bool, envRaw string) (int, error) {
	value := flagValue
	if !flagWasSet {
		if raw := strings.TrimSpace(envRaw); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return 0, fmt.Errorf("PIFORGE_PARALLEL_LIMIT=%q is not an integer between 1 and %d", envRaw, MaxParallelLimit)
			}
			value = parsed
		}
	}
	if value < 1 || value > MaxParallelLimit {
		return 0, fmt.Errorf("parallel limit must be between 1 and %d, got %d", MaxParallelLimit, value)
	}
	return value, nil
}

// runningBranch describes one sibling that is occupying a slot, so a refusal can
// name what is running instead of only the number.
type runningBranch struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// concurrentSiblings returns the sessions in the same branch tree that are running
// now, so the caller can either pause them (FR-3.1) or explain the refusal.
func (s *server) concurrentSiblings(ctx context.Context, userID string, sess *contracts.Session) []runningBranch {
	rootID := sess.RootID
	if rootID == "" {
		rootID = sess.ID
	}
	siblings, err := s.deps.Store.ListSessions(ctx, userID, store.SessionFilter{RootID: rootID, Limit: 50})
	if err != nil {
		return nil
	}
	running := make([]runningBranch, 0, 2)
	for _, sibling := range siblings {
		if sibling.ID == sess.ID {
			continue
		}
		if status, serr := s.deps.Runtime.Status(ctx, sibling.ID); serr == nil && status.Running {
			running = append(running, runningBranch{ID: sibling.ID, Name: branchDisplayName(sibling)})
		}
	}
	return running
}

// branchDisplayName is what a person calls this branch: its label when it has one,
// otherwise its title, otherwise a short id so the refusal still identifies it.
func branchDisplayName(sess *contracts.Session) string {
	if label := strings.TrimSpace(sess.Label); label != "" {
		return label
	}
	if title := strings.TrimSpace(sess.Title); title != "" {
		return title
	}
	if len(sess.ID) > 8 {
		return sess.ID[:8]
	}
	return sess.ID
}

// enforceConcurrency applies FR-3.1–3.3 before a prompt starts a run.
//
// It returns ok=false when the prompt must be refused (the parallel limit is
// reached) and has already written the response. Otherwise it may have paused
// siblings, and says so, because a run that silently stops another run needs a
// visible reason.
func (s *server) enforceConcurrency(w http.ResponseWriter, r *http.Request, userID string, sess *contracts.Session) (paused []string, ok bool) {
	running := s.concurrentSiblings(r.Context(), userID, sess)
	if len(running) == 0 {
		return nil, true
	}

	parallel, _ := s.deps.Store.FeatureEnabled(r.Context(), userID, string(store.FeatureParallel))
	if !parallel {
		// FR-3.1: one at a time. Pause the others first rather than letting two runs
		// race in silence.
		for _, branch := range running {
			ctx, cancel := detach(r.Context(), 10*time.Second)
			_, err := s.deps.Runtime.Interrupt(ctx, branch.ID)
			cancel()
			if err == nil {
				paused = append(paused, branch.ID)
			}
		}
		return paused, true
	}

	// FR-3.3: an explicit limit, with the reason spelled out rather than a bare 429.
	limit := s.parallelLimit()
	if len(running) >= limit {
		names := make([]string, 0, len(running))
		ids := make([]string, 0, len(running))
		for _, branch := range running {
			names = append(names, branch.Name)
			ids = append(ids, branch.ID)
		}
		s.fail(w, r, withStatusDetails(http.StatusTooManyRequests, contracts.ErrRateLimited,
			fmt.Sprintf("并行上限是 %d 个分支，现在有 %d 个在运行（%s），先让其中一个跑完",
				limit, len(running), strings.Join(names, "、")),
			map[string]any{
				"limit":            limit,
				"running":          ids,
				"running_branches": names,
			}))
		return nil, false
	}
	return nil, true
}

// parallelLimit is the effective cap. Deps is validated at startup (see
// cmd/piforged), so a zero here only happens in hand-built test dependencies;
// keeping the fallback means those tests do not have to know the knob exists.
func (s *server) parallelLimit() int {
	limit := s.deps.ParallelLimit
	if limit <= 0 {
		return DefaultParallelLimit
	}
	if limit > MaxParallelLimit {
		return MaxParallelLimit
	}
	return limit
}
