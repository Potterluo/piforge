package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// MCP broker tokens: what a sandbox is allowed to hold instead of a credential.
//
// docs/EXECUTION-BOUNDARY.md §4 says the sandbox may hold the per-session model
// gateway token but never a provider key. MCP credentials were the case that
// could not be solved by "just stage the file": `mcp.json` carries decrypted
// env vars for a stdio server, and an HTTP server's token is the user's own.
//
// Stage 3 therefore brokers HTTP MCP servers. The control plane mints a random,
// per-(session, server) token, stores only its hash plus a sealed copy (so a
// re-spawn of the same session can hand the agent the same token instead of a
// second live credential), and writes `<gateway>/gw/mcp/<token>` into the
// sandbox's mcp.json. The proxy (`/gw/mcp/<token>`) is the only place the real
// credential is decrypted, and it is decrypted per request.
//
// Revocation is the point of storing the row rather than signing a claim: a
// token that is not in this table, or whose `revoked_at` is set, is refused by
// the proxy even though nothing about it has expired. A revoked session is
// *not* re-minted on the next spawn — the deliberate consequence is that MCP
// stays off for that session until the user re-enables it by starting a new one.

// MCPToken is one broker credential. Token is only ever populated by
// EnsureMCPToken; lookups by hash return the claims, not the secret.
type MCPToken struct {
	ID        string
	SessionID string
	UserID    string
	ServerID  string
	// Hash is the SHA-256 of the token, hex encoded. It is the lookup key, so
	// the raw token never has to be indexed or compared.
	Hash      string
	CreatedAt time.Time
	RevokedAt time.Time
}

// ErrMCPTokenRevoked reports a token that existed and was revoked. It is
// separate from ErrNotFound so the proxy can say which happened in its log
// without telling the caller anything it could use.
var ErrMCPTokenRevoked = errors.New("mcp token revoked")

