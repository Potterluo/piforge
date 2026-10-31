package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// projectCols is the base projection: id, user_id, name, repo_url,
// default_branch, created_at, plus the non-secret forge wiring and whether a
// token is sealed (FR-12.6). The cipher itself is never part of a projection.
const projectCols = `p.id, p.user_id, p.name, p.repo_url, p.default_branch, p.created_at,
	p.forge_kind, p.forge_api_base, p.forge_repo, p.forge_base_branch,
	(p.forge_token_cipher IS NOT NULL AND length(p.forge_token_cipher) > 0)`

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

// ProjectNameTaken reports whether the caller already has a project with this
// name, compared case-insensitively.
//
// Case matters to a person reading a list and not to a router choosing what to
// create, so "Frontend" and "frontend" are the same name here. A deleted project
// is not a state this has to filter: DELETE removes the row and its children.
func (s *Store) ProjectNameTaken(ctx context.Context, userID, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if userID == "" || name == "" {
		return false, nil
	}
	var one int
	err := s.r.QueryRowContext(ctx,
		`SELECT 1 FROM projects WHERE user_id = ? AND name COLLATE NOCASE = ? LIMIT 1`,
		userID, name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: project name %q: %w", name, err)
	}
	return true, nil
}

// RunningSessionCount counts the caller's sessions in a project that are running
// a turn right now.
//
// The delete flow asks before it tears anything down: deleting the row under a
// live turn is not a cleanup, it is killing work the user is watching.
func (s *Store) RunningSessionCount(ctx context.Context, userID, projectID string) (int, error) {
	var count int
	err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions
		  WHERE user_id = ? AND project_id = ? AND status = ?`,
		userID, projectID, string(contracts.StatusRunning)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count running sessions in project %q: %w", projectID, err)
	}
	return count, nil
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

// ProjectIndex maps every project id to its owner. It exists for the janitor,
// which decides whether a sandbox directory on disk still has a project: one
// query for a whole sweep instead of one per directory, and no way to forget the
// ownership half (a directory under the wrong user is stale too).
func (s *Store) ProjectIndex(ctx context.Context) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, user_id FROM projects`)
	if err != nil {
		return nil, fmt.Errorf("store: project index: %w", err)
	}
	defer rows.Close()

	index := make(map[string]string, 64)
	for rows.Next() {
		var id, userID string
		if err := rows.Scan(&id, &userID); err != nil {
			return nil, fmt.Errorf("store: project index: %w", err)
		}
		index[id] = userID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: project index: %w", err)
	}
	return index, nil
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

// ProjectPatch is the input for UpdateProject. Nil fields are left alone; an
// explicit empty repo URL is a valid edit (it turns the project into a blank
// workspace), which is why this is pointers rather than zero-value checks.
type ProjectPatch struct {
	Name          *string
	RepoURL       *string
	DefaultBranch *string
}

// UpdateProject edits one project owned by userID.
//
// It exists for the recovery path: a session whose provisioning failed has no
// worktree, and the only useful action there is "the repository address was
// wrong, let me fix it". Ownership is proved first so a stranger gets the same
// not_found as a missing id.
func (s *Store) UpdateProject(ctx context.Context, userID, projectID string, patch ProjectPatch) (*contracts.Project, error) {
	if _, err := s.Project(ctx, userID, projectID); err != nil {
		return nil, err
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return nil, fmt.Errorf("store: update project %q: name is required: %w", projectID, ErrInvalid)
		}
		sets = append(sets, "name = ?")
		args = append(args, name)
	}
	if patch.RepoURL != nil {
		sets = append(sets, "repo_url = ?")
		args = append(args, strings.TrimSpace(*patch.RepoURL))
	}
	if patch.DefaultBranch != nil {
		branch := strings.TrimSpace(*patch.DefaultBranch)
		if branch == "" {
			branch = "main"
		}
		sets = append(sets, "default_branch = ?")
		args = append(args, branch)
	}
	if len(sets) == 0 {
		return s.Project(ctx, userID, projectID)
	}

	args = append(args, projectID, userID)
	if _, err := s.w.ExecContext(ctx,
		`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id = ? AND user_id = ?`, args...); err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: update project %q: %w", projectID, ErrConflict)
		}
		return nil, fmt.Errorf("store: update project %q: %w", projectID, err)
	}
	return s.Project(ctx, userID, projectID)
}

