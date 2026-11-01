package httpapi

import (
	"context"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/diskquota"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// The periodic half of disk-quota enforcement (FR-5.8).
//
// The run boundary is the honest place to *refuse* a turn, but it is not a bound
// on what a sandbox can write: an adversarial acceptance run wrote 3 GiB into a
// sandbox whose admin row said `quota_bytes=2 GiB, over=false` with one
// `fallocate`, and the second one wrote 7.03 GB into a 2 GiB budget in ten seconds
// with a loop of 8 MiB files — 3.27x — while this watchdog was still on its first
// warning.
//
// Three mechanisms bound it now, and they are complementary:
//
//   - the container is created with RLIMIT_FSIZE set to the budget
//     (sandbox.Limits.DiskBytes → `docker run --ulimit fsize`), so a single file
//     cannot exceed the quota. That is the kernel's answer, and it is what makes
//     `fallocate -l 3G` fail instead of succeeding;
//   - this watchdog measures the workspace from *inside* the sandbox, at an
//     interval that depends on whether a run is in flight (2 s) or not (10 s).
//     Many files can each stay under RLIMIT_FSIZE while the total goes over, and
//     only a measuring loop sees that;
//   - the sandbox image runs an in-sandbox enforcer (internal/quotaenforcer) that
//     walks the workspace every 100 ms and SIGSTOPs the processes holding it open
//     for writing. That is the bound on the *burst*; this loop is the slower,
//     more authoritative one that also stops the agents and pauses a sandbox that
//     keeps growing.
//
// When this watchdog sees a sandbox over budget it stops the agents, records a
// `platform.error` with the same 507 message the run boundary uses, and — if the
// workspace keeps growing after the agents are gone — pauses the sandbox. The two
// stages exist so the ordinary case (an `npm install` that overshoots) leaves the
// terminal usable to clean up, while a tenant that keeps writing cannot fill the
// host volume at a few hundred MB/s.

// diskWatchTick is how often the loop wakes up to decide which sandboxes are due.
//
// It is not the measurement interval: each sandbox carries its own `nextAt`
// (diskquota.WatchInterval). A short tick keeps the adaptive intervals exact
// without meaning "measure everything every 500 ms".
const diskWatchTick = 500 * time.Millisecond

// defaultDiskWatchInterval was the fixed five-second interval every sandbox used
// to be measured at. It survives only as the fallback for a test that wants a
// deterministic cadence: the loop's real intervals are diskquota.ActiveWatchInterval
// and diskquota.IdleWatchInterval, which is what the 3.27x measurement argued for.
const defaultDiskWatchInterval = 2 * time.Second

// diskWatch tracks one sandbox between ticks: how many consecutive ticks found
// it over budget, what it held on the previous tick, and when it is next due.
// Growth between ticks is what distinguishes "the agent overshot once" from
// "something is still writing".
type diskWatch struct {
	strikes   int
	lastBytes int64
	nextAt    time.Time
}

// StartDiskWatch runs the periodic check until ctx is done. It is a no-op (with
// one log line saying why) when the deployment has no quota or no way to measure
// a sandbox from inside, because a watchdog that cannot measure is worse than
// none: it would report "fine" forever.
func (sv *Server) StartDiskWatch(ctx context.Context) {
	s := sv.s
	if s.diskQuota.Disabled() {
		s.log.Info("sandbox disk watchdog: disabled (no quota configured)")
		return
	}
	if s.deps.MeasureSandbox == nil {
		s.log.Info("sandbox disk watchdog: disabled (this deployment has no in-sandbox measurement; the run boundary still measures host workspaces)")
		return
	}
	s.log.Info("sandbox disk watchdog: watching",
		"active_interval", s.diskWatchInterval(true).String(),
		"idle_interval", s.diskWatchInterval(false).String(),
		"tick", diskWatchTick.String(),
		"quota", diskquota.HumanBytes(s.diskQuota.Limit()),
		"enforcement", "RLIMIT_FSIZE per container file, the in-sandbox enforcer at 100 ms, and this check (stop the agents, then pause if it keeps growing)")
	go func() {
		ticker := time.NewTicker(diskWatchTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.diskWatchPass(ctx)
			}
		}
	}()
}

