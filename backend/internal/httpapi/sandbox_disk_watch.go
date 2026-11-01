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
// The run boundary is the honest place to *refuse* a turn, but it is not a
// bound on what a sandbox can write: an adversarial acceptance run wrote 3 GiB
// into a sandbox whose admin row said `quota_bytes=2 GiB, over=false` with one
// `fallocate`, and nothing stopped it because nothing was watching between
// turns.
//
// Two mechanisms close that gap, and they are complementary:
//
//   - the container is created with RLIMIT_FSIZE set to the budget
//     (sandbox.Limits.DiskBytes → `docker run --ulimit fsize`), so a single file
//     cannot exceed the quota in the first place. That is the kernel's answer,
//     and it is what makes `fallocate -l 3G` fail instead of succeeding;
//   - this watchdog measures the workspace from *inside* the sandbox every few
//     seconds. Many files can each stay under RLIMIT_FSIZE while the total goes
//     over, and only a measuring loop sees that.
//
// When it sees a sandbox over budget it stops the agents, records a
// `platform.error` with the same 507 message the run boundary uses, and — if the
// workspace keeps growing after the agents are gone — pauses the sandbox. The
// two stages exist so the ordinary case (an `npm install` that overshoots) leaves
// the terminal usable to clean up, while a tenant that keeps writing cannot fill
// the host volume at a few hundred MB/s.

// defaultDiskWatchInterval is how often the watchdog measures one sandbox.
//
// Five seconds is a real bound, not a sample: on this host a single writer
// manages ~1.3 GB/s, so the worst case is a few GiB of overshoot before the
// agents are killed. The alternative — a per-write filesystem quota — needs
// either an XFS/ext4 project-quota mount or CAP_SYS_ADMIN to build a loopback
// filesystem, and this deployment has neither (docs/SANDBOX-DEPLOY.md §6).
const defaultDiskWatchInterval = 5 * time.Second

// diskWatch tracks one sandbox between ticks: how many consecutive ticks found
// it over budget, and what it held on the previous tick. Growth between ticks is
// what distinguishes "the agent overshot once" from "something is still writing".
type diskWatch struct {
	strikes   int
	lastBytes int64
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
	interval := s.deps.DiskWatchInterval
	if interval <= 0 {
		interval = defaultDiskWatchInterval
	}
	s.log.Info("sandbox disk watchdog: watching",
		"interval", interval.String(),
		"quota", diskquota.HumanBytes(s.diskQuota.Limit()),
		"enforcement", "RLIMIT_FSIZE per container file + this periodic check (stop the agents, then pause if it keeps growing)")
	go func() {
		ticker := time.NewTicker(interval)
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

// diskWatchPass measures every READY sandbox and acts on the ones over budget.
func (s *server) diskWatchPass(ctx context.Context) {
	dirs, err := s.deps.Store.SandboxDirectories(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "sandbox disk watchdog: cannot list sandboxes", "error", err.Error())
		return
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
		sb, err := s.deps.Store.Sandbox(ctx, dir.UserID, dir.ID)
		if err != nil {
			continue
		}
		if sb.State != contracts.SandboxReady {
			// A paused sandbox is not writing. Measuring it would only produce a
			// report nobody can act on, and waking it is the wrong repair.
			s.clearDiskWatch(dir.ID)
			continue
		}
		key := diskWatchKey(dir.ID)
		rep := s.diskQuota.MeasureWith(ctx, dir.ID, dir.RootDir, s.deps.MeasureSandbox)
		if rep.Unmeasurable {
			s.log.DebugContext(ctx, "sandbox disk watchdog: could not measure", "sandbox", dir.ID, "error", rep.Error)
			continue
		}
		if !rep.Over {
			s.clearDiskWatch(key)
			continue
		}
		s.enforceDiskWatch(ctx, sb, rep, key)
	}
}

// enforceDiskWatch is the two-stage response to an over-budget sandbox.
func (s *server) enforceDiskWatch(ctx context.Context, sb *contracts.Sandbox, rep diskquota.Report, key string) {
	s.diskMu.Lock()
	state := s.diskWatchState[key]
	grew := state.lastBytes > 0 && rep.Bytes > state.lastBytes
	state.lastBytes = rep.Bytes
	state.strikes++
	first := state.strikes == 1
	s.diskWatchState[key] = state
	s.diskMu.Unlock()

	message := s.diskQuota.Message(rep)
	// One warning per episode, not one every five seconds: the operator needs it
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
	// Three strikes is ~10 s of writing — long enough to tell a runaway agent
	// (already stopped above, which ends the growth) from a tenant that will not
	// stop. A paused sandbox is woken by the next message (wakeSandbox), so this
	// is a brake, not a lockout: resume, delete files, continue.
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

// clearDiskWatch forgets one sandbox's strikes, so a sandbox that was cleaned up
// starts from a clean slate.
func (s *server) clearDiskWatch(key string) {
	s.diskMu.Lock()
	delete(s.diskWatchState, key)
	s.diskMu.Unlock()
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