// DeleteProject removes one project owned by userID.
//
// Foreign keys cascade to sandboxes, worktrees, sessions, runs and events, so a
// deleted project cannot leave rows pointing at a repository that no longer
// exists. The container behind a sandbox is not a row: the caller destroys it
// first (best effort) and this method only removes the record.
func (s *Store) DeleteProject(ctx context.Context, userID, projectID string) error {
	if _, err := s.Project(ctx, userID, projectID); err != nil {
		return err
	}
	res, err := s.w.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND user_id = ?`, projectID, userID)
	if err != nil {
		return fmt.Errorf("store: delete project %q: %w", projectID, err)
	}
	return requireOneRow(res, "store: delete project "+projectID)
}

func scanProject(sc interface{ Scan(dest ...any) error }) (*contracts.Project, error) {
	var (
		p          contracts.Project
		state      string
		created    string
		lastActive sql.NullString
		forgeKind  string
		forgeAPI   string
		forgeRepo  string
		forgeBase  string
		hasToken   bool
	)
	if err := sc.Scan(&p.ID, &p.UserID, &p.Name, &p.RepoURL, &p.DefaultBranch,
		&created, &forgeKind, &forgeAPI, &forgeRepo, &forgeBase, &hasToken,
		&state, &p.SessionCount, &lastActive); err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(created)
	p.SandboxState = contracts.SandboxState(state)
	p.LastActive = parseTimePtr(lastActive)
	if forgeKind != "" || forgeAPI != "" || forgeRepo != "" || hasToken {
		p.Forge = &contracts.ForgeConfig{
			Kind: forgeKind, APIBase: forgeAPI, Repo: forgeRepo,
			BaseBranch: forgeBase, HasToken: hasToken,
		}
	}
	return &p, nil
}

// ---------------------------------------------------------------------------
// Sandboxes
// ---------------------------------------------------------------------------

// sandboxCols is: id, user_id, project_id, state, provider, node, image,
// root_dir, workspace, message, last_active_at, created_at, expires_at.
const sandboxCols = `id, user_id, project_id, state, provider, node, image, root_dir, workspace, message, last_active_at, created_at, expires_at`

// NewSandbox is the input for CreateSandbox. The sandbox manager
// (internal/sandbox) owns the lifecycle; the store only records it.
type NewSandbox struct {
	// ID is the provider's own handle for the sandbox. Empty means "invent one".
	// A provider that has to find the same container again after a request (docker
	// names it from the pair, and its lifecycle calls take that name) reports the
	// id it uses, so the row has to keep it instead of inventing an unrelated one.
	ID        string
	ProjectID string
	State     contracts.SandboxState
	Provider  string
	Node      string
	Image     string
	RootDir   string
	Message   string
	// Workspace is how this process reaches the sandbox's files
	// (contracts.WorkspaceMode). Empty means "host": a directory it can open.
	Workspace contracts.WorkspaceMode
	// APIKey is the key the sandbox's own API requires (per-sandbox, generated
	// where the container was created). It is sealed before it is written: the
	// sandboxes table is a file on disk like any other.
	APIKey    string
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
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = NewID("sbx")
	}
	now := s.now()
	keyCipher, err := s.sealSandboxAPIKey(in.APIKey)
	if err != nil {
		return nil, err
	}
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO sandboxes (id, user_id, project_id, state, provider, node, image, root_dir, workspace,
		                        message, cpu_milli, mem_mb, disk_gb, last_active_at, created_at, expires_at,
		                        api_key_cipher)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, in.ProjectID, string(state), in.Provider, in.Node, in.Image, in.RootDir,
		string(in.Workspace.Normalized()),
		in.Message, in.CPUMilli, in.MemMB, in.DiskGB, formatTime(now), formatTime(now), nullTime(in.ExpiresAt),
		keyCipher); err != nil {
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

// SandboxAPIKey returns the key one sandbox's HTTP API requires, decrypted.
//
// It is deliberately not part of contracts.Sandbox and not reachable from the HTTP
// layer: the only callers are the control plane's own sandbox clients (files,
// terminal, browser, worktree), which hold a sandbox id and nothing else. An empty
// answer means the row predates per-sandbox keys; the caller decides between the
// fixed override and a clear failure — never "call it unauthenticated".
func (s *Store) SandboxAPIKey(ctx context.Context, sandboxID string) (string, error) {
	var blob []byte
	err := s.r.QueryRowContext(ctx,
		`SELECT api_key_cipher FROM sandboxes WHERE id = ?`, sandboxID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: sandbox %q: %w", sandboxID, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: sandbox %q: read api key: %w", sandboxID, err)
	}
	return s.openSandboxAPIKey(blob)
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

// SessionSandbox is where one session's workspace lives: the sandbox that holds
// its worktree (or, for a session without one, the sandbox of its project) and
// that sandbox's host directory.
//
// It exists for the liveness probe (internal/liveness): a container-backed probe
// gets a session id from reconciliation and has to answer two questions the
// session row does not carry — which sandbox to ask, and where its workspace is
// on the host (the agent's launcher writes the heartbeat there). Both come from
// one query, which also means the answer cannot disagree with itself.
type SessionSandbox struct {
	SandboxID string
	// RootDir is the host directory of the sandbox, empty when the row does not
	// know it (an older record).
	RootDir string
	// Provider is the sandbox row's provider ("local", "docker", "runner").
	Provider string
}

// SessionSandboxFor resolves a session to its sandbox.
//
// A sidecar session shares its main session's worktree, and an ephemeral session
// may have none at all, so the lookup prefers the worktree's sandbox and falls
// back to the project's. It is a service-level query like RunningSessions: the
// caller already has the session id and nothing else, and it deliberately takes
// no user predicate.
func (s *Store) SessionSandboxFor(ctx context.Context, sessionID string) (SessionSandbox, error) {
	if strings.TrimSpace(sessionID) == "" {
		return SessionSandbox{}, fmt.Errorf("store: session sandbox: a session id is required: %w", ErrInvalid)
	}
	var (
		id, rootDir, provider sql.NullString
	)
	err := s.r.QueryRowContext(ctx,
		`SELECT COALESCE(w.sandbox_id, p.id),
		        COALESCE(wsb.root_dir, p.root_dir),
		        COALESCE(wsb.provider, p.provider)
		   FROM sessions s
		   LEFT JOIN worktrees w ON w.id = s.worktree_id
		   LEFT JOIN sandboxes wsb ON wsb.id = w.sandbox_id
		   LEFT JOIN sandboxes p ON p.project_id = s.project_id AND p.user_id = s.user_id
		  WHERE s.id = ?`, sessionID).Scan(&id, &rootDir, &provider)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionSandbox{}, fmt.Errorf("store: session sandbox for %q: %w", sessionID, ErrNotFound)
	}
	if err != nil {
		return SessionSandbox{}, fmt.Errorf("store: session sandbox for %q: %w", sessionID, err)
	}
	if !id.Valid || strings.TrimSpace(id.String) == "" {
		return SessionSandbox{}, fmt.Errorf("store: session %q has no sandbox: %w", sessionID, ErrNotFound)
	}
	return SessionSandbox{SandboxID: id.String, RootDir: rootDir.String, Provider: provider.String}, nil
}

// SandboxIDs lists every sandbox id in the deployment. The janitor uses it to
// decide whether a directory under <data>/snapshots still has an owner; one
// query per pass, not one per directory.
func (s *Store) SandboxIDs(ctx context.Context) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id FROM sandboxes`)
	if err != nil {
		return nil, fmt.Errorf("store: sandbox index: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: sandbox index: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sandbox index: %w", err)
	}
	return ids, nil
}

