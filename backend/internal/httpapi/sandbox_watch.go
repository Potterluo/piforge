package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// The sandbox liveness watchdog, and the recovery it performs.
//
// The defect it answers (adversarial acceptance #2, 2026-11-01): a fork storm
// inside one sandbox saturates its pids cgroup, at which point the container
// cannot spawn a single process. `docker exec` fails with "unable to spawn
// stage-1", the platform's own `du` times out, the agent cannot start, and the
// only recovery was an operator pressing pause and then resume. A tenant could
// therefore take its own project offline for as long as it liked, and nothing in
// the product noticed.
//
// Lowering the pids default (deploy/tenant-limits.json, 512 → 256) reduces what a
// storm can park in the cgroup, and it is the first thing this fix does — but it
// cannot *prevent* the wedge: a storm fills whatever ceiling it is given, which
// is what `scripts/measure-pids.ps1` measures (exec fails at 64, 128, 192, 256,
// 512 and 1024 alike, because the failure is saturation and not the number).
// So the guarantee has to be detection and repair:
//
//   - probe every running sandbox through its own API with a short timeout;
//   - two consecutive failures are a wedge (one is a loaded host);
//   - stop and start the container — the workspace is a volume, so the checkout,
//     the transcript and the agent's files survive, and every process of the
//     storm is gone because they all lived in the container;
//   - tell the session, with a `platform.error`, in the same transcript the user
//     is looking at: 「沙箱无响应，正在恢复」, then whether it came back;
//   - expose the same repair as POST /v1/sessions/{id}/sandbox/recover, which is
//     what the one-click 恢复 button calls, and report `responsive` from the
//     cached probe so the banner appears without the page doing its own probing.
//
// What this is not: a resource policy. A recovered sandbox can be wedged again by
// the same tenant a second later; the watchdog will recover it again, and the
// operator sees one WARN per episode. A tenant that does this in a loop is a
// tenancy problem (`tenant_limits.json` max_sandboxes, and deleting the project),
// not something a watchdog can decide.

// defaultSandboxWatchInterval is how often one sandbox is probed.
//
// Five seconds, and a missed probe is confirmed 1.5 s later rather than waiting
// for the next interval: detection is one interval plus the confirm plus one
// probe timeout, where two ten-second intervals put the measured detection at
// 76 s in the third adversarial acceptance (the probe kept succeeding while the
// tenant's own forks failed, so the clock, not the probe, was the cost). The
// confirm is what keeps the shorter interval from restarting a healthy container
// on one missed probe.
const defaultSandboxWatchInterval = 5 * time.Second

// sandboxConfirmDelay is the pause between a missed probe and the probe that
// confirms it. Long enough for a probe that timed out on a loaded host to be
// retried, short enough that detection stays inside one interval.
const sandboxConfirmDelay = 1500 * time.Millisecond

// sandboxRecoveryTimeout bounds one recovery (stop + start + verification).
//
// The measured cost on this host is ~5 s of stop and ~1 s until the container
// answers a command again; the rest is the image's own services coming back
// (~10-20 s, the same window waitForSandboxAPI uses when a paused sandbox is
// woken). A recovery that cannot verify in this window reports failure instead of
// claiming success.
const sandboxRecoveryTimeout = 45 * time.Second

// sandboxProbeVerdict is what the watchdog last saw for one sandbox.
type sandboxProbeVerdict struct {
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	At         time.Time `json:"at"`
	Recovering bool      `json:"recovering,omitempty"`
	// Recovered is when the last successful recovery finished, so the banner can
	// say "recovered 12 s ago" instead of disappearing without a word.
	RecoveredAt time.Time `json:"recovered_at,omitempty"`
	// Restarts counts the recoveries this process has performed for the sandbox.
	Restarts int `json:"restarts,omitempty"`
}

