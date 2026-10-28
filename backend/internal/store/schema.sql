-- PiForge SQLite schema.
--
-- Mirrors PRD §8.2 with the simplifications that come from running a single
-- binary against SQLite instead of Postgres:
--   * jsonb  -> TEXT holding JSON
--   * bytea  -> BLOB
--   * timestamptz -> TEXT, RFC3339 with nanoseconds in UTC
--   * boolean -> INTEGER 0/1
--   * bigint/numeric -> INTEGER/REAL
--
-- The file is applied on every Open and every statement must therefore be
-- re-runnable (IF NOT EXISTS everywhere). Do not add destructive statements
-- here; use an explicit migration step instead.

-- ============ users (PRD §8.2) ============
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL COLLATE NOCASE UNIQUE,
    display_name  TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    idp_subject   TEXT UNIQUE,
    role          TEXT NOT NULL DEFAULT 'user',
    status        TEXT NOT NULL DEFAULT 'active',
    settings      TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL,
    deleted_at    TEXT
);

-- ============ providers: one table + scope shadow (PRD §5.7, fastclaw) ============
CREATE TABLE IF NOT EXISTS providers (
    id         TEXT PRIMARY KEY,
    scope      TEXT NOT NULL CHECK (scope IN ('admin', 'user')),
    owner_id   TEXT REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    api        TEXT NOT NULL,
    base_url   TEXT NOT NULL DEFAULT '',
    key_cipher BLOB,
    models     TEXT NOT NULL DEFAULT '[]',
    extra      TEXT NOT NULL DEFAULT '{}',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);
-- COALESCE(owner_id, '') because SQLite treats NULLs as distinct in UNIQUE
-- indexes, which would let the admin scope grow duplicate names.
CREATE UNIQUE INDEX IF NOT EXISTS providers_scope_owner_name
    ON providers (scope, COALESCE(owner_id, ''), name);

-- ============ projects ============
CREATE TABLE IF NOT EXISTS projects (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    repo_url       TEXT NOT NULL DEFAULT '',
    default_branch TEXT NOT NULL DEFAULT 'main',
    created_at     TEXT NOT NULL,
    UNIQUE (user_id, name)
);

-- ============ sandboxes (granularity = project x user) ============
CREATE TABLE IF NOT EXISTS sandboxes (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    state          TEXT NOT NULL,
    provider       TEXT NOT NULL DEFAULT '',
    node           TEXT NOT NULL DEFAULT '',
    image          TEXT NOT NULL DEFAULT '',
    root_dir       TEXT NOT NULL DEFAULT '',
    message        TEXT NOT NULL DEFAULT '',
    cpu_milli      INTEGER NOT NULL DEFAULT 0,
    mem_mb         INTEGER NOT NULL DEFAULT 0,
    disk_gb        INTEGER NOT NULL DEFAULT 0,
    last_active_at TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    expires_at     TEXT,
    UNIQUE (project_id)
);

