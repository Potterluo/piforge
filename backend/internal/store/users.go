package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// userCols is the projection every user query scans, in this order:
// id, email, display_name, role, status, created_at, deleted_at.
const userCols = `id, email, display_name, role, status, created_at, deleted_at`

// NewUser is the input for CreateUser.
type NewUser struct {
	// Email must be unique, compared case-insensitively.
	Email string
	// DisplayName is what the UI shows; it may be empty.
	DisplayName string
	// PasswordHash is a bcrypt hash produced by internal/auth. Empty means
	// "this account cannot log in with a password" (OIDC-only users).
	PasswordHash string
	// Role defaults to contracts.RoleUser.
	Role contracts.Role
	// Status defaults to "active"; the only other value is "disabled".
	Status string
}

// CreateUser inserts an account and returns it.
func (s *Store) CreateUser(ctx context.Context, in NewUser) (*contracts.User, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		return nil, fmt.Errorf("store: create user: email is required: %w", ErrInvalid)
	}
	role := in.Role
	if role == "" {
		role = contracts.RoleUser
	}
	if role != contracts.RoleUser && role != contracts.RoleAdmin {
		return nil, fmt.Errorf("store: create user: unknown role %q: %w", role, ErrInvalid)
	}
	status := in.Status
	if status == "" {
		status = "active"
	}
	id := NewID("usr")
	now := s.now()

	_, err := s.w.ExecContext(ctx,
		`INSERT INTO users (id, email, display_name, password_hash, role, status, settings, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, '{}', ?)`,
		id, email, in.DisplayName, in.PasswordHash, string(role), status, formatTime(now))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create user %q: %w", email, ErrConflict)
		}
		return nil, fmt.Errorf("store: create user %q: %w", email, err)
	}
	return s.UserByID(ctx, id)
}

// UserByID loads one account by id.
//
// It is the identity lookup used while resolving a session cookie: the id
// comes from the token, so this is a self-read and needs no extra user
// predicate beyond the primary key.
func (s *Store) UserByID(ctx context.Context, userID string) (*contracts.User, error) {
	if userID == "" {
		return nil, fmt.Errorf("store: get user: empty id: %w", ErrNotFound)
	}
	row := s.r.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE id = ? AND deleted_at IS NULL`, userID)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: get user %q: %w", userID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user %q: %w", userID, err)
	}
	return u, nil
}

// UserByEmail loads one account by email and also returns its password hash.
//
// This is a pre-identity lookup used only by the login flow, so it takes no
// caller userID (there is no caller yet). It resolves at most one row through
// the unique email index.
func (s *Store) UserByEmail(ctx context.Context, email string) (*contracts.User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, "", fmt.Errorf("store: get user by email: empty email: %w", ErrInvalid)
	}
	var (
		u       contracts.User
		created string
		deleted sql.NullString
		role    string
		status  string
		hash    string
	)
	err := s.r.QueryRowContext(ctx,
		`SELECT `+userCols+`, password_hash FROM users WHERE email = ? AND deleted_at IS NULL`,
		email).Scan(&u.ID, &u.Email, &u.DisplayName, &role, &status, &created, &deleted, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("store: get user by email %q: %w", email, ErrNotFound)
	}
	if err != nil {
		return nil, "", fmt.Errorf("store: get user by email %q: %w", email, err)
	}
	u.Role = contracts.Role(role)
	u.Status = status
	u.CreatedAt = parseTime(created)
	u.DeletedAt = parseTimePtr(deleted)
	return &u, hash, nil
}

// CountUsers reports how many accounts exist; EnsureAdminSeed uses it to
// decide whether the database is fresh.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// SetPasswordHash replaces one account's password hash.
func (s *Store) SetPasswordHash(ctx context.Context, userID, hash string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ? AND deleted_at IS NULL`, hash, userID)
	if err != nil {
		return fmt.Errorf("store: set password for %q: %w", userID, err)
	}
	return requireOneRow(res, "store: set password for "+userID)
}