// StartSandboxWatch runs the liveness probe until ctx is done. It is a no-op
// (with one log line saying why) when the deployment cannot probe a sandbox from
// inside; a watchdog that cannot probe must not pretend it did.
func (sv *Server) StartSandboxWatch(ctx context.Context) {
	s := sv.s
	if s.deps.ProbeSandbox == nil {
		s.log.Info("sandbox liveness watchdog: disabled (this deployment cannot run a command inside a sandbox)")
		return
	}
	if s.deps.Sandbox == nil {
		s.log.Info("sandbox liveness watchdog: disabled (no sandbox provider)")
		return
	}
	interval := s.deps.SandboxWatchInterval
	if interval <= 0 {
		interval = defaultSandboxWatchInterval
	}
	s.log.Info("sandbox liveness watchdog: watching",
		"interval", interval.String(),
		"probe_timeout", s.probeTimeout().String(),
		"recovery", "stop + start the container, keep its volume; the session is told before and after")
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sandboxWatchPass(ctx)
			}
		}
	}()
}

// probeTimeout is the bound on one probe as the watchdog applies it. The
// deployment's own probe has a timeout of its own; this one also bounds the
// resolution of the sandbox's endpoint, which happens before the command.
func (s *server) probeTimeout() time.Duration {
	if s.deps.SandboxProbeTimeout > 0 {
		return s.deps.SandboxProbeTimeout
	}
	return defaultSandboxProbeTimeout
}

// defaultSandboxProbeTimeout is the outer bound of one probe.
const defaultSandboxProbeTimeout = 5 * time.Second

// sandboxWatchPass probes every running sandbox once and recovers the wedged
// ones.
func (s *server) sandboxWatchPass(ctx context.Context) {
	dirs, err := s.deps.Store.SandboxDirectories(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "sandbox liveness watchdog: cannot list sandboxes", "error", err.Error())
		return
	}
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return
		}
		if !isContainerWorkspace(dir.RootDir) {
			// A shared workspace runs no sandbox process, so there is nothing to
			// probe; the host walk still measures its disk.
			continue
		}
		sb, err := s.deps.Store.Sandbox(ctx, dir.UserID, dir.ID)
		if err != nil {
			continue
		}
		if !sandboxShouldBeProbed(sb.State) || s.isRecovering(sb.ID) {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout())
		perr := s.deps.ProbeSandbox(probeCtx, sb.ID)
		cancel()
		if perr == nil {
			s.noteProbe(sb.ID, true, "")
			continue
		}
		strikes := s.noteProbeFailure(sb.ID, perr.Error())
		s.log.WarnContext(ctx, "sandbox liveness watchdog: the sandbox did not answer",
			slog.String("sandbox", sb.ID),
			slog.String("user", sb.UserID),
			slog.Int("strike", strikes),
			slog.String("error", perr.Error()))
		if strikes < sandboxWedgedStrikes {
			// Confirm now rather than on the next tick: one missed probe is a
			// suspicion (a loaded host), and the second one is the verdict. Doing
			// it here is what makes detection one interval instead of two.
			if !s.confirmUnresponsive(ctx, sb.ID) {
				continue
			}
			strikes = s.noteProbeFailure(sb.ID, "the confirmation probe also failed")
		}
		if strikes < sandboxWedgedStrikes {
			continue
		}
		s.startRecovery(sb, "the sandbox stopped answering its own API (confirmed)")
	}
}

// sandboxWedgedStrikes is how many failed probes — the first one plus its
// confirmation — mean "wedged".
//
// Two, but 1.5 s apart rather than ten: the first failure is a suspicion and the
// confirmation is taken immediately instead of on the next tick. Restarting a
// healthy container still needs two failures, so a loaded host that misses one
// probe costs nothing; a sandbox that cannot run `true` twice in 1.5 s is wedged.
const sandboxWedgedStrikes = 2

// confirmUnresponsive waits out the confirm delay and probes once more. It
// reports true when the sandbox still cannot run a command, i.e. when the miss
// was not a loaded host.
func (s *server) confirmUnresponsive(ctx context.Context, sandboxID string) bool {
	delay := s.deps.SandboxConfirmDelay
	if delay <= 0 {
		delay = sandboxConfirmDelay
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout())
	defer cancel()
	return s.deps.ProbeSandbox(probeCtx, sandboxID) != nil
}