// SandboxDirectory is one sandbox's host checkout. It is what the disk-quota
// report in GET /v1/admin/status measures, and it exists as a row type because
// the admin screen needs the owner and the project next to the bytes: a number
// without "whose sandbox is close to its budget" is not actionable.
type SandboxDirectory struct {
	ID        string
	UserID    string
	ProjectID string
	RootDir   string
}

// SandboxDirectories lists every sandbox's root once per admin status read. One
// query, not one per sandbox.
func (s *Store) SandboxDirectories(ctx context.Context) ([]SandboxDirectory, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, user_id, project_id, root_dir FROM sandboxes ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: sandbox directories: %w", err)
	}
	defer rows.Close()
	out := make([]SandboxDirectory, 0, 8)
	for rows.Next() {
		var row SandboxDirectory
		if err := rows.Scan(&row.ID, &row.UserID, &row.ProjectID, &row.RootDir); err != nil {
			return nil, fmt.Errorf("store: sandbox directories: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sandbox directories: %w", err)
	}
	return out, nil
}

// SandboxIDsForProject lists the sandboxes a project owns. A project normally
// has one, but the rows are the source of truth and a delete has to reclaim
// every snapshot directory they name.
func (s *Store) SandboxIDsForProject(ctx context.Context, userID, projectID string) ([]string, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id FROM sandboxes WHERE project_id = ? AND user_id = ?`, projectID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: project sandbox index: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, 2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: project sandbox index: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: project sandbox index: %w", err)
	}
	return ids, nil
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

// UpdateSandboxStateIdle records a transition made by the idle reaper.
//
// It differs from UpdateSandboxState in exactly one way: it does not refresh
// last_active_at. The reaper's write is not user activity, and moving the clock
// there would restart the idle countdown every time the reaper looked at a row —
// a PAUSED sandbox would then never reach the reclaim threshold.
func (s *Store) UpdateSandboxStateIdle(ctx context.Context, userID, sandboxID string, state contracts.SandboxState, message string) error {
	if state == "" {
		return fmt.Errorf("store: update sandbox %q: empty state: %w", sandboxID, ErrInvalid)
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE sandboxes SET state = ?, message = ?
		  WHERE id = ? AND user_id = ?`,
		string(state), message, sandboxID, userID)
	if err != nil {
		return fmt.Errorf("store: update sandbox %q: %w", sandboxID, err)
	}
	return requireOneRow(res, "store: update sandbox "+sandboxID)
}

