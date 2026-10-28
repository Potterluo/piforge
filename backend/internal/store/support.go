package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SupportAccess is one admin read of somebody else's session (PRD FR-16.8).
//
// It is a receipt, not an audit system: the owner is told who looked, when and
// why, and the grant expires. Anything more would be a promise this product does
// not keep at scale, and anything less makes "the admin cannot read your
// conversations" a lie.
type SupportAccess struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	OwnerID   string     `json:"owner_id"`
	AdminID   string     `json:"admin_id"`
	Reason    string     `json:"reason"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	SeenAt    *time.Time `json:"seen_at,omitempty"`

	// Denormalised for the owner's notice list.
	SessionTitle string `json:"session_title,omitempty"`
	AdminEmail   string `json:"admin_email,omitempty"`
}

// supportAccessTTL bounds how long one grant is good for. Long enough to debug a
// stuck run, short enough that "I looked once" does not become standing access.
const supportAccessTTL = 30 * time.Minute

// CreateSupportAccess records an admin's intent to read a session and returns the
// grant. The owner is notified by the row existing, which is why it is written
// before any transcript is read.
func (s *Store) CreateSupportAccess(ctx context.Context, adminID, sessionID, reason string) (*SupportAccess, error) {
	if err := s.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}

	var ownerID, title string
	err := s.r.QueryRowContext(ctx,
		`SELECT user_id, title FROM sessions WHERE id = ?`, sessionID).Scan(&ownerID, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: support access: %w", err)
	}
	if ownerID == adminID {
		return nil, fmt.Errorf("store: support access: that session is your own: %w", ErrInvalid)
	}

	now := s.now()
	access := &SupportAccess{
		ID:           NewID("sup"),
		SessionID:    sessionID,
		OwnerID:      ownerID,
		AdminID:      adminID,
		Reason:       reason,
		CreatedAt:    now,
		ExpiresAt:    now.Add(supportAccessTTL),
		SessionTitle: title,
	}
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO support_access (id, session_id, owner_id, admin_id, reason, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		access.ID, access.SessionID, access.OwnerID, access.AdminID, access.Reason,
		formatTime(access.CreatedAt), formatTime(access.ExpiresAt)); err != nil {
		return nil, fmt.Errorf("store: create support access: %w", err)
	}
	return access, nil
}

// SupportAccessGrant returns an unexpired grant for this admin and session.
func (s *Store) SupportAccessGrant(ctx context.Context, adminID, sessionID string) (*SupportAccess, error) {
	if err := s.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	row := s.r.QueryRowContext(ctx,
		`SELECT id, session_id, owner_id, admin_id, reason, created_at, expires_at, seen_at
		   FROM support_access
		  WHERE admin_id = ? AND session_id = ? AND expires_at > ?
		  ORDER BY created_at DESC LIMIT 1`,
		adminID, sessionID, formatTime(s.now()))
	access, err := scanSupportAccess(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: support access grant: %w", err)
	}
	return access, nil
}

// SupportNotices lists the receipts owed to one user, newest first.
func (s *Store) SupportNotices(ctx context.Context, userID string) ([]*SupportAccess, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT a.id, a.session_id, a.owner_id, a.admin_id, a.reason, a.created_at, a.expires_at,
		        a.seen_at, COALESCE(s.title, ''), COALESCE(u.email, '')
		   FROM support_access a
		   LEFT JOIN sessions s ON s.id = a.session_id
		   LEFT JOIN users u ON u.id = a.admin_id
		  WHERE a.owner_id = ?
		  ORDER BY a.created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: support notices: %w", err)
	}
	defer rows.Close()

	out := make([]*SupportAccess, 0, 8)
	for rows.Next() {
		var access SupportAccess
		var created, expires string
		var seen sql.NullString
		if err := rows.Scan(&access.ID, &access.SessionID, &access.OwnerID, &access.AdminID,
			&access.Reason, &created, &expires, &seen, &access.SessionTitle, &access.AdminEmail); err != nil {
			return nil, fmt.Errorf("store: scan support notice: %w", err)
		}
		access.CreatedAt = parseTime(created)
		access.ExpiresAt = parseTime(expires)
		access.SeenAt = parseTimePtr(seen)
		out = append(out, &access)
	}
	return out, rows.Err()
}

// MarkSupportNoticeSeen records that the owner has read one receipt.
func (s *Store) MarkSupportNoticeSeen(ctx context.Context, userID, noticeID string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE support_access SET seen_at = ? WHERE id = ? AND owner_id = ? AND seen_at IS NULL`,
		formatTime(s.now()), noticeID, userID)
	if err != nil {
		return fmt.Errorf("store: mark support notice seen: %w", err)
	}
	_, _ = res.RowsAffected()
	return nil
}

func scanSupportAccess(row *sql.Row) (*SupportAccess, error) {
	var access SupportAccess
	var created, expires string
	var seen sql.NullString
	if err := row.Scan(&access.ID, &access.SessionID, &access.OwnerID, &access.AdminID,
		&access.Reason, &created, &expires, &seen); err != nil {
		return nil, err
	}
	access.CreatedAt = parseTime(created)
	access.ExpiresAt = parseTime(expires)
	access.SeenAt = parseTimePtr(seen)
	return &access, nil
}
