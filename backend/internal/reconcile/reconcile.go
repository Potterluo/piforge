// Package reconcile repairs the one state a control-plane restart leaves
// behind that nothing else owns: a session (and run) that SQLite still calls
// "running" while no agent process exists.
//
// # Why this is its own package
//
// The authority is a probe (contracts.LivenessProbe), not a row. On boot the
// in-process registry is empty by definition, so every persisted "running" row
// looks like a leftover from the process that died; during a run it is
// populated, so a live turn is never touched. Keeping the rule here means the
// boot pass, the watchdog, and the project-delete guard all ask the same
// question and write the same record.
//
// # Which authority a deployment asks
//
// With -sandbox local the agent is a child of this process, so the in-process
// registry is the whole truth and a restart really does kill every run. With
// -sandbox docker/runner the agent lives beside its workspace and outlives this
// process: the registry is empty after a restart while the agents are still
// running, so a registry-only reconciler would write "interrupted" over live
// runs. cmd/piforged therefore wires a liveness.Composite there — the registry
// *and* a heartbeat marker read from the sandbox (internal/liveness).
//
// # Conservatism
//
// Reconciliation never guesses, and "unknown" is never written as "dead":
//
//   - no authority at all (nil probe and nil registry) leaves every row alone
//     and says so;
//   - a probe that returns an error is unknown: the row is left running and a
//     WARN names the session, because an unreachable sandbox is not evidence
//     that a run died;
//   - only a definitive false interrupts;
//   - a failed store write changes nothing.
//
// The only state this package writes is for a session that claims a turn is in
// flight and whose authority explicitly says nobody is serving it.
package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/recovery"
	"github.com/piforge/piforge/backend/internal/store"
)

// The two reasons a run is interrupted. They are user-visible (they land in the
// transcript as platform.error and in runs.completion_note), so they are
// sentences rather than error codes. The copy is owned by internal/recovery,
// which also composes the "继续" follow-up: one sentence, one source.
const (
	// ReasonRestart is the boot pass: the process that was running the turn is
	// gone with the previous control plane.
	ReasonRestart = recovery.ReasonRestart
	// ReasonProcessGone is the watchdog / on-demand pass: the control plane is
	// the same process, but the agent child it started is no longer there.
	ReasonProcessGone = recovery.ReasonProcessGone
)

// Liveness is the in-process authority for "is an agent serving this session".
//
// pi.Runtime implements it. It is deliberately a one-question interface: the
// reconciler must not reach for Status (the normalizer's view, which can be
// stale or zero) or for the hub's subscription cache (a subscription outlives a
// dead child).
//
// It is the *local* authority. A deployment whose agents live outside this
// process configures Config.Probe instead; Config.Runtime remains for the
// local provider and for callers that have no probe.
type Liveness interface {
	Alive(sessionID string) bool
}

// DefaultProbeTimeout bounds one probe call. A probe talks to a sandbox (an
// HTTP round trip); without a bound, one unreachable sandbox would stall the
// whole boot pass and every watchdog tick behind it. Exceeding it is an error,
// i.e. "unknown", i.e. the session keeps running.
const DefaultProbeTimeout = 10 * time.Second

// Config wires a Reconciler. Store is required; Probe and Runtime may both be
// nil, in which case the reconciler refuses to interrupt anything.
type Config struct {
	Store *store.Store
	// Probe is the liveness authority to ask, and takes precedence over Runtime
	// when both are set. A deployment with agents outside this process must set
	// it: the in-process registry cannot see them.
	Probe contracts.LivenessProbe
	// Runtime is the in-process registry, used when Probe is nil. Nil together
	// with a nil Probe means "no authority": nothing is interrupted.
	Runtime Liveness
	// ProbeTimeout bounds one probe call (default DefaultProbeTimeout).
	ProbeTimeout time.Duration
	Logger       *slog.Logger
	Now          func() time.Time
}

// Report is the summary one pass logs and tests assert on.
type Report struct {
	// Scanned is how many sessions claimed to be running.
	Scanned int
	// Interrupted is how many sessions this pass actually moved to interrupted.
	Interrupted int
	// Runs is how many started runs were settled as interrupted.
	Runs int
	// Live is how many candidates an authority proved alive and were left alone.
	Live int
	// Unknown is how many candidates the authority could not answer for. They
	// were left running: unknown never interrupts.
	Unknown int
}

// Reconciler reconciles persisted run state against the live agent authority.
type Reconciler struct {
	cfg Config
}

// New returns a reconciler. Nil logger/clock fall back to the process defaults.
func New(cfg Config) *Reconciler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = DefaultProbeTimeout
	}
	switch {
	case cfg.Probe != nil:
		// Nothing to say here: the caller names the probe it wired (the startup
		// log in cmd/piforged does), and this package must not print a type name
		// it does not own.
	case cfg.Runtime != nil:
		// The one deployment shape that can rely on the registry: the agent is a
		// child of this process. Say so, because "a restart killed every run" and
		// "a restart left runs alive" are different products.
		cfg.Logger.Info("session reconciliation asks the in-process agent registry: no liveness probe is configured",
			"detail", "correct for -sandbox local, where the agent is a child of this process; a container provider must configure a probe or a restart will interrupt live runs")
	}
	return &Reconciler{cfg: cfg}
}

// Run reconciles every session in the database that claims a turn is in flight.
// It is what the boot pass and the watchdog call.
func (r *Reconciler) Run(ctx context.Context, reason, phase string) (Report, error) {
	if r.cfg.Store == nil {
		return Report{}, errors.New("reconcile: a store is required")
	}
	candidates, err := r.cfg.Store.RunningSessions(ctx)
	if err != nil {
		return Report{}, err
	}
	return r.reconcile(ctx, candidates, reason, phase)
}

