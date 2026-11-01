/**
 * Wire types for the PiForge HTTP API.
 *
 * These are a hand-maintained mirror of backend/internal/contracts. Every field
 * name here matches a Go json tag exactly; the e2e suite asserts the shapes, so
 * a drift shows up as a failing test rather than a silent runtime undefined.
 *
 * Nothing in this file may import anything: it is the bottom of the frontend
 * dependency graph so that both the shell and the workspace panels can depend
 * on it without depending on each other.
 */

// ---------------------------------------------------------------------------
// Roles and lifecycle
// ---------------------------------------------------------------------------

export type Role = "user" | "admin";

export type SessionKind = "main" | "branch" | "sidecar";

export type SessionStatus =
  | "active"
  | "running"
  | "idle"
  | "paused"
  | "settled"
  | "failed"
  | "archived"
  // A run that was cut off when its agent process went away (a restart or a
  // crash). The backend writes this only after proving nothing serves it; the
  // reason arrives in the run.settled event's payload.
  | "interrupted";

export type SandboxState =
  | "CREATING"
  | "STARTING"
  | "READY"
  | "BUSY"
  | "IDLE"
  | "PAUSED"
  | "TERMINATED"
  | "ERROR"
  | "MISSING";

// ---------------------------------------------------------------------------
// Domain objects
// ---------------------------------------------------------------------------

export interface User {
  id: string;
  email: string;
  display_name: string;
  role: Role;
  status: string;
  created_at: string;
  deleted_at?: string | null;
}

export interface Project {
  id: string;
  user_id: string;
  name: string;
  repo_url: string;
  default_branch: string;
  created_at: string;
  sandbox_state: SandboxState;
  session_count: number;
  last_active_at?: string | null;
  /**
   * Where this project opens pull requests (FR-12.6). Absent means "not
   * configured yet"; `has_token` says whether a token is sealed at rest. The
   * token itself is write-only and never appears in any response.
   */
  forge?: ForgeConfig | null;
}

/** One project's forge wiring, minus the token. */
export interface ForgeConfig {
  kind: "github" | "gitlab" | string;
  api_base: string;
  repo: string;
  base_branch?: string;
  has_token: boolean;
}

export interface Worktree {
  id: string;
  sandbox_id: string;
  user_id: string;
  path: string;
  branch: string;
  base_commit: string;
  created_at: string;
}

export interface Session {
  /** Scratch session (FR-1.10): hidden from the list, swept after a few hours. */
  ephemeral?: boolean;
  id: string;
  user_id: string;
  project_id: string;
  worktree_id?: string;
  kind: SessionKind;
  root_id?: string;
  parent_id?: string;
  fork_entry_id?: string;
  /**
   * The parent transcript seq this fork inherits through (absent/0 = not a
   * fork). The branch view is the parent's log up to this seq followed by its
   * own events; nothing is copied.
   */
  forked_at_seq?: number;
  label?: string;
  title: string;
  status: SessionStatus;
  model_ref?: string;
  thinking_level?: string;
  created_at: string;
  last_active_at: string;
}

export interface Run {
  id: string;
  session_id: string;
  trigger: string;
  status: string;
  tokens_in: number;
  tokens_out: number;
  tokens_cache_read: number;
  tokens_cache_write: number;
  cost_usd: number;
  started_at: string;
  settled_at?: string | null;
}

