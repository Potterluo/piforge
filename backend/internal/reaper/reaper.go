// Package reaper implements the sandbox idle policy (PRD FR-5.3).
//
// # The state machine
//
//	READY --(idle > pauseAfter)--> Pause --> PAUSED
//	PAUSED --(idle > reclaimAfter)--> Take(snapshot) --> Destroy --> MISSING
//
// The ordering in the second line is the point of the package. Fixation comes
// before release: a sandbox is destroyed only after the snapshot has been
// written and its row committed. A snapshot failure leaves both the provider
// sandbox and the store row exactly as they were, and the next tick retries.
// This is the lesson from genet's cleanup loop (commit the image, then delete
// the pod) applied to a workspace that already outlives the container.
//
// # What counts as idle
//
// The clock is the store's sandboxes.last_active_at, which only user-driven
// paths refresh (a message, a manual resume). The reaper persists state with
// UpdateSandboxStateIdle, which deliberately leaves last_active_at alone: an
// automatic pause must not restart the reclaim countdown.
package reaper

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// Snapshotter is the fixation half of reclaim. *snapshot.Writer implements it.
//
// Take must be durable before it returns nil: it is the only thing standing
// between the reaper and a Destroy.
type Snapshotter interface {
	Take(ctx context.Context, sb *contracts.Sandbox) (*store.Snapshot, error)
}

// Config wires the reaper.
type Config struct {
	Store   *store.Store
	Sandbox contracts.SandboxProvider
	// Snapshots is required when ReclaimAfter > 0.
	Snapshots Snapshotter
	Logger    *slog.Logger
	// Now is injectable so tests can freeze the clock.
	Now func() time.Time
	// PauseAfter pauses READY sandboxes idle longer than this. 0 disables the
	// pause stage.
	PauseAfter time.Duration
	// ReclaimAfter destroys PAUSED sandboxes idle longer than this. 0 disables
	// the reclaim stage.
	ReclaimAfter time.Duration
	// Interval is how often Start runs. Defaults to one minute.
	Interval time.Duration
	// OnPaused is called after the provider has stopped a sandbox and before the
	// row is recorded PAUSED. It is where the control plane stops the agent
	// processes on that sandbox, so "paused" describes the machine rather than
	// only the label. Nil is allowed.
	OnPaused func(ctx context.Context, sb *contracts.Sandbox)
}

// Result counts one RunOnce. Failed attempts are counted and the sandbox is
// left for the next tick; they are not errors of the loop itself.
type Result struct {
	Paused           int
	Reclaimed        int
	PauseFailures    int
	SnapshotFailures int
	DestroyFailures  int
}

// Reaper runs the idle policy. It is safe for concurrent use only in the sense
// that two RunOnce calls may race: both queries re-check state, and the store
// update is the last writer. Start runs one goroutine.
type Reaper struct {
	cfg Config
}

// New builds a reaper. PauseAfter and ReclaimAfter may both be 0, in which case
// Start returns immediately (an explicitly disabled policy).
func New(cfg Config) *Reaper {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	return &Reaper{cfg: cfg}
}

// Enabled reports whether either stage is active.
func (r *Reaper) Enabled() bool {
	return r.cfg.PauseAfter > 0 || r.cfg.ReclaimAfter > 0
}

// Start runs RunOnce immediately and then on every tick until ctx is done.
//
// An immediate first pass matters after a restart: a deployment that was down
// over the weekend should not wait a full interval before reclaiming what has
// already been idle for weeks.
func (r *Reaper) Start(ctx context.Context) {
	if !r.Enabled() {
		return
	}
	r.pass(ctx)
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.pass(ctx)
		}
	}
}

func (r *Reaper) pass(ctx context.Context) {
	if _, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
		r.cfg.Logger.Error("sandbox idle: reaper pass failed", "error", err.Error())
	}
}

// RunOnce performs one pass of both stages.
//
// Both candidate lists are read before either stage acts, so a sandbox that is
// READY at the start of this pass cannot be paused and destroyed in the same
// tick. That costs one interval of grace and makes the two transitions visible
// separately in the log, which is what an operator debugging a surprise
// deletion needs.
func (r *Reaper) RunOnce(ctx context.Context) (Result, error) {
	var res Result
	if !r.Enabled() {
		return res, nil
	}
	now := r.cfg.Now().UTC()

	var pauseCandidates []*contracts.Sandbox
	if r.cfg.PauseAfter > 0 {
		before := now.Add(-r.cfg.PauseAfter)
		list, err := r.cfg.Store.ReapableSandboxes(ctx, contracts.SandboxReady, before)
		if err != nil {
			return res, fmt.Errorf("reaper: list ready sandboxes: %w", err)
		}
		pauseCandidates = list
	}

	var reclaimCandidates []*contracts.Sandbox
	if r.cfg.ReclaimAfter > 0 {
		before := now.Add(-r.cfg.ReclaimAfter)
		list, err := r.cfg.Store.ReapableSandboxes(ctx, contracts.SandboxPaused, before)
		if err != nil {
			return res, fmt.Errorf("reaper: list paused sandboxes: %w", err)
		}
		reclaimCandidates = list
	}

	for _, sb := range pauseCandidates {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		r.pause(ctx, sb, now, &res)
	}
	for _, sb := range reclaimCandidates {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		r.reclaim(ctx, sb, now, &res)
	}
	return res, nil
}

