// Package reconcile repairs the one state a control-plane restart leaves
// behind that nothing else owns: a session (and run) that SQLite still calls
// "running" while no agent process exists.
//
// # Why this is its own package
//
// The authority is the *in-process* agent registry (pi.Runtime.Alive). On boot
// it is empty by definition, so every persisted "running" row is a leftover from
// the process that died; during a run it is populated, so a live turn is never
// touched. Keeping the rule here means the boot pass, the watchdog, and the
// project-delete guard all ask the same question and write the same record.
//
// # Conservatism
//
// Reconciliation never guesses. A runtime that does not implement Liveness (or
// is nil) leaves every row alone and says so; an Alive session is left running;
// a failed store write changes nothing. The only state this package writes is
// for a session that claims a turn is in flight and that the registry explicitly
// says nobody is serving.
package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// The two reasons a run is interrupted. They are user-visible (they land in the
// transcript as platform.error and in runs.completion_note), so they are
// sentences rather than error codes.
const (
	// ReasonRestart is the boot pass: the process that was running the turn is
	// gone with the previous control plane.
	ReasonRestart = "服务重启，本次运行被中断"
	// ReasonProcessGone is the watchdog / on-demand pass: the control plane is
	// the same process, but the agent child it started is no longer there.
	ReasonProcessGone = "运行进程已退出，本次运行被中断"
)

// Liveness is the in-process authority for "is an agent serving this session".
//
// pi.Runtime implements it. It is deliberately a one-question interface: the
// reconciler must not reach for Status (the normalizer's view, which can be
// stale or zero) or for the hub's subscription cache (a subscription outlives a
// dead child).
type Liveness interface {
	Alive(sessionID string) bool
}

// Config wires a Reconciler. Store is required; Runtime may be nil, in which
// case the reconciler refuses to interrupt anything.
type Config struct {
	Store   *store.Store
	Runtime Liveness
	Logger  *slog.Logger
	Now     func() time.Time
}

// Report is the summary one pass logs and tests assert on.
type Report struct {
	// Scanned is how many sessions claimed to be running.
	Scanned int
	// Interrupted is how many sessions this pass actually moved to interrupted.
	Interrupted int
	// Runs is how many started runs were settled as interrupted.
	Runs int
	// Live is how many candidates the registry proved alive and were left alone.
	Live int
}

// Reconciler reconciles persisted run state against the live agent registry.
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
	if r.cfg.Runtime == nil {
		// No registry means no way to prove a run is dead. Leaving it running is
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
		if r.cfg.Runtime.Alive(candidate.SessionID) {
			report.Live++
			r.cfg.Logger.DebugContext(ctx, "session kept running: the runtime still serves it",
				"phase", phase, "session", candidate.SessionID, "project", candidate.ProjectID)
			continue
		}
		claim, err := r.cfg.Store.InterruptRunningSession(ctx, candidate.UserID, candidate.SessionID, reason, r.cfg.Now())
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

// summary is the one line an operator can find after a restart. The watchdog is
// silent when it found nothing (it runs every interval); boot and on-demand
// passes always report.
func (r *Reconciler) summary(phase string, report Report, reason string) {
	quiet := phase == "watchdog" && report.Interrupted == 0
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
