package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// sessionCols is the projection every session query scans, in structural
// order. Nullable foreign keys are flattened to ” because contracts models
// them as plain strings with omitempty.
const sessionCols = `id, user_id, project_id, COALESCE(worktree_id, ''), kind,
	COALESCE(root_id, ''), COALESCE(parent_id, ''), fork_entry_id, label, title,
	status, model_ref, thinking_level, created_at, last_active_at`

// NewSession is the input for CreateSession.
type NewSession struct {
	// ProjectID is required and must belong to the caller.
	ProjectID string
	// WorktreeID is optional at creation time: the worktree manager attaches
	// one asynchronously, and sidecars share their parent's.
	WorktreeID string
	// Kind defaults to contracts.KindMain.
	Kind contracts.SessionKind
	// RootID identifies the branch tree. Empty means "this session is its own
	// root", which is what a main session wants.
	RootID string
	// ParentID is the session this one was forked from (branch) or attached to
	// (sidecar).
	ParentID string
	// ForkEntryID is the Pi entry the branch was forked at.
	ForkEntryID   string
	Label         string
	Title         string
	Status        contracts.SessionStatus
	ModelRef      string
	ThinkingLevel string
}

// SessionFilter narrows ListSessions. The zero value lists everything the
// caller owns, newest first.
type SessionFilter struct {
	ProjectID string
	RootID    string
	Status    contracts.SessionStatus
	Kind      contracts.SessionKind
	// Query is a case-insensitive substring match on title and label.
	Query string
	// Cursor is the id of the last row of the previous page.
	Cursor string
	// Limit defaults to 50 and is capped at 200.
	Limit int
}

// SessionPatch carries the fields PATCH /v1/sessions/{id} may change. A nil
// pointer means "leave unchanged", which is why the fields are pointers.
type SessionPatch struct {
	Title         *string
	Label         *string
	ModelRef      *string
	ThinkingLevel *string
	Status        *contracts.SessionStatus
	WorktreeID    *string
}

// statuses is the closed set of session statuses (contracts.SessionStatus).
var sessionStatuses = map[contracts.SessionStatus]bool{
	contracts.StatusActive:   true,
	contracts.StatusRunning:  true,
	contracts.StatusIdle:     true,
	contracts.StatusPaused:   true,
	contracts.StatusSettled:  true,
	contracts.StatusFailed:   true,
	contracts.StatusArchived: true,
}

// CreateSession inserts a session owned by userID.
func (s *Store) CreateSession(ctx context.Context, userID string, in NewSession) (*contracts.Session, error) {
	if _, err := s.Project(ctx, userID, in.ProjectID); err != nil {
		return nil, err
	}
	kind := in.Kind
	if kind == "" {
		kind = contracts.KindMain
	}
	if kind != contracts.KindMain && kind != contracts.KindBranch && kind != contracts.KindSidecar {
		return nil, fmt.Errorf("store: create session: unknown kind %q: %w", kind, ErrInvalid)
	}
	status := in.Status
	if status == "" {
		status = contracts.StatusActive
	}
	if !sessionStatuses[status] {
		return nil, fmt.Errorf("store: create session: unknown status %q: %w", status, ErrInvalid)
	}

	id := NewID("ses")
	rootID := in.RootID
	if rootID == "" && in.ParentID == "" {
		rootID = id
	}
	now := s.now()
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, project_id, worktree_id, kind, root_id, parent_id,
		                       fork_entry_id, label, title, status, model_ref, thinking_level,
		                       config, created_at, last_active_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`,
		id, userID, in.ProjectID, nullString(in.WorktreeID), string(kind), nullString(rootID),
		nullString(in.ParentID), in.ForkEntryID, in.Label, in.Title, string(status),
		in.ModelRef, in.ThinkingLevel, formatTime(now), formatTime(now)); err != nil {
		return nil, fmt.Errorf("store: create session: %w", err)
	}
	return s.Session(ctx, userID, id)
}

// Session loads one session owned by the caller. A session owned by somebody
// else is ErrNotFound, never ErrForbidden: the API must not reveal existence.
func (s *Store) Session(ctx context.Context, userID, sessionID string) (*contracts.Session, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: session %q: %w", sessionID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: session %q: %w", sessionID, err)
	}
	return sess, nil
}

// ListSessions lists the caller's sessions, newest activity first.
func (s *Store) ListSessions(ctx context.Context, userID string, f SessionFilter) ([]*contracts.Session, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	where := []string{`user_id = ?`}
	args := []any{userID}
	if f.ProjectID != "" {
		where = append(where, `project_id = ?`)
		args = append(args, f.ProjectID)
	}
	if f.RootID != "" {
		where = append(where, `(root_id = ? OR id = ?)`)
		args = append(args, f.RootID, f.RootID)
	}
	if f.Status != "" {
		where = append(where, `status = ?`)
		args = append(args, string(f.Status))
	}
	if f.Kind != "" {
		where = append(where, `kind = ?`)
		args = append(args, string(f.Kind))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, `(LOWER(title) LIKE ? OR LOWER(label) LIKE ?)`)
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like)
	}
	if f.Cursor != "" {
		// Keyset pagination on (last_active_at, id). Resolving the cursor row
		// first keeps the predicate portable across SQLite versions.
		var curActive, curID string
		err := s.r.QueryRowContext(ctx,
			`SELECT last_active_at, id FROM sessions WHERE id = ? AND user_id = ?`,
			f.Cursor, userID).Scan(&curActive, &curID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: list sessions: bad cursor %q: %w", f.Cursor, ErrInvalid)
		}
		if err != nil {
			return nil, fmt.Errorf("store: list sessions: cursor %q: %w", f.Cursor, err)
		}
		where = append(where, `(last_active_at < ? OR (last_active_at = ? AND id < ?))`)
		args = append(args, curActive, curActive, curID)
	}

	q := `SELECT ` + sessionCols + ` FROM sessions WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY last_active_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.Session, 0, limit)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list sessions: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	return out, nil
}