// MCPTokenHash is the only transformation applied to a broker token before it is
// stored or looked up.
func MCPTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// EnsureMCPToken returns the live broker token for one (session, server),
// minting it on first use.
//
// It is idempotent on purpose: a session's agent can be restarted (a crash, a
// model switch, a control-plane restart with reattach), and each of those runs
// the launch setup again. Minting a second token every time would leave live
// credentials behind that nothing revokes.
func (s *Store) EnsureMCPToken(ctx context.Context, userID, sessionID, serverID string) (string, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(serverID) == "" {
		return "", fmt.Errorf("store: mcp token: user, session and server are required: %w", ErrInvalid)
	}
	var id string
	var cipher []byte
	var revoked sql.NullString
	err := s.r.QueryRowContext(ctx,
		`SELECT id, token_cipher, revoked_at FROM mcp_tokens
		  WHERE session_id = ? AND server_id = ? AND user_id = ?
		  ORDER BY created_at DESC LIMIT 1`, sessionID, serverID, userID).Scan(&id, &cipher, &revoked)
	switch {
	case err == nil:
		if revoked.Valid && revoked.String != "" {
			return "", fmt.Errorf("store: mcp token for session %s and server %s: %w", sessionID, serverID, ErrMCPTokenRevoked)
		}
		box, berr := secretBoxFor(filepath.Dir(s.path))
		if berr != nil {
			return "", berr
		}
		raw, oerr := box.open(cipher)
		if oerr != nil {
			return "", fmt.Errorf("store: open mcp token: %w", oerr)
		}
		return string(raw), nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through to mint
	default:
		return "", fmt.Errorf("store: read mcp token: %w", err)
	}

	raw := make([]byte, 32)
	if _, rerr := rand.Read(raw); rerr != nil {
		return "", fmt.Errorf("store: generate mcp token: %w", rerr)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	box, berr := secretBoxFor(filepath.Dir(s.path))
	if berr != nil {
		return "", berr
	}
	sealed, serr := box.seal(token)
	if serr != nil {
		return "", fmt.Errorf("store: seal mcp token: %w", serr)
	}
	id = NewID("mcptok")
	if _, ierr := s.w.ExecContext(ctx,
		`INSERT INTO mcp_tokens (id, session_id, user_id, server_id, token_hash, token_cipher, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, sessionID, userID, serverID, MCPTokenHash(token), sealed, formatTime(s.now())); ierr != nil {
		return "", fmt.Errorf("store: save mcp token: %w", ierr)
	}
	return token, nil
}

// MCPTokenByHash resolves a presented token.
//
// A revoked token and an unknown one are different errors to the store but the
// same to the caller: the proxy answers 401 for both and logs which.
func (s *Store) MCPTokenByHash(ctx context.Context, hash string) (*MCPToken, error) {
	var tok MCPToken
	var created string
	var revoked sql.NullString
	err := s.r.QueryRowContext(ctx,
		`SELECT id, session_id, user_id, server_id, token_hash, created_at, revoked_at
		   FROM mcp_tokens WHERE token_hash = ?`, hash).
		Scan(&tok.ID, &tok.SessionID, &tok.UserID, &tok.ServerID, &tok.Hash, &created, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: mcp token: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: mcp token: %w", err)
	}
	tok.CreatedAt = parseTime(created)
	if revoked.Valid && revoked.String != "" {
		tok.RevokedAt = parseTime(revoked.String)
		return &tok, fmt.Errorf("store: mcp token %s: %w", tok.ID, ErrMCPTokenRevoked)
	}
	return &tok, nil
}

// RevokeMCPTokens revokes every broker token of one session and reports how many
// were live.
//
// Revoking is what makes the token a *credential* rather than a copy of one: the
// sandbox may keep the URL, and it stops working the moment this row is stamped.
func (s *Store) RevokeMCPTokens(ctx context.Context, userID, sessionID string) (int, error) {
	res, err := s.w.ExecContext(ctx,
		`UPDATE mcp_tokens SET revoked_at = ?
		  WHERE session_id = ? AND user_id = ? AND (revoked_at IS NULL OR revoked_at = '')`,
		formatTime(s.now()), sessionID, userID)
	if err != nil {
		return 0, fmt.Errorf("store: revoke mcp tokens: %w", err)
	}
	n, rerr := res.RowsAffected()
	if rerr != nil {
		return 0, nil
	}
	return int(n), nil
}

// MCPTokenLiveFor reports how many live broker tokens a session has. The admin
// status call uses it; tests use it to prove that a revoke took effect.
func (s *Store) MCPTokenLiveFor(ctx context.Context, sessionID string) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mcp_tokens
		  WHERE session_id = ? AND (revoked_at IS NULL OR revoked_at = '')`, sessionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count mcp tokens: %w", err)
	}
	return n, nil
}

// MCPServerByID loads one MCP server regardless of who owns it.
//
// The broker needs this: a token names the server, and the definition may be a
// shared (admin-scope) row the session's user can use but not own. It is not an
// HTTP-facing operation, so it does not leak anything: the caller already had to
// present a live token bound to that server.
func (s *Store) MCPServerByID(ctx context.Context, id string) (*MCPTokenServer, error) {
	var (
		name, transport, command, args, cwd, url string
		owner                                    sql.NullString
		enabled                                  int
	)
	err := s.r.QueryRowContext(ctx,
		`SELECT id, COALESCE(owner_id, ''), name, transport, command, args, cwd, url, enabled
		   FROM mcp_servers WHERE id = ?`, id).
		Scan(new(string), &owner, &name, &transport, &command, &args, &cwd, &url, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: mcp server %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: mcp server %q: %w", id, err)
	}
	return &MCPTokenServer{
		ID:        id,
		OwnerID:   owner.String,
		Name:      name,
		Transport: transport,
		Command:   command,
		Args:      args,
		Cwd:       cwd,
		URL:       url,
		Enabled:   enabled == 1,
	}, nil
}

// MCPTokenServer is the definition a broker request needs: where to go and
// whether the user still wants it.
type MCPTokenServer struct {
	ID        string
	OwnerID   string
	Name      string
	Transport string
	Command   string
	Args      string
	Cwd       string
	URL       string
	Enabled   bool
}