// TouchSandbox records real activity on a sandbox. Only user-driven paths call
// it; the reaper's own state writes deliberately do not.
func (s *Store) TouchSandbox(ctx context.Context, userID, sandboxID string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE sandboxes SET last_active_at = ? WHERE id = ? AND user_id = ?`,
		formatTime(s.now()), sandboxID, userID)
	if err != nil {
		return fmt.Errorf("store: touch sandbox %q: %w", sandboxID, err)
	}
	return requireOneRow(res, "store: touch sandbox "+sandboxID)
}

// ReapableSandboxes lists the caller-less sandboxes in one state whose
// last_active_at is older than idleBefore, oldest first.
//
// It is intentionally user-unscoped: the reaper works for the whole deployment,
// and its safety comes from the state machine (only READY may be paused, only
// PAUSED may be reclaimed) rather than from a user predicate.
func (s *Store) ReapableSandboxes(ctx context.Context, state contracts.SandboxState, idleBefore time.Time) ([]*contracts.Sandbox, error) {
	if state == "" {
		return nil, fmt.Errorf("store: reapable sandboxes: empty state: %w", ErrInvalid)
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+sandboxCols+` FROM sandboxes
		  WHERE state = ? AND last_active_at < ?
		  ORDER BY last_active_at ASC`, string(state), formatTime(idleBefore))
	if err != nil {
		return nil, fmt.Errorf("store: reapable sandboxes: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.Sandbox, 0, 8)
	for rows.Next() {
		sb, serr := scanSandbox(rows)
		if serr != nil {
			return nil, fmt.Errorf("store: reapable sandboxes: %w", serr)
		}
		out = append(out, sb)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reapable sandboxes: %w", err)
	}
	return out, nil
}

func scanSandbox(sc interface{ Scan(dest ...any) error }) (*contracts.Sandbox, error) {
	var (
		sb        contracts.Sandbox
		state     string
		workspace string
		lastAct   string
		created   string
		expires   sql.NullString
	)
	if err := sc.Scan(&sb.ID, &sb.UserID, &sb.ProjectID, &state, &sb.Provider, &sb.Node,
		&sb.Image, &sb.RootDir, &workspace, &sb.Message, &lastAct, &created, &expires); err != nil {
		return nil, err
	}
	sb.State = contracts.SandboxState(state)
	sb.Workspace = contracts.WorkspaceMode(workspace).Normalized()
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

// WorktreeByID resolves a worktree without a user check.
//
// It exists for the same reason WorktreePathByID does: callbacks receive a
// worktree id that a session already scoped to the caller, and they run after
// that check. Prefer Worktree() anywhere the caller's identity is not already
// established.
func (s *Store) WorktreeByID(ctx context.Context, worktreeID string) (*contracts.Worktree, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+worktreeCols+` FROM worktrees WHERE id = ?`, worktreeID)
	wt, err := scanWorktree(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: worktree %q: %w", worktreeID, err)
	}
	return wt, nil
}

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

// Worktree loads one worktree owned by the caller.
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

// WorktreeByPath finds the checkout that contains path.
//
// It exists for the remote workspace: every Workspace method receives only the
// root a session already resolved, and docker mode has to turn that host path
// back into the sandbox that owns it. It is deliberately user-unscoped, for the
// same reason WorktreeByID is — the caller proved ownership before asking — and
// it picks the deepest match so a worktree is never shadowed by the checkout it
// lives beside.
func (s *Store) WorktreeByPath(ctx context.Context, path string) (*contracts.Worktree, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store: worktree by path: empty path: %w", ErrInvalid)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+worktreeCols+` FROM worktrees`)
	if err != nil {
		return nil, fmt.Errorf("store: worktree by path: %w", err)
	}
	defer rows.Close()

	var best *contracts.Worktree
	bestLen := -1
	for rows.Next() {
		wt, err := scanWorktree(rows)
		if err != nil {
			return nil, fmt.Errorf("store: worktree by path: %w", err)
		}
		if !pathContains(wt.Path, path) {
			continue
		}
		if n := len(filepath.Clean(wt.Path)); n > bestLen {
			best, bestLen = wt, n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: worktree by path: %w", err)
	}
	if best == nil {
		return nil, fmt.Errorf("store: no worktree contains %q: %w", path, ErrNotFound)
	}
	return best, nil
}

// pathContains reports whether target is root or lives below it. Windows paths
// are compared case-insensitively, the way the OS does.
func pathContains(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	if target == root {
		return true
	}
	return strings.HasPrefix(target, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
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