// sandboxShouldBeProbed reports whether a state has a running container to probe.
func sandboxShouldBeProbed(state contracts.SandboxState) bool {
	switch state {
	case contracts.SandboxReady, contracts.SandboxBusy, contracts.SandboxIdle:
		return true
	default:
		// PAUSED, TERMINATED, MISSING, CREATING: nothing to ask. A paused sandbox
		// is woken by the next prompt, and its own wake path already waits for
		// the API.
		return false
	}
}

// startRecovery runs one recovery in the background, at most one per sandbox.
func (s *server) startRecovery(sb *contracts.Sandbox, reason string) {
	if sb == nil || !s.beginRecovery(sb.ID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), sandboxRecoveryTimeout)
		defer cancel()
		defer s.endRecovery(sb.ID)
		if _, _, err := s.recoverSandbox(ctx, sb, reason); err != nil {
			s.log.Error("sandbox liveness watchdog: recovery failed",
				slog.String("sandbox", sb.ID), slog.String("error", err.Error()))
		}
	}()
}

// recoverSandbox performs the repair: stop the container, start it again from its
// volume, verify that it answers, and tell every session on it.
//
// Order matters. The user is told *before* the container is touched, because the
// stop kills whatever the agent was doing and the transcript is where they look.
// The row's state is updated only after the verification, so the panel does not
// claim READY for a sandbox that never came back.
func (s *server) recoverSandbox(ctx context.Context, sb *contracts.Sandbox, reason string) (bool, time.Duration, error) {
	started := time.Now()
	s.markRecovering(sb.ID, true)
	defer s.markRecovering(sb.ID, false)

	sessions := s.sessionsOnSandboxContext(ctx, sb.UserID, sb.ID)
	unresponsive := fmt.Sprintf("沙箱无响应，正在恢复：%s。工作区、对话和文件都在卷上，恢复不会丢数据。", reason)
	for _, id := range sessions {
		if s.deps.Runtime != nil {
			_ = s.deps.Runtime.Stop(ctx, id)
		}
		// The agent lives inside the sandbox and outlives this process's stream;
		// the container is about to be stopped, which ends it, so only the event
		// is written here.
		s.appendSandboxEvent(ctx, sb.UserID, id, contracts.ErrSandboxUnresponsive, unresponsive, map[string]any{
			"sandbox_id": sb.ID,
			"reason":     reason,
		})
	}
	s.log.WarnContext(ctx, "sandbox recovery: stopping and starting the container",
		slog.String("sandbox", sb.ID),
		slog.String("user", sb.UserID),
		slog.String("reason", reason),
		slog.Int("sessions", len(sessions)))

	if err := s.deps.Sandbox.Pause(ctx, sb.ID); err != nil {
		s.failRecovery(ctx, sb, sessions, fmt.Sprintf("恢复失败：无法停止沙箱容器（%v）。工作区数据未受影响；请重试，或联系管理员。", err))
		return false, time.Since(started), fmt.Errorf("pause %s: %w", sb.ID, err)
	}
	live, err := s.deps.Sandbox.Resume(ctx, sb.ID)
	if err != nil {
		s.failRecovery(ctx, sb, sessions, fmt.Sprintf("恢复失败：沙箱容器停止后无法重新启动（%v）。工作区数据未受影响；请重试，或联系管理员。", err))
		return false, time.Since(started), fmt.Errorf("resume %s: %w", sb.ID, err)
	}

	// Verification is the difference between "we restarted it" and "it works".
	// A container that starts but whose own API never answers is still wedged,
	// and saying "recovered" would send the user back into the same wall.
	if err := s.awaitSandboxProbe(ctx, sb.ID); err != nil {
		s.failRecovery(ctx, sb, sessions, fmt.Sprintf("沙箱已重启但仍无响应（%v）。已保留工作区数据；请稍后点击「恢复」重试。", err))
		return false, time.Since(started), err
	}

	elapsed := time.Since(started)
	if live == nil {
		live = &contracts.Sandbox{ID: sb.ID, State: contracts.SandboxReady}
	}
	state := live.State
	if state == "" {
		state = contracts.SandboxReady
	}
	message := fmt.Sprintf("sandbox recovered after a restart (%s)", fmtDuration(elapsed))
	if err := s.deps.Store.UpdateSandboxState(ctx, sb.UserID, sb.ID, state, message); err != nil {
		s.log.WarnContext(ctx, "sandbox recovery: could not record the recovered state",
			slog.String("sandbox", sb.ID), slog.String("error", err.Error()))
	}
	done := fmt.Sprintf("沙箱已恢复（用时 %.1f 秒）。对话和工作区都还在，可以继续。", elapsed.Seconds())
	for _, id := range sessions {
		s.appendSandboxEvent(ctx, sb.UserID, id, contracts.ErrSandboxRecovered, done, map[string]any{
			"sandbox_id": sb.ID,
			"elapsed_ms": elapsed.Milliseconds(),
		})
	}
	s.noteProbeRecovered(sb.ID, elapsed)
	s.log.WarnContext(ctx, "sandbox recovery: the sandbox answers again",
		slog.String("sandbox", sb.ID), slog.String("elapsed", elapsed.String()))
	return true, elapsed, nil
}

