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
    -- Where a pull request for this project is opened (FR-12.6). The token is
    -- sealed by the control plane's secret box; the columns are added to old
    -- databases by store.addColumn, because CREATE TABLE IF NOT EXISTS never
    -- alters an existing table.
    forge_kind        TEXT NOT NULL DEFAULT '',
    forge_api_base    TEXT NOT NULL DEFAULT '',
    forge_repo        TEXT NOT NULL DEFAULT '',
    forge_base_branch TEXT NOT NULL DEFAULT '',
    forge_token_cipher BLOB,
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
    -- How the control plane reaches this sandbox's files: '' / 'host' is a
    -- directory it can open, 'api' is a workspace that exists only inside the
    -- container (docs/EXECUTION-BOUNDARY.md stage 2). root_dir follows: a
    -- container path ("/workspace") for 'api'.
    workspace      TEXT NOT NULL DEFAULT '',
    message        TEXT NOT NULL DEFAULT '',
    cpu_milli      INTEGER NOT NULL DEFAULT 0,
    mem_mb         INTEGER NOT NULL DEFAULT 0,
    disk_gb        INTEGER NOT NULL DEFAULT 0,
    last_active_at TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    expires_at     TEXT,
    -- The key this sandbox's own HTTP API requires, sealed with the deployment's
    -- secret box (AES-GCM; store/secrets.go). One key per container: a deployment
    -- key would open every tenant's shell once sandboxes are addressable by name
    -- on a shared network. Never selected by sandboxCols.
    api_key_cipher BLOB,
    -- The sandbox's own proof secret, sealed the same way (store/sandbox_proof.go).
    -- The gateway HMACs a model token with it, so "a token alone" is not enough
    -- to spend it; the value never enters the container, only HMAC(secret, token)
    -- in the agent's environment does. Never selected by sandboxCols.
    proof_cipher   BLOB,
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
    -- The parent transcript seq this branch inherits through. A fork records a
    -- cut instead of copying events: the transcript view merges the parent's log
    -- up to forked_at_seq with the branch's own events. Zero means "not a fork".
    -- Added to older databases by store.migrate.
    forked_at_seq  INTEGER NOT NULL DEFAULT 0,
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
-- The reconciler (internal/reconcile) sweeps whatever claims a turn is in
-- flight on boot and once per watchdog interval. A partial index keeps that
-- pass proportional to the runs actually stuck, not to every session the
-- deployment has ever created.
CREATE INDEX IF NOT EXISTS sessions_running ON sessions (status) WHERE status = 'running';

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

