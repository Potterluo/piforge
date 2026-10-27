package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// ---------------------------------------------------------------------------
// Tool calls (in-conversation Trace; not an audit log)
// ---------------------------------------------------------------------------

// ToolCall is one tool invocation, persisted around the side effect: the
// intent before execution, the observation after (PRD §7.3).
type ToolCall struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	RunID       string          `json:"run_id,omitempty"`
	Name        string          `json:"name"`
	Namespace   string          `json:"namespace,omitempty"`
	ArgsHash    string          `json:"args_hash"`
	ArgsPreview json.RawMessage `json:"args_preview,omitempty"`
	IsError     *bool           `json:"is_error,omitempty"`
	ExitCode    *int            `json:"exit_code,omitempty"`
	DurationMS  *int            `json:"duration_ms,omitempty"`
	ResultBytes *int            `json:"result_bytes,omitempty"`
	StartedAt   time.Time       `json:"started_at"`
	EndedAt     *time.Time      `json:"ended_at,omitempty"`
}

// ToolCallFinish is the post-execution observation.
type ToolCallFinish struct {
	IsError     bool
	ExitCode    *int
	DurationMS  *int
	ResultBytes *int
	EndedAt     *time.Time
}

// CreateToolCall records a tool invocation intent before it runs.
func (s *Store) CreateToolCall(ctx context.Context, userID string, tc ToolCall) error {
	if tc.ID == "" || tc.SessionID == "" || tc.Name == "" {
		return fmt.Errorf("store: create tool call: id, session and name are required: %w", ErrInvalid)
	}
	if _, err := s.Session(ctx, userID, tc.SessionID); err != nil {
		return err
	}
	started := tc.StartedAt
	if started.IsZero() {
		started = s.now()
	}
	preview := any(nil)
	if len(tc.ArgsPreview) > 0 {
		if !json.Valid(tc.ArgsPreview) {
			return fmt.Errorf("store: create tool call %q: args_preview is not JSON: %w", tc.ID, ErrInvalid)
		}
		preview = string(tc.ArgsPreview)
	}
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO tool_calls (id, user_id, session_id, run_id, name, namespace, args_hash,
		                         args_preview, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.ID, userID, tc.SessionID, tc.RunID, tc.Name, tc.Namespace, tc.ArgsHash,
		preview, formatTime(started))
	if err != nil {
		return fmt.Errorf("store: create tool call %q: %w", tc.ID, err)
	}
	return nil
}