// SetUserStatus flips an account between "active" and "disabled".
func (s *Store) SetUserStatus(ctx context.Context, userID, status string) error {
	if status != "active" && status != "disabled" {
		return fmt.Errorf("store: set status: unknown status %q: %w", status, ErrInvalid)
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE users SET status = ? WHERE id = ? AND deleted_at IS NULL`, status, userID)
	if err != nil {
		return fmt.Errorf("store: set status for %q: %w", userID, err)
	}
	return requireOneRow(res, "store: set status for "+userID)
}

// AdminListUsers lists every account.
//
// AdminXxx: it first proves the caller is an active admin and returns
// ErrForbidden otherwise, so it can never be reached through a normal route by
// accident. This is the only cross-user read the product allows, and it
// exposes account metadata only — never conversation content (PRD §3).
func (s *Store) AdminListUsers(ctx context.Context, callerID string) ([]*contracts.User, error) {
	if err := s.requireAdmin(ctx, callerID); err != nil {
		return nil, err
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE deleted_at IS NULL ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: admin list users: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.User, 0, 8)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: admin list users: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: admin list users: %w", err)
	}
	return out, nil
}

func scanUser(sc interface{ Scan(dest ...any) error }) (*contracts.User, error) {
	var (
		u       contracts.User
		role    string
		created string
		deleted sql.NullString
	)
	if err := sc.Scan(&u.ID, &u.Email, &u.DisplayName, &role, &u.Status, &created, &deleted); err != nil {
		return nil, err
	}
	u.Role = contracts.Role(role)
	u.CreatedAt = parseTime(created)
	u.DeletedAt = parseTimePtr(deleted)
	return &u, nil
}

// ---------------------------------------------------------------------------
// Auth sessions (opaque tokens, stored hashed)
// ---------------------------------------------------------------------------

// CreateAuthSession records a new login session. tokenHash is the sha256 of
// the raw cookie value; the raw value is never persisted.
func (s *Store) CreateAuthSession(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	if userID == "" || tokenHash == "" {
		return fmt.Errorf("store: create auth session: empty argument: %w", ErrInvalid)
	}
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, formatTime(s.now()), formatTime(expiresAt))
	if err != nil {
		return fmt.Errorf("store: create auth session: %w", err)
	}
	return nil
}

// AuthSessionUser resolves a token hash to its owner, ignoring sessions that
// expired at or before now. A disabled owner is still returned so that the
// caller can distinguish "disabled" from "no session" and revoke the cookie.
func (s *Store) AuthSessionUser(ctx context.Context, tokenHash string, now time.Time) (*contracts.User, error) {
	if tokenHash == "" {
		return nil, fmt.Errorf("store: auth session: empty token: %w", ErrNotFound)
	}
	row := s.r.QueryRowContext(ctx,
		`SELECT `+prefixed(userCols, "u")+`
		   FROM auth_sessions a
		   JOIN users u ON u.id = a.user_id
		  WHERE a.token_hash = ? AND a.expires_at > ? AND u.deleted_at IS NULL`,
		tokenHash, formatTime(now))
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: auth session: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: auth session: %w", err)
	}
	return u, nil
}

// DeleteAuthSession revokes one token (logout).
func (s *Store) DeleteAuthSession(ctx context.Context, tokenHash string) error {
	if _, err := s.w.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("store: delete auth session: %w", err)
	}
	return nil
}

// DeleteUserAuthSessions revokes every token of one user (password reset,
// account disable).
func (s *Store) DeleteUserAuthSessions(ctx context.Context, userID string) error {
	if _, err := s.w.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: delete auth sessions for %q: %w", userID, err)
	}
	return nil
}

// DeleteExpiredAuthSessions prunes tokens that are past their expiry and
// reports how many rows went away.
func (s *Store) DeleteExpiredAuthSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE expires_at <= ?`, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("store: prune auth sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune auth sessions: %w", err)
	}
	return n, nil
}

// prefixed qualifies a bare column list with a table alias ("id, email" ->
// "u.id, u.email") so joins can reuse the scan helpers.
func prefixed(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// requireOneRow turns "no rows affected" into ErrNotFound.
func requireOneRow(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

// isUniqueViolation detects a SQLite UNIQUE/PRIMARY KEY conflict without
// depending on driver-specific error types.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed: unique")
}