// diskWatchInterval is how long a sandbox waits between measurements: the fast
// interval while a run is in flight, the slow one otherwise. Deps.DiskWatchInterval
// overrides both, which is how a test gets a deterministic cadence.
func (s *server) diskWatchInterval(active bool) time.Duration {
	if s.deps.DiskWatchInterval > 0 {
		return s.deps.DiskWatchInterval
	}
	return diskquota.WatchInterval(active)
}

// diskWatchPass measures the sandboxes that are due and acts on the ones over
// budget.
func (s *server) diskWatchPass(ctx context.Context) {
	dirs, err := s.deps.Store.SandboxDirectories(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "sandbox disk watchdog: cannot list sandboxes", "error", err.Error())
		return
	}
	// One indexed query for the whole pass: which sandboxes have a run in flight.
	// A failure here only costs the fast interval, never a measurement.
	running, rerr := s.deps.Store.RunningSandboxIDs(ctx)
	if rerr != nil {
		s.log.DebugContext(ctx, "sandbox disk watchdog: cannot tell which sandboxes are running; using the idle interval",
			"error", rerr.Error())
		running = map[string]bool{}
	}
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return
		}
		if !isContainerWorkspace(dir.RootDir) {
			// A shared workspace is measured by the host walk, and the run
			// boundary is where that answer is used. Nothing to stop here: the
			// files are on this host, not in a sandbox this process cannot see.
			continue
		}
		key := diskWatchKey(dir.ID)
		now := time.Now()
		if !s.diskWatchDue(key, now) {
			continue
		}
		interval := s.diskWatchInterval(running[dir.ID])
		sb, err := s.deps.Store.Sandbox(ctx, dir.UserID, dir.ID)
		if err != nil {
			s.diskWatchSchedule(key, now.Add(interval))
			continue
		}
		if sb.State != contracts.SandboxReady {
			// A paused sandbox is not writing. Measuring it would only produce a
			// report nobody can act on, and waking it is the wrong repair. Its
			// strikes are forgotten, but its place in the queue is kept: clearing
			// the entry outright would make it due on every tick.
			s.diskWatchReset(key, now.Add(interval))
			continue
		}
		rep := s.diskQuota.MeasureWith(ctx, dir.ID, dir.RootDir, s.deps.MeasureSandbox)
		if rep.Unmeasurable {
			s.log.DebugContext(ctx, "sandbox disk watchdog: could not measure", "sandbox", dir.ID, "error", rep.Error)
			s.diskWatchSchedule(key, now.Add(interval))
			continue
		}
		if !rep.Over {
			s.diskWatchReset(key, now.Add(interval))
			continue
		}
		s.enforceDiskWatch(ctx, sb, rep, key, now.Add(interval))
	}
}

