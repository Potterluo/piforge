package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// NewAPIKey is the input for CreateAPIKey. The caller mints the token and keeps
// only its hash: the plaintext is shown once, at creation, and never again.
type NewAPIKey struct {
	Name      string
	Prefix    string
	TokenHash string
}

const apiKeyCols = `id, user_id, name, prefix, created_at, last_used_at, revoked_at`

// CreateAPIKey stores one token hash for a user.
func (s *Store) CreateAPIKey(ctx context.Context, userID string, in NewAPIKey) (*contracts.APIKey, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("store: api key name is required: %w", ErrInvalid)
	}
	if in.TokenHash == "" || in.Prefix == "" {
		return nil, fmt.Errorf("store: api key needs a prefix and a hash: %w", ErrInvalid)
	}
	id := NewID("key")
	now := s.now()
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO api_keys (id, user_id, name, prefix, token_hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, name, in.Prefix, in.TokenHash, formatTime(now)); err != nil {
		return nil, fmt.Errorf("store: create api key: %w", err)
	}
	return s.APIKey(ctx, userID, id)
}

// APIKey loads one key owned by the caller.
func (s *Store) APIKey(ctx context.Context, userID, keyID string) (*contracts.APIKey, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE id = ? AND user_id = ?`, keyID, userID)
	key, err := scanAPIKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: api key %q: %w", keyID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return key, nil
}

// ListAPIKeys returns the caller's keys, newest first, revoked ones included so
// the history is visible.
func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]*contracts.APIKey, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	defer rows.Close()
	out := make([]*contracts.APIKey, 0, 8)
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// RevokeAPIKey marks a key unusable. Deleting it would lose the audit trail of a
// token that may have leaked.
func (s *Store) RevokeAPIKey(ctx context.Context, userID, keyID string) (*contracts.APIKey, error) {
	if _, err := s.APIKey(ctx, userID, keyID); err != nil {
		return nil, err
	}
	if _, err := s.w.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		formatTime(s.now()), keyID, userID); err != nil {
		return nil, fmt.Errorf("store: revoke api key: %w", err)
	}
	return s.APIKey(ctx, userID, keyID)
}

// APIKeyUser resolves a presented token hash to its owner, and records the use.
//
// Revoked keys resolve to ErrNotFound rather than a distinct error: a caller with
// either a wrong token or a revoked one must not learn which it is.
func (s *Store) APIKeyUser(ctx context.Context, tokenHash string) (*contracts.User, error) {
	var userID string
	var revoked sql.NullString
	err := s.r.QueryRowContext(ctx,
		`SELECT user_id, revoked_at FROM api_keys WHERE token_hash = ?`, tokenHash).Scan(&userID, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: api key: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: api key: %w", err)
	}
	if revoked.Valid {
		return nil, fmt.Errorf("store: api key revoked: %w", ErrNotFound)
	}
	user, err := s.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Best effort: a failed timestamp must not fail the request it measured.
	_, _ = s.w.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE token_hash = ?`, formatTime(s.now()), tokenHash)
	return user, nil
}

func scanAPIKey(sc interface{ Scan(dest ...any) error }) (*contracts.APIKey, error) {
	var (
		key       contracts.APIKey
		created   string
		lastUsed  sql.NullString
		revokedAt sql.NullString
	)
	if err := sc.Scan(&key.ID, &key.UserID, &key.Name, &key.Prefix, &created, &lastUsed, &revokedAt); err != nil {
		return nil, err
	}
	key.CreatedAt = parseTime(created)
	if lastUsed.Valid {
		key.LastUsedAt = parseTime(lastUsed.String)
	}
	key.Revoked = revokedAt.Valid
	if revokedAt.Valid {
		key.RevokedAt = parseTime(revokedAt.String)
	}
	return &key, nil
}