// pause stops a READY sandbox and records PAUSED. A provider failure leaves the
// row READY so the next tick retries; recording PAUSED for a sandbox that is
// still running would make the label lie.
func (r *Reaper) pause(ctx context.Context, sb *contracts.Sandbox, now time.Time, res *Result) {
	current, ok := r.recheck(ctx, sb, contracts.SandboxReady)
	if !ok {
		return
	}
	if current.LastActiveAt.After(sb.LastActiveAt) {
		// A message arrived between the listing and this loop. That is activity;
		// the next tick will recompute idleness from the newer timestamp.
		r.cfg.Logger.Debug("sandbox idle: skip pause, activity arrived", "sandbox", sb.ID)
		return
	}
	r.cfg.Logger.Info("sandbox idle: pausing",
		"sandbox", sb.ID, "user", sb.UserID, "project", sb.ProjectID,
		"last_active_at", sb.LastActiveAt.UTC().Format(time.RFC3339),
		"idle", now.Sub(sb.LastActiveAt).Round(time.Second).String())
	if err := r.cfg.Sandbox.Pause(ctx, sb.ID); err != nil {
		res.PauseFailures++
		r.cfg.Logger.Warn("sandbox idle: pause failed; leaving it READY for the next tick",
			"sandbox", sb.ID, "error", err.Error())
		return
	}
	if r.cfg.OnPaused != nil {
		r.cfg.OnPaused(ctx, sb)
	}
	msg := fmt.Sprintf("idle pause after %s", now.Sub(sb.LastActiveAt).Round(time.Second))
	if err := r.cfg.Store.UpdateSandboxStateIdle(ctx, sb.UserID, sb.ID, contracts.SandboxPaused, msg); err != nil {
		res.PauseFailures++
		r.cfg.Logger.Warn("sandbox idle: paused but could not record PAUSED",
			"sandbox", sb.ID, "error", err.Error())
		return
	}
	res.Paused++
	r.cfg.Logger.Info("sandbox idle: paused", "sandbox", sb.ID)
}

// recheck reloads a candidate immediately before acting on it. The listing is a
// snapshot in time; a wake-up between the query and the loop must win.
func (r *Reaper) recheck(ctx context.Context, sb *contracts.Sandbox, want contracts.SandboxState) (*contracts.Sandbox, bool) {
	row, err := r.cfg.Store.Sandbox(ctx, sb.UserID, sb.ID)
	if err != nil {
		r.cfg.Logger.Warn("sandbox idle: candidate vanished; skipping", "sandbox", sb.ID, "error", err.Error())
		return nil, false
	}
	if row.State != want {
		return nil, false
	}
	return row, true
}

// reclaim fixes then releases. Every failure path before Destroy leaves the
// sandbox alone on purpose.
func (r *Reaper) reclaim(ctx context.Context, sb *contracts.Sandbox, now time.Time, res *Result) {
	current, ok := r.recheck(ctx, sb, contracts.SandboxPaused)
	if !ok {
		return
	}
	if current.LastActiveAt.After(sb.LastActiveAt) {
		r.cfg.Logger.Debug("sandbox idle: skip reclaim, activity arrived", "sandbox", sb.ID)
		return
	}
	idle := now.Sub(sb.LastActiveAt).Round(time.Second)
	r.cfg.Logger.Info("sandbox idle: reclaiming",
		"sandbox", sb.ID, "user", sb.UserID, "project", sb.ProjectID,
		"last_active_at", sb.LastActiveAt.UTC().Format(time.RFC3339),
		"idle", idle.String())

	// Fixate first. Nothing below this point runs unless the manifest and its
	// row are both durable.
	snap, err := r.cfg.Snapshots.Take(ctx, sb)
	if err != nil {
		res.SnapshotFailures++
		r.cfg.Logger.Error("sandbox idle: snapshot failed; sandbox left untouched",
			"sandbox", sb.ID, "error", err.Error())
		return
	}
	if snap == nil {
		// A snapshotter that reports success without a durable row is broken; a
		// nil result must never be read as permission to destroy.
		res.SnapshotFailures++
		r.cfg.Logger.Error("sandbox idle: snapshot returned no record; sandbox left untouched", "sandbox", sb.ID)
		return
	}
	snapKey := snap.BlobKey
	r.cfg.Logger.Info("sandbox idle: snapshot written",
		"sandbox", sb.ID, "snapshot", snapKey)

	// Release second.
	if err := r.cfg.Sandbox.Destroy(ctx, sb.ID); err != nil {
		res.DestroyFailures++
		r.cfg.Logger.Error("sandbox idle: destroy failed after snapshot; leaving it PAUSED for the next tick",
			"sandbox", sb.ID, "snapshot", snapKey, "error", err.Error())
		return
	}
	msg := fmt.Sprintf("reclaimed after %s idle; snapshot %s", idle, snapKey)
	if err := r.cfg.Store.UpdateSandboxStateIdle(ctx, sb.UserID, sb.ID, contracts.SandboxMissing, msg); err != nil {
		res.DestroyFailures++
		r.cfg.Logger.Error("sandbox idle: destroyed but could not record MISSING",
			"sandbox", sb.ID, "snapshot", snapKey, "error", err.Error())
		return
	}
	res.Reclaimed++
	r.cfg.Logger.Info("sandbox idle: reclaimed", "sandbox", sb.ID, "snapshot", snapKey)
}