// awaitSandboxProbe waits for a restarted sandbox to answer again, bounded by the
// caller's context.
func (s *server) awaitSandboxProbe(ctx context.Context, sandboxID string) error {
	var last error
	for {
		if err := ctx.Err(); err != nil {
			if last != nil {
				return fmt.Errorf("%w (last probe: %v)", err, last)
			}
			return err
		}
		probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout())
		err := s.deps.ProbeSandbox(probeCtx, sandboxID)
		cancel()
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// failRecovery records the failure where the user will see it.
func (s *server) failRecovery(ctx context.Context, sb *contracts.Sandbox, sessions []string, message string) {
	s.log.Error("sandbox recovery: failed",
		slog.String("sandbox", sb.ID), slog.String("message", message))
	for _, id := range sessions {
		s.appendSandboxEvent(ctx, sb.UserID, id, contracts.ErrSandboxRecoveryFailed, message, map[string]any{
			"sandbox_id": sb.ID,
		})
	}
}

// appendSandboxEvent writes one platform.error into a session's transcript.
//
// A write failure is logged, never fatal: the recovery already happened, and a
// missing event must not turn it into an error the caller has to handle.
func (s *server) appendSandboxEvent(ctx context.Context, userID, sessionID, code, message string, extra map[string]any) {
	if s.deps.Store == nil || sessionID == "" {
		return
	}
	payload := map[string]any{"code": code, "message": message}
	for key, value := range extra {
		payload[key] = value
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := &contracts.Event{
		SessionID: sessionID,
		Type:      contracts.EvPlatformErr,
		Payload:   body,
		TS:        time.Now(),
	}
	if _, err := s.deps.Store.AppendEvent(context.WithoutCancel(ctx), userID, event); err != nil {
		s.log.WarnContext(ctx, "could not record a sandbox-recovery event",
			"session", sessionID, "error", err.Error())
	}
}

// --- probe bookkeeping -------------------------------------------------------

// noteProbe records a healthy probe and clears the strike counter.
func (s *server) noteProbe(sandboxID string, ok bool, probeErr string) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	verdict := s.probeState[sandboxID]
	verdict.OK = ok
	verdict.Error = probeErr
	verdict.At = time.Now()
	s.probeState[sandboxID] = verdict
	if ok {
		delete(s.probeStrikes, sandboxID)
	}
}

// noteProbeFailure records a failed probe and returns the new strike count.
func (s *server) noteProbeFailure(sandboxID, probeErr string) int {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	verdict := s.probeState[sandboxID]
	verdict.OK = false
	verdict.Error = probeErr
	verdict.At = time.Now()
	s.probeState[sandboxID] = verdict
	s.probeStrikes[sandboxID]++
	return s.probeStrikes[sandboxID]
}

// noteProbeRecovered records a successful recovery.
func (s *server) noteProbeRecovered(sandboxID string, elapsed time.Duration) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	verdict := s.probeState[sandboxID]
	verdict.OK = true
	verdict.Error = ""
	verdict.At = time.Now()
	verdict.RecoveredAt = time.Now()
	verdict.Restarts++
	s.probeState[sandboxID] = verdict
	delete(s.probeStrikes, sandboxID)
}