export interface SessionStats {
  session_id: string;
  user_messages: number;
  assistant_messages: number;
  tool_calls: number;
  tokens_in: number;
  tokens_out: number;
  tokens_cache_read: number;
  cost_usd: number;
  context_tokens: number;
  context_window: number;
  context_percent: number;
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

/**
 * The normalized event envelope. `seq` is monotonic per transcript and doubles
 * as the SSE event id, so a reconnecting client replays from where it stopped.
 * For a fork the transcript is the parent's log up to `forked_at_seq` followed
 * by the branch's own events, so the seqs are transcript-wide rather than the
 * branch session's own row numbers.
 */
export interface PfEvent {
  seq: number;
  session_id: string;
  branch_id: string;
  kind: SessionKind;
  type: EventType;
  ts: string;
  payload?: unknown;
  run_id?: string;
  entry_id?: string;
  parent_id?: string;
}

export const EV = {
  runStarted: "run.started",
  runSettled: "run.settled",
  runRetry: "run.retry",
  runAborted: "run.aborted",
  /**
   * A run ended without a result (stop / restart / agent gone). The payload
   * carries the cause, so the transcript can say where it came from instead of
   * inferring it from an error sentence.
   */
  runInterrupted: "run.interrupted",
  turnStarted: "turn.started",
  turnEnded: "turn.ended",
  msgStarted: "message.started",
  msgDelta: "message.delta",
  msgFinal: "message.final",
  toolStarted: "tool.started",
  toolProgress: "tool.progress",
  toolEnded: "tool.ended",
  queueChanged: "queue.updated",
  compaction: "context.compaction",
  contextUsage: "context.usage",
  ptyStream: "pty.stream",
  platformErr: "platform.error",
} as const;

export type EventType = (typeof EV)[keyof typeof EV] | string;

/** Payload of message.delta — a text or thinking increment. */
export interface DeltaPayload {
  message_id: string;
  block: "text" | "thinking";
  content_index: number;
  delta: string;
}

/** Payload of tool.started / tool.ended / tool.progress. */
export interface ToolPayload {
  tool_call_id: string;
  name: string;
  args?: Record<string, unknown>;
  chunk?: string;
  is_error?: boolean;
  duration_ms?: number;
  result_preview?: string;
}

/** Payload of run.settled. This is the ONLY completion signal. */
export interface SettledPayload {
  aborted: boolean;
  run_id?: string;
}

/**
 * Why a run ended without a result. The server writes one of the three explicit
 * causes; an old record that predates the field is matched from its sentence,
 * and an unrecognised sentence stays "" (unknown) rather than being forced into
 * a bucket.
 */
export type InterruptedCause = "stop" | "restart" | "process_exit" | "";

/** Payload of run.interrupted. */
export interface InterruptedPayload {
  cause?: InterruptedCause;
  reason?: string;
}

/**
 * What the checkout still holds after an interruption (GET
 * /v1/sessions/{id}/recovery). `summary` is composed server-side so the UI
 * cannot turn an unreadable worktree into "clean".
 */
export interface RecoverySurvival {
  summary: string;
  unavailable?: "sandbox" | "workspace";
  dirty_known: boolean;
  dirty_count: number;
  commit_known: boolean;
  last_commit?: string;
}

/** The recovery answer for one interrupted session. */
export interface RecoveryView {
  interrupted: boolean;
  cause?: InterruptedCause;
  cause_label?: string;
  reason?: string;
  /** The last user request in the interrupted run, from the event log. */
  last_request?: string;
  interrupted_at?: string;
  survival: RecoverySurvival;
  can_continue: boolean;
  /** Why 继续 is not offered; absent when it is. */
  blocked_reason?: string;
}

/** Payload of context.compaction. */
export interface CompactionPayload {
  /** "start" or "end"; the end of a failed attempt has neither a summary nor a kept entry. */
  phase?: "start" | "end";
  reason: "manual" | "threshold" | "overflow";
  /** Pi sets this when a compaction was stopped before it produced anything. */
  aborted?: boolean;
  summary?: string;
  first_kept_entry_id?: string;
  tokens_before?: number;
  estimated_tokens_after?: number;
}

export interface TreeEntry {
  entry_id: string;
  parent_id?: string;
  type: string;
  label?: string;
  role?: string;
  preview?: string;
  ts: string;
  children?: TreeEntry[];
}

export interface ForkPoint {
  entry_id: string;
  preview: string;
  ts: string;
}

export interface RuntimeStatus {
  session_id: string;
  running: boolean;
  streaming: boolean;
  compacting: boolean;
  queue_depth: number;
  model_ref?: string;
  last_seq: number;
  process_id?: number;
  session_file?: string;
  /**
   * The model the control plane would use for this session right now. Present
   * even when no agent exists yet, so the header can name a real model (or
   * 未配置) instead of an internal `unknown`.
   */
  resolved_model_ref?: string;
  /** Nothing to send to at all: the header says 未配置. */
  model_unavailable?: boolean;
}

export interface PromptResult {
  accepted: boolean;
  disposition: "started" | "queued" | "handled";
  queue_position: number;
  run_id: string;
  /** The model this turn used when the stored reference had to be re-resolved. */
  model_ref?: string;
  /** The reference (or provider) that no longer resolved, when that happened. */
  model_fallback_from?: string;
  /**
   * Present only when an inline comment was cut to fit the server's caps
   * (`backend/internal/httpapi/review_comment.go`). The composed message also
   * says it — that string is the transcript entry — so this is the same fact in
   * a form the UI can render without parsing Chinese text.
   */
  review_truncation?: ReviewTruncation;
}

/** What the inline-comment composer left out, with exact counts. */
export interface ReviewTruncation {
  quote_lines_kept: number;
  quote_lines_omitted: number;
  quote_lines_shortened: number;
  quote_line_runes_cut: number;
  body_runes_cut: number;
}

export interface InterruptResult {
  cleared_steering: string[];
  cleared_follow_up: string[];
}

// ---------------------------------------------------------------------------
// Workspace
// ---------------------------------------------------------------------------

export interface FileNode {
  name: string;
  path: string;
  is_dir: boolean;
  size?: number;
  modified?: string;
  git_state?: string;
  children?: FileNode[];
  truncated?: boolean;
}

export interface FileContent {
  path: string;
  size: number;
  binary: boolean;
  truncated: boolean;
  body: string;
  language?: string;
  mime_type?: string;
  modified: string;
  /**
   * The bytes were reached through a symlink that leaves the workspace root.
   * The server still returns them (in a container placement the link is inside
   * the tenant's own sandbox), but the panel must not present them as a
   * workspace file. Absent means "resolves inside the workspace".
   */
  outside_workspace?: boolean;
  /** Where the link pointed, when `outside_workspace` is set. */
  resolved_path?: string;
}

export interface GitStatus {
  branch: string;
  ahead: number;
  behind: number;
  clean: boolean;
  changes: GitChange[];
  conflicts?: string[];
  /** Totals over the change list, measured against HEAD. */
  additions?: number;
  deletions?: number;
}

export interface GitChange {
  path: string;
  index: string;
  worktree: string;
  /** Lines this path adds/removes against HEAD (untracked = additions only). */
  additions?: number;
  deletions?: number;
  /** "lockfile" or "ci" when the review must not skim this row. */
  risk?: string;
}

export interface GitCommit {
  hash: string;
  short: string;
  parents: string[];
  author: string;
  email?: string;
  ts: string;
  subject: string;
  refs?: GitRef[];
}

export interface GitRef {
  type: "branch" | "tag" | "head" | "remote" | string;
  name: string;
}

export interface GitGraph {
  commits: GitCommit[];
  head: string;
  next_cursor?: string;
}

/** One pull request this session opened (FR-12.6). */
export interface PullRequestInfo {
  number: number;
  url: string;
  title: string;
  head: string;
  base: string;
  /** True when the flow found an open PR instead of creating a second one. */
  existing?: boolean;
}

/** A file a PR would change, with the line counts git measured. */
export interface PRFile {
  path: string;
  status: "added" | "modified" | "deleted" | "renamed" | string;
  additions: number;
  deletions: number;
}

/**
 * What the dialog shows before pushing anything: the branch, its commits, the
 * files, and the title/body the server pre-filled from the session.
 */
export interface PRPreview {
  project_id: string;
  branch: string;
  base_branch: string;
  remote: boolean;
  ahead: number;
  behind: number;
  commit_count: number;
  commits: GitCommit[];
  files: PRFile[];
  additions: number;
  deletions: number;
  dirty: boolean;
  title: string;
  body: string;
  forge?: ForgeConfig | null;
  pull_request?: PullRequestInfo;
  warnings?: string[];
}

export interface CreatePRResult {
  pull_request: PullRequestInfo;
  created: boolean;
  pushed: { branch: string; commit: string; changed: boolean };
  warnings?: string[];
}

export interface FileDiff {
  path: string;
  old_path?: string;
  status: "added" | "modified" | "deleted" | "renamed" | string;
  additions: number;
  deletions: number;
  binary: boolean;
  patch: string;
}

export type DiffScope = "unstaged" | "staged" | "branch" | "commit" | "range";

export interface SearchResult {
  path: string;
  line?: number;
  text?: string;
}

// ---------------------------------------------------------------------------
// Providers, skills, MCP
// ---------------------------------------------------------------------------

export type ProviderSource = "inherited" | "custom";

export interface ModelSpec {
  id: string;
  name: string;
  context_window: number;
  max_tokens: number;
  reasoning: boolean;
  input_cost: number;
  output_cost: number;
}

export interface Provider {
  id: string;
  name: string;
  api: string;
  base_url?: string;
  models: ModelSpec[];
  source: ProviderSource;
  enabled: boolean;
  has_key: boolean;
  created_at: string;
}

/**
 * The deterministic first-run answer from GET /v1/setup.
 *
 * `needs_provider` is the server's copy of `hasUsableModel`: enabled, keyed and
 * at least one model. `shared_provider_available` says the deployment already
 * has one every account inherits. `bootstrap` (when present) is the non-secret
 * prefill from PIFORGE_BOOTSTRAP_PROVIDER — never a key.
 */
export interface SetupStatus {
  needs_provider: boolean;
  is_admin: boolean;
  shared_provider_available: boolean;
  bootstrap?: { name: string; base_url?: string; model: string };
  /** The deployment's fallback provider (`-default-provider`). */
  default_provider?: string;
  /** True when that name does not resolve for this account. */
  default_provider_missing?: boolean;
}

export interface Skill {
  id: string;
  scope: "admin" | "user";
  name: string;
  version: string;
  description: string;
  content_hash: string;
  source_url?: string;
  enabled: boolean;
  /**
   * Server-resolved: this row is the one the caller's new sessions mount under
   * its name. A private row shadows an enabled shared row with the same name;
   * a disabled private row makes the shared row effective again. Optional so
   * component fixtures (and an older server) do not have to invent it; absent
   * means "no shadowing information", which the UI treats as effective.
   */
  effective?: boolean;
  created_at: string;
}

/**
 * The skill version a session was actually mounted with (FR-14.5).
 *
 * A copy, not a join: editing the skill afterwards leaves this record alone, so
 * the session keeps saying what it used. `content_hash` is derived from the
 * mounted files, which is what distinguishes two undeclared versions.
 */
export interface SessionSkill {
  session_id: string;
  skill_id: string;
  name: string;
  version: string;
  content_hash: string;
  description?: string;
  mounted_path: string;
  recorded_at: string;
}

export interface MCPServer {
  id: string;
  scope: "admin" | "user";
  name: string;
  transport: "stdio" | "http";
  command?: string;
  args?: string[];
  cwd?: string;
  url?: string;
  exposure: "codemode" | "deferred" | "direct" | "hidden" | string;
  enabled: boolean;
  has_secrets: boolean;
  created_at: string;
}

/**
 * One server the deployment cannot hand to the agent (docs/EXECUTION-BOUNDARY.md
 * §10.1 item 8). `code` is what a client switches on; `reason` is the sentence a
 * person reads. Both come from the server, because the rule ("is this server
 * stageable here?") lives in one place and the panel only renders it.
 */
export interface MCPOmission {
  code: string;
  reason: string;
}

/**
 * The MCP panel's answer: what is configured, and what this deployment can
 * actually stage. `agent_isolated` is the placement; `unavailable` is keyed by
 * server name, matching the checkout's mount manifest.
 */
export interface MCPServersResponse {
  servers: MCPServer[];
  agent_isolated?: boolean;
  unavailable?: Record<string, MCPOmission>;
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

export interface ApiErrorBody {
  error: {
    code: string;
    message: string;
    details?: Record<string, unknown>;
    request_id?: string;
  };
}

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly details?: Record<string, unknown>;

