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

// projectCols is the base projection: id, user_id, name, repo_url,
// default_branch, created_at.
const projectCols = `p.id, p.user_id, p.name, p.repo_url, p.default_branch, p.created_at`

// NewProject is the input for CreateProject.
type NewProject struct {
	// Name is unique per user.
	Name string
	// RepoURL may be empty, which means a blank workspace (PRD FR-1.1).
	RepoURL string
	// DefaultBranch defaults to "main".
	DefaultBranch string
}

// CreateProject creates a project owned by userID.
func (s *Store) CreateProject(ctx context.Context, userID string, in NewProject) (*contracts.Project, error) {
	if userID == "" {
		return nil, fmt.Errorf("store: create project: empty user: %w", ErrInvalid)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("store: create project: name is required: %w", ErrInvalid)
	}
	branch := in.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	id := NewID("prj")
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO projects (id, user_id, name, repo_url, default_branch, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, name, in.RepoURL, branch, formatTime(s.now())); err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create project %q: %w", name, ErrConflict)
		}
		return nil, fmt.Errorf("store: create project %q: %w", name, err)
	}
	return s.Project(ctx, userID, id)
}

// ListProjects lists the caller's projects newest first, with the runtime
// decoration the list view needs (sandbox state, session count, last active).
func (s *Store) ListProjects(ctx context.Context, userID string) ([]*contracts.Project, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+projectCols+`,
		        COALESCE((SELECT sb.state FROM sandboxes sb
		                   WHERE sb.project_id = p.id AND sb.user_id = p.user_id
		                   ORDER BY sb.created_at DESC LIMIT 1), '') AS sandbox_state,
		        (SELECT COUNT(*) FROM sessions ss
		          WHERE ss.project_id = p.id AND ss.user_id = p.user_id AND ss.ephemeral = 0) AS session_count,
		        (SELECT MAX(ss.last_active_at) FROM sessions ss
		          WHERE ss.project_id = p.id AND ss.user_id = p.user_id) AS last_active
		   FROM projects p
		  WHERE p.user_id = ?
		  ORDER BY p.created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.Project, 0, 8)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list projects: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	return out, nil
}

// Project loads one project, scoped to its owner. A project owned by somebody
// else is reported as ErrNotFound on purpose: the HTTP layer turns that into
// 404 so a caller cannot probe which ids exist (PRD §9.7).
func (s *Store) Project(ctx context.Context, userID, projectID string) (*contracts.Project, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+projectCols+`,
		        COALESCE((SELECT sb.state FROM sandboxes sb
		                   WHERE sb.project_id = p.id AND sb.user_id = p.user_id
		                   ORDER BY sb.created_at DESC LIMIT 1), '') AS sandbox_state,
		        (SELECT COUNT(*) FROM sessions ss
		          WHERE ss.project_id = p.id AND ss.user_id = p.user_id AND ss.ephemeral = 0) AS session_count,
		        (SELECT MAX(ss.last_active_at) FROM sessions ss
		          WHERE ss.project_id = p.id AND ss.user_id = p.user_id) AS last_active
		   FROM projects p
		  WHERE p.id = ? AND p.user_id = ?`, projectID, userID)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: project %q: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: project %q: %w", projectID, err)
	}
	return p, nil
}

func scanProject(sc interface{ Scan(dest ...any) error }) (*contracts.Project, error) {
	var (
		p          contracts.Project
		state      string
		created    string
		lastActive sql.NullString
	)
	if err := sc.Scan(&p.ID, &p.UserID, &p.Name, &p.RepoURL, &p.DefaultBranch,
		&created, &state, &p.SessionCount, &lastActive); err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(created)
	p.SandboxState = contracts.SandboxState(state)
	p.LastActive = parseTimePtr(lastActive)
	return &p, nil
}

// ---------------------------------------------------------------------------
// Sandboxes
// ---------------------------------------------------------------------------

// sandboxCols is: id, user_id, project_id, state, provider, node, image,
// root_dir, message, last_active_at, created_at, expires_at.
const sandboxCols = `id, user_id, project_id, state, provider, node, image, root_dir, message, last_active_at, created_at, expires_at`

// NewSandbox is the input for CreateSandbox. The sandbox manager
// (internal/sandbox) owns the lifecycle; the store only records it.
type NewSandbox struct {
	ProjectID string
	State     contracts.SandboxState
	Provider  string
	Node      string
	Image     string
	RootDir   string
	Message   string
	CPUMilli  int
	MemMB     int
	DiskGB    int
	ExpiresAt *time.Time
}

// CreateSandbox records a sandbox for one of the caller's projects. There is
// at most one sandbox per (project, user) (PRD §8.2: UNIQUE(project_id)).
func (s *Store) CreateSandbox(ctx context.Context, userID string, in NewSandbox) (*contracts.Sandbox, error) {
	if _, err := s.Project(ctx, userID, in.ProjectID); err != nil {
		return nil, err
	}
	state := in.State
	if state == "" {
		state = contracts.SandboxCreating
	}
	id := NewID("sbx")
	now := s.now()
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO sandboxes (id, user_id, project_id, state, provider, node, image, root_dir,
		                        message, cpu_milli, mem_mb, disk_gb, last_active_at, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, in.ProjectID, string(state), in.Provider, in.Node, in.Image, in.RootDir,
		in.Message, in.CPUMilli, in.MemMB, in.DiskGB, formatTime(now), formatTime(now), nullTime(in.ExpiresAt)); err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create sandbox for project %q: %w", in.ProjectID, ErrConflict)
		}
		return nil, fmt.Errorf("store: create sandbox for project %q: %w", in.ProjectID, err)
	}
	return s.Sandbox(ctx, userID, id)
}

// Sandbox loads one sandbox owned by the caller.
func (s *Store) Sandbox(ctx context.Context, userID, sandboxID string) (*contracts.Sandbox, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+sandboxCols+` FROM sandboxes WHERE id = ? AND user_id = ?`, sandboxID, userID)
	sb, err := scanSandbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: sandbox %q: %w", sandboxID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: sandbox %q: %w", sandboxID, err)
	}
	return sb, nil
}

// SandboxForProject loads the caller's sandbox for a project.
func (s *Store) SandboxForProject(ctx context.Context, userID, projectID string) (*contracts.Sandbox, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+sandboxCols+` FROM sandboxes WHERE project_id = ? AND user_id = ?`, projectID, userID)
	sb, err := scanSandbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: sandbox for project %q: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: sandbox for project %q: %w", projectID, err)
	}
	return sb, nil
}

// UpdateSandboxState records a state-machine transition (PRD FR-5.1) and
// refreshes last_active_at.
func (s *Store) UpdateSandboxState(ctx context.Context, userID, sandboxID string, state contracts.SandboxState, message string) error {
	if state == "" {
		return fmt.Errorf("store: update sandbox %q: empty state: %w", sandboxID, ErrInvalid)
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE sandboxes SET state = ?, message = ?, last_active_at = ?
		  WHERE id = ? AND user_id = ?`,
		string(state), message, formatTime(s.now()), sandboxID, userID)
	if err != nil {
		return fmt.Errorf("store: update sandbox %q: %w", sandboxID, err)
	}
	return requireOneRow(res, "store: update sandbox "+sandboxID)
}