// FinishToolCall records the result of a tool invocation. Replaying it is
// harmless (last write wins), which keeps the event pump tolerant of retries.
func (s *Store) FinishToolCall(ctx context.Context, userID, toolCallID string, in ToolCallFinish) error {
	ended := in.EndedAt
	if ended == nil {
		now := s.now()
		ended = &now
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE tool_calls
		    SET is_error = ?, exit_code = ?, duration_ms = ?, result_bytes = ?, ended_at = ?
		  WHERE id = ? AND user_id = ?`,
		boolInt(in.IsError), in.ExitCode, in.DurationMS, in.ResultBytes,
		formatTime(*ended), toolCallID, userID)
	if err != nil {
		return fmt.Errorf("store: finish tool call %q: %w", toolCallID, err)
	}
	return requireOneRow(res, "store: finish tool call "+toolCallID)
}

// ListToolCalls lists a session's tool calls oldest first.
func (s *Store) ListToolCalls(ctx context.Context, userID, sessionID string) ([]*ToolCall, error) {
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, session_id, run_id, name, namespace, args_hash,
		        COALESCE(args_preview, ''), is_error, exit_code, duration_ms,
		        result_bytes, started_at, ended_at
		   FROM tool_calls WHERE session_id = ? AND user_id = ?
		  ORDER BY started_at ASC, id ASC`, sessionID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list tool calls for %q: %w", sessionID, err)
	}
	defer rows.Close()

	out := make([]*ToolCall, 0, 8)
	for rows.Next() {
		tc, err := scanToolCall(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list tool calls for %q: %w", sessionID, err)
		}
		out = append(out, tc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tool calls for %q: %w", sessionID, err)
	}
	return out, nil
}

func scanToolCall(sc interface{ Scan(dest ...any) error }) (*ToolCall, error) {
	var (
		tc          ToolCall
		preview     string
		isErr       sql.NullInt64
		exitCode    sql.NullInt64
		duration    sql.NullInt64
		resultBytes sql.NullInt64
		started     string
		ended       sql.NullString
	)
	if err := sc.Scan(&tc.ID, &tc.SessionID, &tc.RunID, &tc.Name, &tc.Namespace, &tc.ArgsHash,
		&preview, &isErr, &exitCode, &duration, &resultBytes, &started, &ended); err != nil {
		return nil, err
	}
	if preview != "" {
		tc.ArgsPreview = json.RawMessage(preview)
	}
	if isErr.Valid {
		v := isErr.Int64 == 1
		tc.IsError = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		tc.ExitCode = &v
	}
	if duration.Valid {
		v := int(duration.Int64)
		tc.DurationMS = &v
	}
	if resultBytes.Valid {
		v := int(resultBytes.Int64)
		tc.ResultBytes = &v
	}
	tc.StartedAt = parseTime(started)
	tc.EndedAt = parseTimePtr(ended)
	return &tc, nil
}

// ---------------------------------------------------------------------------
// MCP servers (single table + scope)
// ---------------------------------------------------------------------------

// CreateMCPServer creates a user-scoped MCP server definition.
func (s *Store) CreateMCPServer(ctx context.Context, userID string, in contracts.MCPServer) (*contracts.MCPServer, error) {
	if userID == "" {
		return nil, fmt.Errorf("store: create mcp server: empty user: %w", ErrInvalid)
	}
	if in.Name == "" {
		return nil, fmt.Errorf("store: create mcp server: name is required: %w", ErrInvalid)
	}
	transport := in.Transport
	if transport == "" {
		transport = contracts.MCPStdio
	}
	if transport != contracts.MCPStdio && transport != contracts.MCPHTTP {
		return nil, fmt.Errorf("store: create mcp server %q: unknown transport %q: %w", in.Name, transport, ErrInvalid)
	}
	exposure := in.Exposure
	if exposure == "" {
		exposure = "codemode"
	}
	args, err := json.Marshal(in.Args)
	if err != nil {
		return nil, fmt.Errorf("store: create mcp server %q: encode args: %w", in.Name, err)
	}
	if in.Args == nil {
		args = []byte("[]")
	}
	enabled := in.Enabled
	id := NewID("mcp")
	_, err = s.w.ExecContext(ctx,
		`INSERT INTO mcp_servers (id, scope, owner_id, name, transport, command, args, cwd, url,
		                          headers, exposure, enabled, created_at)
		 VALUES (?, 'user', ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?, ?)`,
		id, userID, in.Name, string(transport), in.Command, string(args), in.Cwd, in.URL,
		exposure, boolInt(enabled), formatTime(s.now()))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create mcp server %q: %w", in.Name, ErrConflict)
		}
		return nil, fmt.Errorf("store: create mcp server %q: %w", in.Name, err)
	}
	return s.MCPServer(ctx, userID, id)
}

