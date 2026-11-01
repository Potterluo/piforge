package store

import (
	"context"
	"fmt"
)

// RunningSandboxIDs reports which sandboxes currently have a session whose status
// is 'running'.
//
// It exists for the disk watchdog's interval: "is a run in flight here" is the
// signal that decides whether to measure every two seconds or every ten. Asking
// the store rather than the runtime is deliberate — the runtime's registry only
// holds sessions this process has attached, and after a control-plane restart that
// is exactly the sessions the watchdog knows least about. `sessions.status` is
// written by the prompt path and cleared by the settle path, so it is the same
// number the UI shows.
//
// One query, indexed (`sessions_running`), no per-sandbox round trip.
func (s *Store) RunningSandboxIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT DISTINCT w.sandbox_id
		   FROM sessions s
		   JOIN worktrees w ON w.id = s.worktree_id
		  WHERE s.status = 'running' AND w.sandbox_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("store: running sandboxes: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: running sandboxes: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: running sandboxes: %w", err)
	}
	return out, nil
}