  constructor(status: number, body: ApiErrorBody) {
    super(body.error?.message ?? `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = body.error?.code ?? "internal";
    this.details = body.error?.details;
  }

  /** True when the client should drop its event cursor and refetch. */
  get needsResync(): boolean {
    return this.code === "need_full_resync";
  }
}
/** The admin status page's counts (PRD FR-16.5). */
export interface DeploymentStatus {
  users: number;
  users_active: number;
  projects: number;
  sessions: number;
  sessions_live: number;
  agents_running: number;
  /** Turns in flight right now — the runtime's own running/streaming state. */
  runs_running: number;
  /** Agent processes alive, warm-but-idle included (same value as agents_running). */
  agents_live?: number;
  sandboxes: number;
  sandboxes_up: number;
  providers: number;
  skills: number;
  mcp_servers: number;
  events: number;
  /** The effective FR-3.3 cap (from -parallel-limit / PIFORGE_PARALLEL_LIMIT). */
  parallel_limit: number;
  /** The largest value the cap may be configured to. */
  parallel_limit_max: number;
  /** Sandbox browsers alive right now; zero unless a panel is open or idle. */
  browsers_running: number;
  /** The sandboxes those browsers belong to. */
  browser_sandboxes: string[];
  /** The effective idle policy, e.g. "1m0s". */
  browser_idle_after: string;
  /**
   * One row per directory the server creates under its data directory, with
   * direct-entry count and recursive bytes. This is the runtime directory
   * lifecycle made visible: growth shows up here before it fills the volume.
   */
  storage: StorageUsage[];
  /** Sum of every row's bytes. */
  storage_total_bytes: number;
  /**
   * One row per sandbox: its measured checkout and the budget it is measured
   * against (FR-5.8). "Who is close" is not answerable from the directory block,
   * which reports the whole sandboxes root as one number.
   */
  sandbox_usage: SandboxUsage[];
  /** The effective per-sandbox disk budget in bytes; 0 means enforcement is off. */
  sandbox_disk_quota: number;
  /**
   * The per-tenant quota policy: the default every tenant gets, where it came
   * from, how sandbox networks are decided, and one row per account with the
   * limit it actually runs under and how many sandboxes it holds.
   */
  tenant_limits: TenantLimitsReport;
}

/** The tenant quota policy as the admin screen reads it (GET /v1/admin/status). */
export interface TenantLimitsReport {
  /** The default CPU limit (docker --cpus); empty means unlimited. */
  cpus?: string;
  /** The default memory limit ("2g"); empty means unlimited. */
  memory?: string;
  /** The default process limit; 0 means unlimited. */
  pids?: number;
  /** How many sandboxes one tenant may hold at once; 0 means unlimited. */
  max_sandboxes: number;
  /** Where the policy came from: "flags", a file path, or "none". */
  source: string;
  /** "shared" (one bridge for every sandbox) or "tenant" (a bridge per tenant). */
  network_mode: string;
  /** False when the deployment configured no limits at all. */
  enforced: boolean;
  /**
   * The default outbound-network mode: "none" (no internet; the control plane's
   * gateway stays reachable), "open", or "allowlist" (modelled, refused at boot
   * because Docker cannot filter a bridge by hostname).
   */
  egress_mode: string;
  /** Where the egress policy came from: a file path, "flags", "network-mode" or "none". */
  egress_source: string;
  /** False when the deployment configured no egress policy at all — that is open. */
  egress_configured: boolean;
  /** False for -sandbox local, where there is no bridge and the mode is no boundary. */
  egress_applied: boolean;
  tenants: TenantLimitRow[];
}

/** One tenant's effective limits, next to what it is using. */
export interface TenantLimitRow {
  user_id: string;
  email?: string;
  cpus?: string;
  memory?: string;
  pids?: number;
  max_sandboxes: number;
  /** Sandboxes this tenant holds right now, which is what the cap applies to. */
  sandboxes: number;
  /** False when both the container limits and the cap are unlimited. */
  effective: boolean;
  /** True when the policy names this tenant explicitly. */
  override: boolean;
  /** This tenant's effective outbound mode: "none" or "open". */
  egress?: string;
  /** True when the egress policy names this tenant explicitly. */
  egress_override?: boolean;
}

/**
 * One tool call in the operator's forensic audit trail (GET /v1/admin/audit).
 *
 * It is not the session's Trace: the row survives the session, project and user
 * it describes, and it carries the model and workspace revision the call ran
 * against, the run's token/cost facts, and a bounded redacted preview. The full
 * arguments are never here (docs/AUDIT.md).
 */
export interface AuditRow {
  id: string;
  /** Monotonic per session; ordering reconstructs the recorded order. */
  seq: number;
  started_at: string;
  ended_at?: string;
  duration_ms?: number;
  user_id: string;
  user_email?: string;
  project_id: string;
  project?: string;
  session_id: string;
  session?: string;
  sandbox_id?: string;
  run_id?: string;
  tool: string;
  namespace?: string;
  args_hash: string;
  /** Bounded, redacted JSON object — never the raw arguments. */
  args_preview?: unknown;
  preview_bytes: number;
  preview_truncated?: boolean;
  /** running | ok | error | abandoned */
  status: string;
  /** Absent while the call is still running. */
  failed?: boolean;
  exit_code?: number;
  result_bytes?: number;
  model_ref?: string;
  commit_sha?: string;
  /** Run-level facts joined from audit_runs; zero means not reported. */
  run_model_ref?: string;
  run_tokens_in: number;
  run_tokens_out: number;
  run_tokens_cache_read: number;
  run_tokens_cache_write: number;
  run_cost_usd: number;
  prev_hash?: string;
  hash: string;
}

/** One page of the audit trail, plus how many rows the filter matched. */
export interface AuditResponse {
  calls: AuditRow[];
  total: number;
  limit: number;
  offset: number;
  /** The filters the server understood, echoed back. */
  filters: Record<string, string>;
}

/**
 * The answer to "is this range of the trail intact" (GET /v1/admin/audit/verify).
 *
 * `ok` covers the visited rows only. An `anchor` says the session's visible
 * range starts mid-chain because retention removed a prefix (or a filter chose
 * the range), which is not a break. See docs/AUDIT.md for the limits: the chain
 * detects an edit or a deletion, not a writer who recomputes every hash.
 */
export interface AuditVerifyResult {
  ok: boolean;
  checked: number;
  sessions: number;
  first_break?: {
    session_id: string;
    id: string;
    tool_call_id: string;
    seq: number;
    reason: "edited" | "gap" | "link" | "head";
    detail: string;
    expected?: string;
    actual?: string;
  };
  anchors?: Array<{
    session_id: string;
    seq: number;
    prev_hash: string;
    reason: "retention" | "range" | "genesis";
  }>;
}

/** One sandbox's footprint against the disk budget (GET /v1/admin/status). */
export interface SandboxUsage {
  sandbox_id: string;
  user_id: string;
  project_id: string;
  /** Recursive bytes below the sandbox root. */
  bytes: number;
  files: number;
  /** The budget this was measured against; 0 when enforcement is disabled. */
  quota_bytes: number;
  /** bytes > quota_bytes (a positive quota). */
  over: boolean;
  /** The walk hit its file cap; `bytes` is a lower bound. */
  truncated?: boolean;
  measured_at: string;
  /**
   * The workspace could not be measured (a sandbox that did not answer). `bytes`
   * is then meaningless and `over` is false because nothing was proven, so the
   * page says "未知" instead of showing a zero an operator would trust.
   */
  unmeasurable?: boolean;
  /** Why the measurement failed, for an operator. No secret. */
  error?: string;
}

/** One runtime directory's footprint (see backend/internal/runtimedirs). */
export interface StorageUsage {
  name: string;
  /** Direct children (sessions, projects, skills). */
  entries: number;
  /** Regular files below the directory. */
  files: number;
  /** Their total size. */
  bytes: number;
  /** The walk hit its cap; `files`/`bytes` are lower bounds. */
  truncated?: boolean;
}
/**
 * One recorded admin read of a session (PRD FR-16.8). The owner sees these; they
 * are receipts, not an audit log, and `seen_at` is what clears the badge.
 */
export interface SupportNotice {
  id: string;
  session_id: string;
  owner_id: string;
  admin_id: string;
  reason: string;
  created_at: string;
  expires_at: string;
  seen_at?: string;
  session_title?: string;
  admin_email?: string;
}
/** A bearer token a user created for scripts (PRD FR-13.9). */
export interface APIKey {
  id: string;
  user_id: string;
  name: string;
  /** The readable head of the token, so two keys can be told apart. */
  prefix: string;
  created_at: string;
  last_used_at?: string;
  revoked: boolean;
  revoked_at?: string;
}
/** One page of the sandbox browser (PRD FR-11.7). */
export interface BrowserTab {
  id: string;
  url: string;
  title: string;
}
/** One column of the branch comparison screen (PRD FR-2.5). */
export interface CompareRow {
  session_id: string;
  title: string;
  kind: string;
  status: string;
  model_ref?: string;
  last_active_at: string;
  user_messages: number;
  tool_calls: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  /** The part of the usage numbers that came from the cloned/forked conversation. */
  inherited_user_messages: number;
  inherited_tool_calls: number;
  inherited_tokens_in: number;
  inherited_tokens_out: number;
  inherited_cost_usd: number;
  files_changed: number;
  additions: number;
  deletions: number;
  /** Committed since the merge base (`git diff <trunk>...<branch>`). */
  committed_files: number;
  committed_additions: number;
  committed_deletions: number;
  /** Staged, unstaged and untracked in the branch's working tree. */
  uncommitted_files: number;
  uncommitted_additions: number;
  uncommitted_deletions: number;
  /** Present when the change size could not be measured, instead of a fake zero. */
  diff_error?: string;
}
/**
 * One failing test (PRD FR-12.5). `file`/`line` are the assertion site when the
 * runner printed one; the panel opens that file in the editor when clicked.
 */
export interface TestFailure {
  name: string;
  file?: string;
  line?: number;
  message?: string;
}
/**
 * One test command's outcome. `unrecognized` is a first-class status: the
 * command was a recognised runner's, but its output could not be read, so
 * `output_tail` carries the raw tail and the counts are zero.
 */
export interface TestRun {
  id: string;
  session_id: string;
  run_id?: string;
  /** agent = a tool call; terminal = a command typed in the PTY. */
  source: "agent" | "terminal";
  runner: string;
  command: string;
  status: "running" | "passed" | "failed" | "unrecognized";
  total: number;
  passed: number;
  failed: number;
  skipped: number;
  duration_ms: number;
  failures: TestFailure[];
  output_tail?: string;
  started_at: string;
  ended_at?: string;
}
export interface TestRunsResponse {
  runs: TestRun[];
  count: number;
}