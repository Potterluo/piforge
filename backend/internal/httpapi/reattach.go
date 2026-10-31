package httpapi

import (
	"context"
	"fmt"
	"time"
)

// Reattaching to agents that outlived this process.
//
// Stage 3 of docs/EXECUTION-BOUNDARY.md moved the agent's stdio into a
// supervisor inside the sandbox, so after a control-plane restart the agent is
// still running and still holding the run. Two things have to happen for that to
// be worth anything:
//
//  1. the new control plane has to *connect* to it — otherwise the agent's events
//     pile up in the supervisor's replay buffer and its run never settles into
//     the database;
//  2. it must not do that for a session that merely claims to be running.
//
// The first is hub.ensure, which is the same path a message takes: it calls the
// runtime's Start, and Start's Dial reattaches when a supervisor is listening.
// The consequence is that the subscription, the event pump, the derived run
// state and the transcript all resume exactly as they do for a fresh process.
//
// The second is the liveness probe. It is asked before anything is dialled: a
// session whose agent is provably gone is left to reconciliation (which writes
// `interrupted`, honestly), and one the probe cannot answer for is skipped —
// "unknown" must never become "start a second agent for this session".

// ReattachAttemptTimeout bounds one session's reattach. A dial inside the
// sandbox is fast; a launch that follows a failed reattach is not, so this is
// generous without being a stall.
const ReattachAttemptTimeout = 90 * time.Second

// ReattachLiveSessions reconnects this process to every running session an
// authority can prove an agent for.
//
// It is called once after the startup reconciliation has run, so a session whose
// agent died with the previous control plane has already been marked interrupted
// and is not in the list. It returns how many sessions it reattached, which the
// caller logs: zero is a normal answer on a deployment with no agents.
func (sv *Server) ReattachLiveSessions(ctx context.Context) (int, error) {
	s := sv.s
	if s.deps.Runtime == nil {
		return 0, fmt.Errorf("no agent runtime is configured")
	}
	candidates, err := s.deps.Store.RunningSessions(ctx)
	if err != nil {
		return 0, fmt.Errorf("list running sessions: %w", err)
	}
	attached := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return attached, err
		}
		if s.deps.Probe != nil {
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			alive, aerr := s.deps.Probe.Alive(probeCtx, candidate.SessionID)
			cancel()
			if aerr != nil {
				// Unknown is not dead, and it is not "reattach": leave the row to
				// the watchdog, which asks again.
				s.log.WarnContext(ctx, "cannot prove an agent for a running session; not reattaching",
					"session", candidate.SessionID, "error", aerr.Error())
				continue
			}
			if !alive {
				// Reconciliation should have caught this; if it has not run yet,
				// starting an agent here would resurrect a finished session.
				s.log.DebugContext(ctx, "no agent to reattach for a running session",
					"session", candidate.SessionID)
				continue
			}
		}
		sess, serr := s.deps.Store.Session(ctx, candidate.UserID, candidate.SessionID)
		if serr != nil {
			s.log.WarnContext(ctx, "running session cannot be loaded for reattach",
				"session", candidate.SessionID, "error", serr.Error())
			continue
		}
		spec := s.sessionSpec(candidate.UserID, sess)
		if spec.Worktree == "" {
			// A session with no worktree has no agent to reconnect to, and Start
			// would fail anyway. Nothing was lost: there is no run either.
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, ReattachAttemptTimeout)
		startErr := s.hub.ensure(attemptCtx, candidate.UserID, spec)
		cancel()
		if startErr != nil {
			s.log.WarnContext(ctx, "could not reattach to a running agent",
				"session", candidate.SessionID, "error", startErr.Error())
			continue
		}
		attached++
		s.log.InfoContext(ctx, "reconnected to a running agent after the restart",
			"session", candidate.SessionID, "project", candidate.ProjectID,
			"detail", "the run continues in the same process; its events are being persisted again")
	}
	return attached, nil
}