// enforceDiskWatch is the two-stage response to an over-budget sandbox.
func (s *server) enforceDiskWatch(ctx context.Context, sb *contracts.Sandbox, rep diskquota.Report, key string, next time.Time) {
	s.diskMu.Lock()
	state := s.diskWatchState[key]
	grew := state.lastBytes > 0 && rep.Bytes > state.lastBytes
	state.lastBytes = rep.Bytes
	state.strikes++
	state.nextAt = next
	first := state.strikes == 1
	s.diskWatchState[key] = state
	s.diskMu.Unlock()

	message := s.diskQuota.Message(rep)
	// One warning per episode, not one per measurement: the operator needs it
	// once, with the numbers. The pause (below) is the second signal.
	if first {
		s.log.WarnContext(ctx, "sandbox is over its disk quota",
			slog.String("sandbox", sb.ID),
			slog.String("user", sb.UserID),
			slog.Int64("used_bytes", rep.Bytes),
			slog.Int64("limit_bytes", rep.QuotaBytes),
			slog.String("response", "stopping the agents; the next run is refused until the sandbox is under budget"))
	}
	s.log.DebugContext(ctx, "sandbox disk watchdog: over budget",
		"sandbox", sb.ID, "used_bytes", rep.Bytes, "limit_bytes", rep.QuotaBytes,
		"strike", state.strikes, "still_growing", grew)

	// Every tick: the agents stop and the transcript says why. The user sees the
	// same 507 message a refused run carries, so "why did my turn die" has an
	// answer in the session itself.
	for _, id := range s.sessionsOnSandboxContext(ctx, sb.UserID, sb.ID) {
		_ = s.deps.Runtime.Stop(ctx, id)
		// A container placement's agent outlives the control-plane stream by
		// design (the supervisor inside the sandbox owns it), so the runtime stop
		// only detaches. An agent that is still writing has to be killed, or "the
		// agent stops within N seconds" is not true.
		if s.deps.StopAgent != nil {
			if err := s.deps.StopAgent(ctx, sb.ID, id); err != nil {
				s.log.DebugContext(ctx, "sandbox disk watchdog: could not stop the in-sandbox agent",
					"sandbox", sb.ID, "session", id, "error", err.Error())
			}
		}
		s.appendQuotaRefusal(ctx, sb.UserID, id, sb, rep, message)
	}

	// Continued growth while the agents are gone means something other than the
	// agent is writing (a terminal, a leftover process). Stop the sandbox: the
	// volume is 1007 GB and the control plane's own data lives on the same host.
	// Three strikes is one active interval (6 s) of writing — long enough to tell
	// a runaway agent (already stopped above) from a tenant that will not stop.
	// A paused sandbox is woken by the next message (wakeSandbox), so this is a
	// brake, not a lockout: resume, delete files, continue.
	if state.strikes >= 3 && grew {
		if err := s.deps.Sandbox.Pause(ctx, sb.ID); err != nil {
			s.log.WarnContext(ctx, "sandbox disk watchdog: could not pause the over-quota sandbox",
				"sandbox", sb.ID, "error", err.Error())
			return
		}
		if err := s.deps.Store.UpdateSandboxState(ctx, sb.UserID, sb.ID, contracts.SandboxPaused,
			"disk quota exceeded: paused by the platform"); err != nil {
			s.log.WarnContext(ctx, "sandbox disk watchdog: paused but could not record it",
				"sandbox", sb.ID, "error", err.Error())
		}
		s.log.WarnContext(ctx, "sandbox disk watchdog: paused an over-quota sandbox that kept growing",
			"sandbox", sb.ID, "used_bytes", rep.Bytes, "limit_bytes", rep.QuotaBytes)
	}
}

// diskWatchDue reports whether one sandbox is due for a measurement.
func (s *server) diskWatchDue(key string, now time.Time) bool {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	state, ok := s.diskWatchState[key]
	return !ok || state.nextAt.IsZero() || !now.Before(state.nextAt)
}

// diskWatchSchedule sets when one sandbox is measured next, keeping its strikes.
func (s *server) diskWatchSchedule(key string, next time.Time) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	state := s.diskWatchState[key]
	state.nextAt = next
	s.diskWatchState[key] = state
}

// diskWatchReset forgets one sandbox's strikes (it was measured under budget, or
// it is not running) and sets when it is due next.
func (s *server) diskWatchReset(key string, next time.Time) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	state := s.diskWatchState[key]
	state.strikes = 0
	state.lastBytes = 0
	state.nextAt = next
	s.diskWatchState[key] = state
}

func diskWatchKey(sandboxID string) string { return sandboxID }

// isContainerWorkspace reports whether a sandbox's reported root is the path
// inside its container rather than a directory this process can open.
func isContainerWorkspace(root string) bool {
	return path.Clean(strings.TrimSpace(root)) == sandbox.ContainerWorkspace
}

// sandboxMeasurer is the measurer for one sandbox root: the in-sandbox probe for
// a container path, nil (the host walk) for a directory this process owns.
func (s *server) sandboxMeasurer(root string) diskquota.Measurer {
	if isContainerWorkspace(root) {
		return s.deps.MeasureSandbox
	}
	return nil
}

// sessionsOnSandboxContext is sessionsOnSandbox without the request: the
// watchdog has no request, and the query is the same one the pause handler uses.
func (s *server) sessionsOnSandboxContext(ctx context.Context, userID, sandboxID string) []string {
	sessions, err := s.deps.Store.ListSessions(ctx, userID, store.SessionFilter{Limit: 500})
	if err != nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, sess := range sessions {
		if sess.WorktreeID == "" {
			continue
		}
		wt, werr := s.deps.Store.WorktreeByID(ctx, sess.WorktreeID)
		if werr != nil || wt.SandboxID != sandboxID {
			continue
		}
		out = append(out, sess.ID)
	}
	return out
}