-- ============ worktrees ============
CREATE TABLE IF NOT EXISTS worktrees (
    id          TEXT PRIMARY KEY,
    sandbox_id  TEXT NOT NULL REFERENCES sandboxes(id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    branch      TEXT NOT NULL,
    base_commit TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    UNIQUE (sandbox_id, path)
);
CREATE INDEX IF NOT EXISTS worktrees_user ON worktrees (user_id);

-- ============ sessions: main | branch | sidecar ============
CREATE TABLE IF NOT EXISTS sessions (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    worktree_id    TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('main', 'branch', 'sidecar')),
    root_id        TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    parent_id      TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    fork_entry_id  TEXT NOT NULL DEFAULT '',
    label          TEXT NOT NULL DEFAULT '',
    title          TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'active',
    model_ref      TEXT NOT NULL DEFAULT '',
    thinking_level TEXT NOT NULL DEFAULT '',
    config         TEXT NOT NULL DEFAULT '{}',
    ephemeral      INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL,
    last_active_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_active ON sessions (user_id, last_active_at DESC);
CREATE INDEX IF NOT EXISTS sessions_root ON sessions (root_id);
CREATE INDEX IF NOT EXISTS sessions_project ON sessions (user_id, project_id);

-- ============ runs (one prompt -> agent_settled) ============
CREATE TABLE IF NOT EXISTS runs (
    id             TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    trigger        TEXT NOT NULL DEFAULT 'user',
    status         TEXT NOT NULL DEFAULT 'started',
    tokens_in      INTEGER NOT NULL DEFAULT 0,
    tokens_out     INTEGER NOT NULL DEFAULT 0,
    tokens_cache_r INTEGER NOT NULL DEFAULT 0,
    tokens_cache_w INTEGER NOT NULL DEFAULT 0,
    cost_usd       REAL NOT NULL DEFAULT 0,
    started_at     TEXT NOT NULL,
    settled_at     TEXT,
    interrupted_by TEXT NOT NULL DEFAULT '',
    completion_note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS runs_session ON runs (session_id, started_at);

-- ============ events: transcript projection, (session, seq) is the cursor ============
CREATE TABLE IF NOT EXISTS events (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    run_id     TEXT NOT NULL DEFAULT '',
    entry_id   TEXT NOT NULL DEFAULT '',
    parent_id  TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL,
    payload    TEXT,
    blob_ref   TEXT NOT NULL DEFAULT '',
    ts         TEXT NOT NULL,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX IF NOT EXISTS events_session_entry ON events (session_id, entry_id);

-- ============ tool calls (in-conversation Trace, not an audit log) ============
CREATE TABLE IF NOT EXISTS tool_calls (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    run_id       TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL,
    namespace    TEXT NOT NULL DEFAULT '',
    args_hash    TEXT NOT NULL DEFAULT '',
    args_preview TEXT,
    is_error     INTEGER,
    exit_code    INTEGER,
    duration_ms  INTEGER,
    result_bytes INTEGER,
    started_at   TEXT NOT NULL,
    ended_at     TEXT
);
CREATE INDEX IF NOT EXISTS tool_calls_session ON tool_calls (session_id, started_at);

-- ============ MCP (single table + scope) ============
CREATE TABLE IF NOT EXISTS mcp_servers (
    id             TEXT PRIMARY KEY,
    scope          TEXT NOT NULL CHECK (scope IN ('admin', 'user')),
    owner_id       TEXT REFERENCES users(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    transport      TEXT NOT NULL,
    command        TEXT NOT NULL DEFAULT '',
    args           TEXT NOT NULL DEFAULT '[]',
    cwd            TEXT NOT NULL DEFAULT '',
    url            TEXT NOT NULL DEFAULT '',
    headers        TEXT NOT NULL DEFAULT '{}',
    secrets_cipher BLOB,
    oauth          TEXT NOT NULL DEFAULT '{}',
    exposure       TEXT NOT NULL DEFAULT 'codemode',
    tool_overrides TEXT NOT NULL DEFAULT '{}',
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS mcp_servers_scope_owner_name
    ON mcp_servers (scope, COALESCE(owner_id, ''), name);

-- ============ skills ============
CREATE TABLE IF NOT EXISTS skills (
    id           TEXT PRIMARY KEY,
    scope        TEXT NOT NULL CHECK (scope IN ('admin', 'user')),
    owner_id     TEXT REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    version      TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL,
    bundle_key   TEXT NOT NULL DEFAULT '',
    source_url   TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS skills_scope_owner_name_version
    ON skills (scope, COALESCE(owner_id, ''), name, version);

-- ============ snapshots ============
CREATE TABLE IF NOT EXISTS snapshots (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sandbox_id   TEXT NOT NULL,
    kind         TEXT NOT NULL,
    git_commit   TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    blob_key     TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS snapshots_sandbox ON snapshots (sandbox_id, created_at DESC);

-- ============ auth sessions: opaque tokens, stored as sha256 hashes ============
CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_sessions_user ON auth_sessions (user_id);
CREATE INDEX IF NOT EXISTS auth_sessions_expiry ON auth_sessions (expires_at);

-- ============ messages: client_msg_id idempotency (PRD §7.4.5) ============
-- Not in PRD §8.2: the PRD relies on the gateway for idempotency, but a
-- single binary has no separate gateway, so the record lives next to the
-- transcript instead.
CREATE TABLE IF NOT EXISTS messages (
    id             TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_msg_id  TEXT NOT NULL,
    text_body      TEXT NOT NULL DEFAULT '',
    accepted       INTEGER NOT NULL DEFAULT 1,
    disposition    TEXT NOT NULL DEFAULT '',
    queue_position INTEGER NOT NULL DEFAULT 0,
    run_id         TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    UNIQUE (session_id, client_msg_id)
);
-- ============ support_access: admin read-only troubleshooting (PRD FR-16.8) ============
-- An admin may read one session's transcript, for a bounded window, and only
-- after saying why. The owner sees the record: this is a receipt, not an audit
-- system, and pretending it is one would be worse than not having it.
CREATE TABLE IF NOT EXISTS support_access (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    owner_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    admin_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    seen_at    TEXT
);

CREATE INDEX IF NOT EXISTS support_access_owner ON support_access (owner_id, seen_at);
CREATE INDEX IF NOT EXISTS support_access_session ON support_access (session_id);
-- ============ feature_flags: per-user switches (PRD FR-16.7) ============
-- Defaults live in Go (store.featureDefaults) so a flag that is never written has
-- one authoritative value; a row here is an explicit override.
CREATE TABLE IF NOT EXISTS feature_flags (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    flag       TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (user_id, flag)
);