// UpdateSession applies a partial update and returns the fresh row. It bumps
// last_active_at, because a rename or a model switch is user activity.
func (s *Store) UpdateSession(ctx context.Context, userID, sessionID string, p SessionPatch) (*contracts.Session, error) {
	sets := make([]string, 0, 7)
	args := make([]any, 0, 9)

	if p.Title != nil {
		sets = append(sets, `title = ?`)
		args = append(args, *p.Title)
	}
	if p.Label != nil {
		sets = append(sets, `label = ?`)
		args = append(args, *p.Label)
	}
	if p.ModelRef != nil {
		sets = append(sets, `model_ref = ?`)
		args = append(args, *p.ModelRef)
	}
	if p.ThinkingLevel != nil {
		sets = append(sets, `thinking_level = ?`)
		args = append(args, *p.ThinkingLevel)
	}
	if p.WorktreeID != nil {
		sets = append(sets, `worktree_id = ?`)
		args = append(args, nullString(*p.WorktreeID))
	}
	if p.Status != nil {
		if !sessionStatuses[*p.Status] {
			return nil, fmt.Errorf("store: update session: unknown status %q: %w", *p.Status, ErrInvalid)
		}
		sets = append(sets, `status = ?`)
		args = append(args, string(*p.Status))
	}
	if len(sets) == 0 {
		return s.Session(ctx, userID, sessionID)
	}
	sets = append(sets, `last_active_at = ?`)
	args = append(args, formatTime(s.now()))
	args = append(args, sessionID, userID)

	res, err := s.w.ExecContext(ctx,
		`UPDATE sessions SET `+strings.Join(sets, ", ")+` WHERE id = ? AND user_id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: update session %q: %w", sessionID, err)
	}
	if err := requireOneRow(res, "store: update session "+sessionID); err != nil {
		return nil, err
	}
	return s.Session(ctx, userID, sessionID)
}

// TouchSession bumps last_active_at; the message path calls it so the sidebar
// order reflects real activity.
func (s *Store) TouchSession(ctx context.Context, userID, sessionID string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE sessions SET last_active_at = ? WHERE id = ? AND user_id = ?`,
		formatTime(s.now()), sessionID, userID)
	if err != nil {
		return fmt.Errorf("store: touch session %q: %w", sessionID, err)
	}
	return requireOneRow(res, "store: touch session "+sessionID)
}

func scanSession(sc interface{ Scan(dest ...any) error }) (*contracts.Session, error) {
	var (
		sess       contracts.Session
		kind       string
		status     string
		created    string
		lastActive string
	)
	if err := sc.Scan(&sess.ID, &sess.UserID, &sess.ProjectID, &sess.WorktreeID, &kind,
		&sess.RootID, &sess.ParentID, &sess.ForkEntryID, &sess.Label, &sess.Title,
		&status, &sess.ModelRef, &sess.ThinkingLevel, &created, &lastActive); err != nil {
		return nil, err
	}
	sess.Kind = contracts.SessionKind(kind)
	sess.Status = contracts.SessionStatus(status)
	sess.CreatedAt = parseTime(created)
	sess.LastActiveAt = parseTime(lastActive)
	return &sess, nil
}

// DeleteSession removes a session and everything scoped to it.
//
// The foreign keys cascade (runs, events, tool_calls, messages), so one delete
// is enough for the data. What cannot be cascaded is the worktree on disk and the
// running agent: the caller stops the agent first, and the worktree is left for
// the provisioner to reclaim, because deleting a directory out from under a live
// process is how data gets corrupted rather than cleaned up.
func (s *Store) DeleteSession(ctx context.Context, userID, sessionID string) error {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return fmt.Errorf("store: delete session %q: %w", sessionID, err)
	}
	return requireOneRow(res, "store: delete session "+sessionID)
}

// SessionAny returns a session without an ownership check.
//
// Only the support-access path uses it: the admin has already recorded why they
// are reading and the owner has a receipt for it. Every other caller must use
// Session, which is what makes "an admin cannot read your conversations" true by
// default rather than by good intentions.
func (s *Store) SessionAny(ctx context.Context, sessionID string) (*contracts.Session, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE id = ?`, sessionID)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: session %q: %w", sessionID, err)
	}
	return sess, nil
}