// Project reconciles only the sessions of one project. The delete guard calls it
// before it trusts a "running" row, so a project pinned by a crash becomes
// deletable in the same request instead of asking the user to retry.
func (r *Reconciler) Project(ctx context.Context, userID, projectID, reason, phase string) (Report, error) {
	if r.cfg.Store == nil {
		return Report{}, errors.New("reconcile: a store is required")
	}
	candidates, err := r.cfg.Store.RunningSessionsInProject(ctx, userID, projectID)
	if err != nil {
		return Report{}, err
	}
	return r.reconcile(ctx, candidates, reason, phase)
}

func (r *Reconciler) reconcile(ctx context.Context, candidates []store.RunningSession, reason, phase string) (Report, error) {
	report := Report{Scanned: len(candidates)}
	if len(candidates) == 0 {
		r.summary(phase, report, reason)
		return report, nil
	}
	if r.cfg.Probe == nil && r.cfg.Runtime == nil {
		// No authority means no way to prove a run is dead. Leaving it running is
		// the conservative answer; lying about it would be worse than the stale
		// row.
		r.cfg.Logger.WarnContext(ctx, "session reconciliation skipped: the agent registry is unavailable",
			"phase", phase, "scanned", report.Scanned, "reason", reason)
		report.Live = len(candidates)
		r.summary(phase, report, reason)
		return report, nil
	}

	var errs []error
	for _, candidate := range candidates {
		alive, lerr := r.authority(ctx, candidate.SessionID)
		if lerr != nil {
			// Unknown, not dead. The probe could not answer — an unreachable
			// sandbox, a missing key, a timeout — and an unavailable authority
			// is not evidence that a run died. Leave the row alone and name the
			// session so an operator can see which authority went quiet.
			report.Unknown++
			r.cfg.Logger.WarnContext(ctx, "session liveness is unknown; the session is left running",
				"phase", phase,
				"session", candidate.SessionID,
				"project", candidate.ProjectID,
				"reason", reason,
				"error", lerr.Error())
			continue
		}
		if alive {
			report.Live++
			r.cfg.Logger.DebugContext(ctx, "session kept running: an authority still serves it",
				"phase", phase, "session", candidate.SessionID, "project", candidate.ProjectID)
			continue
		}
		claim, err := r.cfg.Store.InterruptRunningSession(ctx, candidate.UserID, candidate.SessionID,
			recovery.CauseFromReason(reason), reason, r.cfg.Now())
		if err != nil {
			errs = append(errs, err)
			r.cfg.Logger.WarnContext(ctx, "session reconciliation could not interrupt a run",
				"phase", phase, "session", candidate.SessionID, "project", candidate.ProjectID, "error", err.Error())
			continue
		}
		if !claim.Changed() {
			// Another pass (or the delete handler) got there first. Still list it
			// as handled so a second boot pass logs an honest zero.
			r.cfg.Logger.DebugContext(ctx, "session already reconciled", "phase", phase, "session", candidate.SessionID)
			continue
		}
		report.Interrupted++
		report.Runs += len(claim.RunIDs)
		r.cfg.Logger.InfoContext(ctx, "interrupted an orphaned run",
			"phase", phase,
			"session", candidate.SessionID,
			"project", candidate.ProjectID,
			"runs", len(claim.RunIDs),
			"reason", reason)
	}
	r.summary(phase, report, reason)
	return report, errors.Join(errs...)
}

// authority asks the configured liveness source. An error is "unknown" to every
// caller: the probe was asked and could not answer.
//
// The registry is synchronous and has no failure mode, so it is called directly;
// a probe talks to a sandbox and is bounded, because a boot pass that hangs on a
// dead sandbox is a deployment that never opens its port.
func (r *Reconciler) authority(ctx context.Context, sessionID string) (bool, error) {
	if r.cfg.Probe == nil {
		return r.cfg.Runtime.Alive(sessionID), nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	defer cancel()
	return r.cfg.Probe.Alive(probeCtx, sessionID)
}

// summary is the one line an operator can find after a restart. The watchdog is
// silent when it found nothing (it runs every interval); boot and on-demand
// passes always report.
func (r *Reconciler) summary(phase string, report Report, reason string) {
	quiet := phase == "watchdog" && report.Interrupted == 0 && report.Unknown == 0
	if quiet {
		r.cfg.Logger.Debug("session reconciliation found nothing to repair",
			"phase", phase, "scanned", report.Scanned)
		return
	}
	r.cfg.Logger.Info("session reconciliation",
		"phase", phase,
		"scanned", report.Scanned,
		"interrupted", report.Interrupted,
		"runs", report.Runs,
		"kept_live", report.Live,
		"unknown", report.Unknown,
		"reason", reason)
}

// Start runs the watchdog until ctx is cancelled. An interval of zero disables
// the periodic pass; the boot pass is the caller's and always runs.
func (r *Reconciler) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		r.cfg.Logger.Info("session reconciliation watchdog disabled", "reason", "interval is 0")
		return
	}
	r.cfg.Logger.Info("session reconciliation watchdog enabled", "interval", interval.String())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A pass that fails must not stop the watchdog: the next tick is the
			// retry, and the failure is already logged with the session.
			if _, err := r.Run(ctx, ReasonProcessGone, "watchdog"); err != nil {
				r.cfg.Logger.Warn("session reconciliation watchdog pass failed", "error", err.Error())
			}
		}
	}
}

// InterruptedStatus is exposed so callers do not have to import contracts just
// to describe what this package writes.
func InterruptedStatus() contracts.SessionStatus { return contracts.StatusInterrupted }