-- ============ audit_log: the forensic trail (docs/AUDIT.md) ============
--
-- `tool_calls` above is the transcript's Trace: it cascades away with its
-- session, and it is what the user reads. This table is the operator's copy of
-- what happened, and it is deliberately a different thing:
--
--   * no foreign keys. Deleting a session, project or user must not erase the
--     record of what that account's agent did — the same rule the gateway
--     revocation tombstones follow (gateway_revocations, above).
--   * append-only. A tool call is two rows: 'start' (intent: tool, bounded
--     preview, ids, model, commit) and 'end' (observation: status, exit code,
--     duration, bytes). Neither row is updated after it is written.
--   * `seq` is monotonic per session, allocated by the writer (one SQLite
--     writer connection, so COALESCE(MAX(seq),0)+1 is atomic). Ordering by
--     (session_id, seq) reconstructs the order the facts were recorded.
--   * `prev_hash`/`hash` chain every row to the one before it in the same
--     session (internal/audit). Verification is /v1/admin/audit/verify.
--
-- The preview is bounded (2048 bytes, 256 runes per field) and redacted; file
-- content is stored as sha256 + size, never as bytes. See internal/audit.
CREATE TABLE IF NOT EXISTS audit_log (
    id             TEXT PRIMARY KEY,
    seq            INTEGER NOT NULL,
    session_id     TEXT NOT NULL,
    tool_call_id   TEXT NOT NULL,
    phase          TEXT NOT NULL CHECK (phase IN ('start', 'end')),
    recorded_at    TEXT NOT NULL,
    started_at     TEXT NOT NULL,
    ended_at       TEXT,
    -- Denormalised on purpose: a LEFT JOIN would lose the label when its
    -- subject is deleted, which is exactly when an investigator needs it.
    user_id        TEXT NOT NULL DEFAULT '',
    project_id     TEXT NOT NULL DEFAULT '',
    sandbox_id     TEXT NOT NULL DEFAULT '',
    run_id         TEXT NOT NULL DEFAULT '',
    user_email     TEXT NOT NULL DEFAULT '',
    project_name   TEXT NOT NULL DEFAULT '',
    session_title  TEXT NOT NULL DEFAULT '',
    tool           TEXT NOT NULL,
    namespace      TEXT NOT NULL DEFAULT '',
    args_hash      TEXT NOT NULL DEFAULT '',
    args_preview   TEXT NOT NULL DEFAULT '',
    preview_bytes  INTEGER NOT NULL DEFAULT 0,
    args_truncated INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'running',
    is_error       INTEGER,
    exit_code      INTEGER,
    duration_ms    INTEGER,
    result_bytes   INTEGER,
    model_ref      TEXT NOT NULL DEFAULT '',
    commit_sha     TEXT NOT NULL DEFAULT '',
    prev_hash      TEXT NOT NULL DEFAULT '',
    hash           TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS audit_log_seq ON audit_log (session_id, seq);
-- The admin table's default order, and the retention cutoff's scan.
CREATE INDEX IF NOT EXISTS audit_log_started ON audit_log (started_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_user ON audit_log (user_id, started_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_project ON audit_log (project_id, started_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_session ON audit_log (session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_tool ON audit_log (tool, started_at DESC);
-- Retention deletes whole calls (both phases) by the call's start time.
CREATE INDEX IF NOT EXISTS audit_log_call ON audit_log (tool_call_id);

-- ============ audit_runs: the run-level facts a tool row points at ============
--
-- Token usage and cost are only final when the run settles, long after the
-- tool calls it contains. They live here, keyed by (run_id, session_id), and
-- the admin query joins them in; the tool rows themselves stay append-only so
-- their chain does not change when a run ends. No foreign key for the same
-- reason as audit_log: the row must survive the session it describes.
CREATE TABLE IF NOT EXISTS audit_runs (
    run_id       TEXT NOT NULL,
    session_id   TEXT NOT NULL,
    user_id      TEXT NOT NULL DEFAULT '',
    project_id   TEXT NOT NULL DEFAULT '',
    model_ref    TEXT NOT NULL DEFAULT '',
    commit_sha   TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'running',
    tokens_in    INTEGER NOT NULL DEFAULT 0,
    tokens_out   INTEGER NOT NULL DEFAULT 0,
    tokens_cache_r INTEGER NOT NULL DEFAULT 0,
    tokens_cache_w INTEGER NOT NULL DEFAULT 0,
    cost_usd     REAL NOT NULL DEFAULT 0,
    started_at   TEXT NOT NULL,
    settled_at   TEXT,
    PRIMARY KEY (run_id, session_id)
);
CREATE INDEX IF NOT EXISTS audit_runs_started ON audit_runs (started_at DESC);
CREATE INDEX IF NOT EXISTS audit_runs_session ON audit_runs (session_id, started_at DESC);

-- ============ audit_pruned: why a session's chain starts mid-sequence ============
--
-- Retention deletes whole calls, which removes a prefix of a session's rows.
-- Without a marker, "the first row I can see is seq 41" is indistinguishable
-- from "somebody deleted rows 1–40". This row is the marker: verification only
-- accepts a mid-chain head when the watermark covers the missing prefix. It is
-- written by the same retention transaction, never by a read path.
CREATE TABLE IF NOT EXISTS audit_pruned (
    session_id        TEXT PRIMARY KEY,
    pruned_through_seq INTEGER NOT NULL,
    pruned_rows       INTEGER NOT NULL DEFAULT 0,
    pruned_at         TEXT NOT NULL
);

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

-- What a session was actually handed (PRD FR-14.5). Copied at mount time rather
-- than joined, so editing or deleting a skill never rewrites a session's history.
-- written_at is the fact's timestamp; the primary key makes the record
-- write-once: a later mount of the same skill cannot replace it.
CREATE TABLE IF NOT EXISTS session_skills (
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    skill_id     TEXT NOT NULL,
    name         TEXT NOT NULL,
    version      TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    mounted_path TEXT NOT NULL DEFAULT '',
    recorded_at  TEXT NOT NULL,
    PRIMARY KEY (session_id, skill_id)
);
CREATE INDEX IF NOT EXISTS session_skills_session ON session_skills (session_id, name);

-- ============ snapshots ============
-- A snapshot belongs to its sandbox row, and the foreign key is the lifecycle:
-- a project delete cascades to sandboxes, and the rows go with it. Databases
-- created before this column had the key are rebuilt by
-- store.ensureSnapshotOwnership on the next Open (the same idempotent-migration
-- rule as the rest of this file).
CREATE TABLE IF NOT EXISTS snapshots (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sandbox_id   TEXT NOT NULL REFERENCES sandboxes(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    git_commit   TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    blob_key     TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS snapshots_sandbox ON snapshots (sandbox_id, created_at DESC);
-- Covers the janitor's sweep, which reads (id, sandbox_id, blob_key) for every
-- row once per pass: an index-only scan, so the sweep never reads the table it
-- is deciding about.
CREATE INDEX IF NOT EXISTS snapshots_sweep ON snapshots (sandbox_id, id, blob_key);

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
-- ============ api keys: a bearer token for scripts (PRD FR-13.9) ============
CREATE TABLE IF NOT EXISTS api_keys (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    token_hash   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);
CREATE INDEX IF NOT EXISTS api_keys_user ON api_keys (user_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS api_keys_hash ON api_keys (token_hash);

-- ============ pull_requests: what a session opened (PRD FR-12.6) ============
-- The forge is the source of truth; this row is the receipt. It is what lets a
-- reloaded session still show the link, and it is keyed by (session, branch) so
-- opening the same branch twice reuses the row instead of growing a history.
CREATE TABLE IF NOT EXISTS pull_requests (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    branch      TEXT NOT NULL,
    base_branch TEXT NOT NULL DEFAULT '',
    number      INTEGER NOT NULL DEFAULT 0,
    url         TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    forge_kind  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE (session_id, branch)
);
CREATE INDEX IF NOT EXISTS pull_requests_project ON pull_requests (project_id, branch);

-- ============ test_runs: the test-results panel (PRD FR-12.5) ============
-- Derived from a session's own tool events (an agent bash call) or from a
-- command typed in that session's terminal. Stored as a table rather than a
-- sidecar so it survives a reload and is deleted with its session by the same
-- cascade as events and tool_calls. `command` is kept because the panel shows
-- what was run; `output_tail` is only filled for unrecognised formats, where
-- the raw tail is the honest answer.
CREATE TABLE IF NOT EXISTS test_runs (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    run_id       TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL DEFAULT 'agent',
    runner       TEXT NOT NULL DEFAULT '',
    command      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'running',
    total        INTEGER NOT NULL DEFAULT 0,
    passed       INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    skipped      INTEGER NOT NULL DEFAULT 0,
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    failures     TEXT NOT NULL DEFAULT '[]',
    output_tail  TEXT NOT NULL DEFAULT '',
    started_at   TEXT NOT NULL,
    ended_at     TEXT
);
CREATE INDEX IF NOT EXISTS test_runs_session ON test_runs (session_id, started_at DESC);
-- One run per tool call. Terminal runs carry an empty tool_call_id, so the
-- uniqueness is partial: otherwise every terminal run would collide on ''.
CREATE UNIQUE INDEX IF NOT EXISTS test_runs_tool_call
    ON test_runs (session_id, tool_call_id) WHERE tool_call_id <> '';
-- Per-session MCP broker tokens (docs/EXECUTION-BOUNDARY.md §4, stage 3).
--
-- The sandbox never holds a user's MCP credential. It holds
-- <gateway>/gw/mcp/<token>; this row says which session and server that token
-- stands for, and revoking it (revoked_at) stops the proxy immediately. The
-- token itself is stored twice on purpose: hashed for lookup, sealed for the
-- launch path that has to hand the same token to a restarted agent instead of
-- minting a second live credential.
CREATE TABLE IF NOT EXISTS mcp_tokens (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    server_id    TEXT NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL,
    token_cipher BLOB,
    created_at   TEXT NOT NULL,
    revoked_at   TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS mcp_tokens_hash ON mcp_tokens (token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS mcp_tokens_session_server ON mcp_tokens (session_id, server_id);

-- ============ gateway_revocations: the durable half of token revocation ============
--
-- A model-gateway token is an HMAC claim (internal/llmgw), so the only way to
-- end one before its TTL is a counter the verifier can compare against. That
-- counter used to live in the control plane's memory, which meant a restart
-- inside the token's lifetime silently re-enabled every token minted before it
-- — the adversarial acceptance run #2 named this as one of the three risks it
-- left open.
--
-- The row is the authority: `generation` is the number every token for this
-- session must carry, `expires_at` is the moment the last token that could
-- still be alive expires (so the row is swept, not kept forever), and
-- `revoked_at` says when the counter last moved. The in-memory map in
-- internal/llmgw stays as a fast path but is no longer authoritative.
--
-- There is deliberately NO foreign key to sessions. A revoked session can be
-- deleted, and its tokens (generation 0, the ones minted before the revoke)
-- must not become valid again when the row cascades away. The row is removed by
-- the sweep at `expires_at`, which is exactly when the last affected token
-- expires by its own claims.
CREATE TABLE IF NOT EXISTS gateway_revocations (
    session_id TEXT PRIMARY KEY,
    generation INTEGER NOT NULL,
    revoked_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS gateway_revocations_expiry ON gateway_revocations (expires_at);