// probeSnapshot returns the last verdict for one sandbox, if it was ever probed.
func (s *server) probeSnapshot(sandboxID string) (sandboxProbeVerdict, bool) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	verdict, ok := s.probeState[sandboxID]
	return verdict, ok
}

func (s *server) isRecovering(sandboxID string) bool {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	return s.recovering[sandboxID]
}

func (s *server) beginRecovery(sandboxID string) bool {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.recovering == nil {
		s.recovering = map[string]bool{}
	}
	if s.recovering[sandboxID] {
		return false
	}
	s.recovering[sandboxID] = true
	return true
}

func (s *server) endRecovery(sandboxID string) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	delete(s.recovering, sandboxID)
}

func (s *server) markRecovering(sandboxID string, on bool) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	verdict := s.probeState[sandboxID]
	verdict.Recovering = on
	verdict.At = time.Now()
	s.probeState[sandboxID] = verdict
}

// handleSandboxRecover is the one-click repair.
//
// It probes first: a sandbox that answers must not be restarted, because
// restarting it throws away its /tmp and whatever the agent is doing. When the
// probe fails it runs the same repair the watchdog does, synchronously, and
// answers with what happened — the button's label is a promise, and the reply is
// where it is kept.
func (s *server) handleSandboxRecover(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.sandboxFor(w, r)
	if !ok {
		return
	}
	if s.deps.ProbeSandbox == nil || s.deps.Sandbox == nil {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail,
			"这个部署无法在沙箱内运行命令，因此无法检测或恢复无响应的沙箱"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sandboxRecoveryTimeout)
	defer cancel()

	probeCtx, probeCancel := context.WithTimeout(ctx, s.probeTimeout())
	probeErr := s.deps.ProbeSandbox(probeCtx, sb.ID)
	probeCancel()
	if probeErr == nil {
		s.noteProbe(sb.ID, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"state":      sb.State,
			"recovered":  true,
			"restarted":  false,
			"message":    "沙箱有响应，无需恢复。",
			"elapsed_ms": 0,
		})
		return
	}
	s.noteProbe(sb.ID, false, probeErr.Error())
	if !s.beginRecovery(sb.ID) {
		writeJSON(w, http.StatusOK, map[string]any{
			"state":      sb.State,
			"recovered":  false,
			"restarted":  false,
			"recovering": true,
			"message":    "沙箱正在恢复中，请稍候。",
		})
		return
	}
	defer s.endRecovery(sb.ID)

	recovered, elapsed, err := s.recoverSandbox(ctx, sb, "无响应的沙箱被手动恢复（用户点击「恢复」）")
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "沙箱恢复失败：%v", err))
		return
	}
	state := contracts.SandboxReady
	if fresh, serr := s.deps.Store.Sandbox(r.Context(), auth.MustUser(r.Context()).ID, sb.ID); serr == nil && fresh.State != "" {
		state = fresh.State
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":      state,
		"recovered":  recovered,
		"restarted":  true,
		"elapsed_ms": elapsed.Milliseconds(),
		"message":    fmt.Sprintf("沙箱已恢复（用时 %.1f 秒）。对话和工作区都还在，可以继续。", elapsed.Seconds()),
	})
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