func scanSandbox(sc interface{ Scan(dest ...any) error }) (*contracts.Sandbox, error) {
	var (
		sb      contracts.Sandbox
		state   string
		lastAct string
		created string
		expires sql.NullString
	)
	if err := sc.Scan(&sb.ID, &sb.UserID, &sb.ProjectID, &state, &sb.Provider, &sb.Node,
		&sb.Image, &sb.RootDir, &sb.Message, &lastAct, &created, &expires); err != nil {
		return nil, err
	}
	sb.State = contracts.SandboxState(state)
	sb.LastActiveAt = parseTime(lastAct)
	sb.CreatedAt = parseTime(created)
	sb.ExpiresAt = parseTimePtr(expires)
	return &sb, nil
}

// ---------------------------------------------------------------------------
// Worktrees
// ---------------------------------------------------------------------------

// worktreeCols is: id, sandbox_id, user_id, path, branch, base_commit,
// created_at.
const worktreeCols = `id, sandbox_id, user_id, path, branch, base_commit, created_at`

// NewWorktree is the input for CreateWorktree.
type NewWorktree struct {
	SandboxID  string
	Path       string
	Branch     string
	BaseCommit string
}

// CreateWorktree records a git worktree inside one of the caller's sandboxes.
func (s *Store) CreateWorktree(ctx context.Context, userID string, in NewWorktree) (*contracts.Worktree, error) {
	if _, err := s.Sandbox(ctx, userID, in.SandboxID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Path) == "" || strings.TrimSpace(in.Branch) == "" {
		return nil, fmt.Errorf("store: create worktree: path and branch are required: %w", ErrInvalid)
	}
	id := NewID("wt")
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO worktrees (id, sandbox_id, user_id, path, branch, base_commit, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.SandboxID, userID, in.Path, in.Branch, in.BaseCommit, formatTime(s.now())); err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create worktree %q: %w", in.Path, ErrConflict)
		}
		return nil, fmt.Errorf("store: create worktree %q: %w", in.Path, err)
	}
	return s.Worktree(ctx, userID, id)
}

// Worktree loads one worktree owned by the caller.
// WorktreePathByID resolves a worktree's on-disk path without a user check.
//
// It exists for the terminal relay, which receives a worktree id that a session
// already scoped to the caller, and which runs after that check. Prefer
// Worktree() anywhere the caller's identity is not already established.
func (s *Store) WorktreePathByID(ctx context.Context, worktreeID string) (string, error) {
	var path string
	err := s.r.QueryRowContext(ctx,
		`SELECT path FROM worktrees WHERE id = ?`, worktreeID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: worktree path %q: %w", worktreeID, err)
	}
	return path, nil
}

func (s *Store) Worktree(ctx context.Context, userID, worktreeID string) (*contracts.Worktree, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+worktreeCols+` FROM worktrees WHERE id = ? AND user_id = ?`, worktreeID, userID)
	wt, err := scanWorktree(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: worktree %q: %w", worktreeID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: worktree %q: %w", worktreeID, err)
	}
	return wt, nil
}

// ListWorktrees lists the caller's worktrees, optionally for one sandbox.
func (s *Store) ListWorktrees(ctx context.Context, userID, sandboxID string) ([]*contracts.Worktree, error) {
	q := `SELECT ` + worktreeCols + ` FROM worktrees WHERE user_id = ?`
	args := []any{userID}
	if sandboxID != "" {
		q += ` AND sandbox_id = ?`
		args = append(args, sandboxID)
	}
	q += ` ORDER BY created_at ASC`

	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list worktrees: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.Worktree, 0, 4)
	for rows.Next() {
		wt, err := scanWorktree(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list worktrees: %w", err)
		}
		out = append(out, wt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list worktrees: %w", err)
	}
	return out, nil
}

func scanWorktree(sc interface{ Scan(dest ...any) error }) (*contracts.Worktree, error) {
	var (
		wt      contracts.Worktree
		created string
	)
	if err := sc.Scan(&wt.ID, &wt.SandboxID, &wt.UserID, &wt.Path, &wt.Branch, &wt.BaseCommit, &created); err != nil {
		return nil, err
	}
	wt.CreatedAt = parseTime(created)
	return &wt, nil
}
