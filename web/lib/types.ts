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
  | "archived";

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
 * The normalized event envelope. `seq` is monotonic per session and doubles as
 * the SSE event id, so a reconnecting client replays from where it stopped.
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

/** Payload of context.compaction. */
export interface CompactionPayload {
  reason: "manual" | "threshold" | "overflow";
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
}

export interface PromptResult {
  accepted: boolean;
  disposition: "started" | "queued" | "handled";
  queue_position: number;
  run_id: string;
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
}

export interface GitStatus {
  branch: string;
  ahead: number;
  behind: number;
  clean: boolean;
  changes: GitChange[];
  conflicts?: string[];
}

export interface GitChange {
  path: string;
  index: string;
  worktree: string;
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

export interface FileDiff {
  path: string;
  old_path?: string;
  status: "added" | "modified" | "deleted" | "renamed" | string;
  additions: number;
  deletions: number;
  binary: boolean;
  patch: string;
}

export type DiffScope = "unstaged" | "staged" | "branch" | "commit";

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

export interface Skill {
  id: string;
  scope: "admin" | "user";
  name: string;
  version: string;
  description: string;
  content_hash: string;
  source_url?: string;
  enabled: boolean;
  created_at: string;
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
  sandboxes: number;
  sandboxes_up: number;
  providers: number;
  skills: number;
  mcp_servers: number;
  events: number;
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
  files_changed: number;
  additions: number;
  deletions: number;
  /** Present when the change size could not be measured, instead of a fake zero. */
  diff_error?: string;
}