// MCPServer loads one MCP server the caller owns.
func (s *Store) MCPServer(ctx context.Context, userID, id string) (*contracts.MCPServer, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT id, scope, name, transport, command, args, cwd, url, exposure,
		        COALESCE(LENGTH(secrets_cipher), 0), enabled, created_at
		   FROM mcp_servers WHERE id = ? AND scope = 'user' AND owner_id = ?`, id, userID)
	m, err := scanMCPServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: mcp server %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: mcp server %q: %w", id, err)
	}
	return m, nil
}

// DeleteMCPServer removes one of the caller's own MCP servers. Shared
// (admin-scope) definitions are not deletable through this path, and another
// user's row reports ErrNotFound.
func (s *Store) DeleteMCPServer(ctx context.Context, userID, id string) error {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM mcp_servers WHERE id = ? AND scope = 'user' AND owner_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete mcp server %q: %w", id, err)
	}
	return requireOneRow(res, "store: delete mcp server "+id)
}

// ListMCPServers lists the caller's own MCP servers plus the shared ones.
func (s *Store) ListMCPServers(ctx context.Context, userID string) ([]*contracts.MCPServer, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, scope, name, transport, command, args, cwd, url, exposure,
		        COALESCE(LENGTH(secrets_cipher), 0), enabled, created_at
		   FROM mcp_servers
		  WHERE (scope = 'user' AND owner_id = ?) OR scope = 'admin'
		  ORDER BY scope ASC, name ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list mcp servers: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.MCPServer, 0, 8)
	for rows.Next() {
		m, err := scanMCPServer(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list mcp servers: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list mcp servers: %w", err)
	}
	return out, nil
}

func scanMCPServer(sc interface{ Scan(dest ...any) error }) (*contracts.MCPServer, error) {
	var (
		m          contracts.MCPServer
		scope      string
		transport  string
		args       string
		hasSecrets int64
		enabled    int
		created    string
	)
	if err := sc.Scan(&m.ID, &scope, &m.Name, &transport, &m.Command, &args, &m.Cwd, &m.URL,
		&m.Exposure, &hasSecrets, &enabled, &created); err != nil {
		return nil, err
	}
	m.Scope = contracts.SkillScope(scope)
	m.Transport = contracts.MCPTransport(transport)
	m.HasSecrets = hasSecrets > 0
	m.Enabled = enabled == 1
	m.CreatedAt = parseTime(created)
	if args != "" {
		if err := json.Unmarshal([]byte(args), &m.Args); err != nil {
			return nil, fmt.Errorf("store: decode mcp args: %w", err)
		}
	}
	return &m, nil
}

// ---------------------------------------------------------------------------
// Skills
// ---------------------------------------------------------------------------

// CreateSkill records a user-scoped skill bundle.
func (s *Store) CreateSkill(ctx context.Context, userID string, in contracts.Skill) (*contracts.Skill, error) {
	if userID == "" {
		return nil, fmt.Errorf("store: create skill: empty user: %w", ErrInvalid)
	}
	if in.Name == "" || in.ContentHash == "" {
		return nil, fmt.Errorf("store: create skill: name and content_hash are required: %w", ErrInvalid)
	}
	version := in.Version
	if version == "" {
		version = "0.0.0"
	}
	id := NewID("skl")
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO skills (id, scope, owner_id, name, version, description, content_hash,
		                     bundle_key, source_url, enabled, created_at)
		 VALUES (?, 'user', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, in.Name, version, in.Description, in.ContentHash, in.BundleKey, in.SourceURL,
		boolInt(in.Enabled), formatTime(s.now()))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create skill %q: %w", in.Name, ErrConflict)
		}
		return nil, fmt.Errorf("store: create skill %q: %w", in.Name, err)
	}
	return s.Skill(ctx, userID, id)
}

// Skill loads one skill the caller can see (own or shared).
func (s *Store) Skill(ctx context.Context, userID, skillID string) (*contracts.Skill, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT id, scope, name, version, description, content_hash, bundle_key, source_url, enabled, created_at
		   FROM skills
		  WHERE id = ? AND ((scope = 'user' AND owner_id = ?) OR scope = 'admin')`,
		skillID, userID)
	sk, err := scanSkill(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: skill %q: %w", skillID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: skill %q: %w", skillID, err)
	}
	return sk, nil
}

