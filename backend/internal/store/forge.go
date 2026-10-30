package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Forge wiring and pull-request receipts (PRD FR-12.6).
//
// The token is stored as ciphertext produced by the control plane's secret box
// (crypto.Box, the same one that seals provider API keys). The store never sees
// the plaintext and never opens it: only the handler that is about to call the
// forge does, in memory, for the length of one request.

// SetProjectForge replaces one project's forge wiring.
//
// setToken is explicit because "leave the token alone" and "clear the token"
// are different edits: the settings form cannot read a sealed token back, so it
// resubmits the form without one and the common save must not delete what is
// already there. tokenCipher is ignored unless setToken.
func (s *Store) SetProjectForge(ctx context.Context, userID, projectID string, cfg contracts.ForgeConfig, tokenCipher []byte, setToken bool) (*contracts.Project, error) {
	if _, err := s.Project(ctx, userID, projectID); err != nil {
		return nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(cfg.Kind))
	apiBase := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	repo := strings.TrimSpace(cfg.Repo)
	base := strings.TrimSpace(cfg.BaseBranch)

	sets := []string{"forge_kind = ?", "forge_api_base = ?", "forge_repo = ?", "forge_base_branch = ?"}
	args := []any{kind, apiBase, repo, base}
	if setToken {
		sets = append(sets, "forge_token_cipher = ?")
		args = append(args, tokenCipher)
	}
	args = append(args, projectID, userID)
	if _, err := s.w.ExecContext(ctx,
		`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id = ? AND user_id = ?`, args...); err != nil {
		return nil, fmt.Errorf("store: set project forge %q: %w", projectID, err)
	}
	return s.Project(ctx, userID, projectID)
}

// ProjectForgeTokenCipher returns the sealed token for one project. An empty
// result means no token was ever stored (or it was cleared); the caller refuses
// the request with an explanation instead of calling the forge anonymously.
func (s *Store) ProjectForgeTokenCipher(ctx context.Context, userID, projectID string) ([]byte, error) {
	var blob []byte
	err := s.r.QueryRowContext(ctx,
		`SELECT forge_token_cipher FROM projects WHERE id = ? AND user_id = ?`,
		projectID, userID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: project %q: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read forge token for %q: %w", projectID, err)
	}
	return blob, nil
}

// SavePullRequest records the PR a session created. It is an upsert keyed by
// (session, branch): re-running the flow on the same branch is the same
// receipt, not a second history row.
func (s *Store) SavePullRequest(ctx context.Context, userID, sessionID, projectID, branch, baseBranch, kind string, pr *contracts.PullRequest) error {
	if pr == nil || strings.TrimSpace(pr.URL) == "" {
		return fmt.Errorf("store: save pull request: no URL: %w", ErrInvalid)
	}
	now := formatTime(s.now())
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO pull_requests
		     (id, user_id, project_id, session_id, branch, base_branch, number, url, title, forge_kind, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (session_id, branch) DO UPDATE SET
		     base_branch = excluded.base_branch,
		     number = excluded.number,
		     url = excluded.url,
		     title = excluded.title,
		     forge_kind = excluded.forge_kind,
		     updated_at = excluded.updated_at`,
		NewID("pr"), userID, projectID, sessionID, branch, baseBranch,
		pr.Number, pr.URL, pr.Title, kind, now, now)
	if err != nil {
		return fmt.Errorf("store: save pull request for session %q: %w", sessionID, err)
	}
	return nil
}

// PullRequestForBranch is the last PR recorded for one session and branch.
// No row means "this session has not opened one", not "the forge has none",
// which is why the create flow asks the forge as well.
func (s *Store) PullRequestForBranch(ctx context.Context, userID, sessionID, branch string) (*contracts.PullRequest, error) {
	var pr contracts.PullRequest
	var kind, base string
	err := s.r.QueryRowContext(ctx,
		`SELECT number, url, title, base_branch, forge_kind
		   FROM pull_requests WHERE user_id = ? AND session_id = ? AND branch = ?`,
		userID, sessionID, branch).Scan(&pr.Number, &pr.URL, &pr.Title, &base, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read pull request for session %q: %w", sessionID, err)
	}
	pr.Head = branch
	pr.Base = base
	return &pr, nil
}
