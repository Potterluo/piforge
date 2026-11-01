package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/diskquota"
)

// Disk quota enforcement (FR-5.8).
//
// The budget is a per-sandbox ceiling on the checkout, enforced when a run is
// about to start. That is the boundary that matters: the user is waiting on this
// turn, the sandbox has just been woken and is about to be worked in, and the
// alternative — letting the run proceed and noticing the volume is full later —
// takes the whole deployment down instead of one turn.
//
// The measurement is a du-style walk (diskquota), not per-write accounting, so a
// run that writes a lot is caught on the *next* turn. That is a real limit and it
// is named as one: what the budget guarantees is that the disk cannot fill
// silently, not that a single turn cannot overshoot.

// sandboxForSession resolves the sandbox behind a session's worktree. A session
// that has not been provisioned has none, and neither does a deployment without a
// sandbox provider: both answer nil, and every caller treats that as "nothing to
// measure or wake", never as an error.
func (s *server) sandboxForSession(ctx context.Context, userID string, sess *contracts.Session) *contracts.Sandbox {
	if sess == nil || sess.WorktreeID == "" || s.deps.Store == nil {
		return nil
	}
	wt, err := s.deps.Store.Worktree(ctx, userID, sess.WorktreeID)
	if err != nil {
		return nil
	}
	sb, err := s.deps.Store.Sandbox(ctx, userID, wt.SandboxID)
	if err != nil {
		return nil
	}
	return sb
}

// enforceSandboxQuota refuses a run when its sandbox checkout is over the disk
// budget, and records what happened.
//
// The event is the notification path: a refused run leaves a `platform.error`
// entry in the session, so the transcript (and the SSE stream, when the tab is
// open) says why the turn did not happen instead of the request quietly 507-ing.
func (s *server) enforceSandboxQuota(ctx context.Context, userID string, sess *contracts.Session) error {
	g := s.diskQuota
	if g == nil || g.Disabled() {
		return nil
	}
	sb := s.sandboxForSession(ctx, userID, sess)
	if sb == nil || strings.TrimSpace(sb.RootDir) == "" {
		return nil
	}
	rep := g.MeasureWith(ctx, sb.ID, sb.RootDir, s.sandboxMeasurer(sb.RootDir))
	if !rep.Over {
		// An unmeasurable sandbox is reported, not refused: the run would fail
		// on its own if the sandbox were unreachable, and a 507 that claims
		// "over quota" for a failed `du` would be a lie the user acts on.
		if rep.Unmeasurable {
			s.log.WarnContext(ctx, "sandbox disk quota: the workspace could not be measured; the run is not refused",
				slog.String("sandbox", sb.ID), slog.String("error", rep.Error))
		}
		return nil
	}

	message := g.Message(rep)
	s.log.WarnContext(ctx, "sandbox is over its disk quota; the run was refused",
		slog.String("sandbox", sb.ID),
		slog.String("user", userID),
		slog.String("project", sb.ProjectID),
		slog.Int64("used_bytes", rep.Bytes),
		slog.Int64("limit_bytes", rep.QuotaBytes))
	s.recordQuotaRefusal(ctx, userID, sess, sb, rep, message)

	return withStatusDetails(http.StatusInsufficientStorage, contracts.ErrSandboxDiskQuota,
		message, map[string]any{
			"sandbox_id":  sb.ID,
			"used_bytes":  rep.Bytes,
			"limit_bytes": rep.QuotaBytes,
			// The screen that can explain the limit uses this, and a client that
			// wants to show a progress bar needs the same pair.
			"used":  diskquota.HumanBytes(rep.Bytes),
			"limit": diskquota.HumanBytes(rep.QuotaBytes),
		})
}

// recordQuotaRefusal appends the refusal to the session's event log. A write
// failure is logged, never fatal: the run is refused either way, and a missing
// event must not turn "over quota" into "internal error".
func (s *server) recordQuotaRefusal(ctx context.Context, userID string, sess *contracts.Session, sb *contracts.Sandbox, rep diskquota.Report, message string) {
	s.appendQuotaRefusal(ctx, userID, sess.ID, sb, rep, message)
}

// appendQuotaRefusal is recordQuotaRefusal by session id, for the callers that
// have no *contracts.Session in hand (the periodic disk watchdog).
func (s *server) appendQuotaRefusal(ctx context.Context, userID, sessionID string, sb *contracts.Sandbox, rep diskquota.Report, message string) {
	payload, err := json.Marshal(map[string]any{
		"code":        contracts.ErrSandboxDiskQuota,
		"message":     message,
		"sandbox_id":  sb.ID,
		"used_bytes":  rep.Bytes,
		"limit_bytes": rep.QuotaBytes,
	})
	if err != nil {
		return
	}
	event := &contracts.Event{
		SessionID: sessionID,
		Type:      contracts.EvPlatformErr,
		Payload:   payload,
		TS:        time.Now(),
	}
	if _, err := s.deps.Store.AppendEvent(context.WithoutCancel(ctx), userID, event); err != nil {
		s.log.WarnContext(ctx, "could not record a disk-quota refusal",
			"session", sessionID, "error", err.Error())
	}
}

// sandboxUsageRow is one line of the admin status's per-sandbox disk report.
//
// It carries the owner and the project next to the numbers because "a sandbox is
// at 91% of its budget" without "whose" is not something an operator can act on.
type sandboxUsageRow struct {
	SandboxID  string    `json:"sandbox_id"`
	UserID     string    `json:"user_id"`
	ProjectID  string    `json:"project_id"`
	Bytes      int64     `json:"bytes"`
	Files      int       `json:"files"`
	QuotaBytes int64     `json:"quota_bytes"`
	Over       bool      `json:"over"`
	Truncated  bool      `json:"truncated,omitempty"`
	MeasuredAt time.Time `json:"measured_at"`
	// Unmeasurable says this row's Bytes is not a measurement. The page shows
	// "unknown" for it instead of a zero, which is the difference between "this
	// sandbox is empty" and "this deployment cannot see it".
	Unmeasurable bool   `json:"unmeasurable,omitempty"`
	Error        string `json:"error,omitempty"`
}

// sandboxUsage measures every sandbox against the budget. The guardian caches a
// report for a minute, so the admin page's 15-second poll does not turn into a
// disk scan of every checkout.
func (s *server) sandboxUsage(ctx context.Context) []sandboxUsageRow {
	rows := []sandboxUsageRow{}
	if s.deps.Store == nil {
		return rows
	}
	dirs, err := s.deps.Store.SandboxDirectories(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "admin status: sandbox usage unavailable", "error", err.Error())
		return rows
	}
	for _, dir := range dirs {
		rep := s.diskQuota.CachedWith(ctx, dir.ID, dir.RootDir, s.sandboxMeasurer(dir.RootDir))
		rows = append(rows, sandboxUsageRow{
			SandboxID:    dir.ID,
			UserID:       dir.UserID,
			ProjectID:    dir.ProjectID,
			Bytes:        rep.Bytes,
			Files:        rep.Files,
			QuotaBytes:   rep.QuotaBytes,
			Over:         rep.Over,
			Truncated:    rep.Truncated,
			MeasuredAt:   rep.MeasuredAt,
			Unmeasurable: rep.Unmeasurable,
			Error:        rep.Error,
		})
	}
	return rows
}