// DeleteSkill removes one of the caller's own skills. Shared skills are not
// deletable through this path, and another user's skill reports ErrNotFound.
func (s *Store) DeleteSkill(ctx context.Context, userID, skillID string) error {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM skills WHERE id = ? AND scope = 'user' AND owner_id = ?`, skillID, userID)
	if err != nil {
		return fmt.Errorf("store: delete skill %q: %w", skillID, err)
	}
	return requireOneRow(res, "store: delete skill "+skillID)
}

// ListSkills lists the caller's own skills plus the shared ones (PRD §5.8).
func (s *Store) ListSkills(ctx context.Context, userID string) ([]*contracts.Skill, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, scope, name, version, description, content_hash, bundle_key, source_url, enabled, created_at
		   FROM skills
		  WHERE (scope = 'user' AND owner_id = ?) OR scope = 'admin'
		  ORDER BY scope ASC, name ASC, version ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list skills: %w", err)
	}
	defer rows.Close()

	out := make([]*contracts.Skill, 0, 8)
	for rows.Next() {
		sk, err := scanSkill(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list skills: %w", err)
		}
		out = append(out, sk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list skills: %w", err)
	}
	return out, nil
}

func scanSkill(sc interface{ Scan(dest ...any) error }) (*contracts.Skill, error) {
	var (
		sk      contracts.Skill
		scope   string
		enabled int
		created string
	)
	if err := sc.Scan(&sk.ID, &scope, &sk.Name, &sk.Version, &sk.Description,
		&sk.ContentHash, &sk.BundleKey, &sk.SourceURL, &enabled, &created); err != nil {
		return nil, err
	}
	sk.Scope = contracts.SkillScope(scope)
	sk.Enabled = enabled == 1
	sk.CreatedAt = parseTime(created)
	return &sk, nil
}

// ---------------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------------

// Snapshot is one sandbox snapshot (PRD FR-5.6). contracts has no type for it,
// so the store owns this shape.
type Snapshot struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	SandboxID   string    `json:"sandbox_id"`
	Kind        string    `json:"kind"` // fs | fs_mem
	GitCommit   string    `json:"git_commit,omitempty"`
	ContentHash string    `json:"content_hash"`
	SizeBytes   int64     `json:"size_bytes"`
	BlobKey     string    `json:"blob_key"`
	CreatedAt   time.Time `json:"created_at"`
}

// CreateSnapshot records a snapshot taken at a tool boundary.
func (s *Store) CreateSnapshot(ctx context.Context, userID string, in Snapshot) (*Snapshot, error) {
	if _, err := s.Sandbox(ctx, userID, in.SandboxID); err != nil {
		return nil, err
	}
	kind := in.Kind
	if kind != "fs_mem" {
		kind = "fs"
	}
	if in.ContentHash == "" {
		return nil, fmt.Errorf("store: create snapshot: content_hash is required: %w", ErrInvalid)
	}
	id := NewID("snp")
	created := s.now()
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO snapshots (id, user_id, sandbox_id, kind, git_commit, content_hash,
		                        size_bytes, blob_key, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, in.SandboxID, kind, in.GitCommit, in.ContentHash, in.SizeBytes,
		in.BlobKey, formatTime(created)); err != nil {
		return nil, fmt.Errorf("store: create snapshot: %w", err)
	}
	return &Snapshot{
		ID: id, UserID: userID, SandboxID: in.SandboxID, Kind: kind,
		GitCommit: in.GitCommit, ContentHash: in.ContentHash, SizeBytes: in.SizeBytes,
		BlobKey: in.BlobKey, CreatedAt: created,
	}, nil
}

// ListSnapshots lists the caller's snapshots for a sandbox, newest first.
func (s *Store) ListSnapshots(ctx context.Context, userID, sandboxID string) ([]*Snapshot, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, user_id, sandbox_id, kind, git_commit, content_hash, size_bytes, blob_key, created_at
		   FROM snapshots WHERE sandbox_id = ? AND user_id = ?
		  ORDER BY created_at DESC`, sandboxID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list snapshots: %w", err)
	}
	defer rows.Close()

	out := make([]*Snapshot, 0, 4)
	for rows.Next() {
		var (
			sn      Snapshot
			created string
		)
		if err := rows.Scan(&sn.ID, &sn.UserID, &sn.SandboxID, &sn.Kind, &sn.GitCommit,
			&sn.ContentHash, &sn.SizeBytes, &sn.BlobKey, &created); err != nil {
			return nil, fmt.Errorf("store: list snapshots: %w", err)
		}
		sn.CreatedAt = parseTime(created)
		out = append(out, &sn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list snapshots: %w", err)
	}
	return out, nil
}
