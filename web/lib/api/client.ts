/**
 * The single HTTP client for the PiForge API.
 *
 * Both the application shell and the workspace panels import from here, so the
 * request shapes stay in one place and a backend change breaks in exactly one
 * file. Every function returns the wire types from `@/lib/types`.
 */
import {
  ApiError,
  type ApiErrorBody,
  type AuditResponse,
  type DiffScope,
  type FileContent,
  type FileDiff,
  type FileNode,
  type ForkPoint,
  type GitGraph,
  type GitStatus,
  type PRPreview,
  type CreatePRResult,
  type InterruptResult,
  type MCPServer,
  type MCPServersResponse,
  type Project,
  type DeploymentStatus,
  type SupportNotice,
  type PromptResult,
  type Provider,
  type RecoveryView,
  type RuntimeStatus,
  type SearchResult,
  type Session,
  type SessionSkill,
  type SessionStats,
  type SetupStatus,
  type Skill,
  type TestRunsResponse,
  type TreeEntry,
  type APIKey,
  type BrowserTab,
  type CompareRow,
  type User,
} from "@/lib/types";
import { diffQuery, type DiffRange } from "@/lib/git/diff-scope";

const BASE = "";

let csrfToken: string | null = null;

/** Reads the non-HttpOnly CSRF cookie the server sets at login. */
export function readCsrfToken(): string | null {
  if (typeof document === "undefined") return csrfToken;
  const match = document.cookie.match(/(?:^|;\s*)pf_csrf=([^;]+)/);
  csrfToken = match ? decodeURIComponent(match[1]) : null;
  return csrfToken;
}

function setCsrfFromResponse(res: Response): void {
  const token = res.headers.get("x-pf-csrf");
  if (token) csrfToken = token;
}

async function toError(res: Response): Promise<ApiError> {
  let body: ApiErrorBody;
  try {
    body = (await res.json()) as ApiErrorBody;
  } catch {
    body = { error: { code: "internal", message: `HTTP ${res.status}` } };
  }
  return new ApiError(res.status, body);
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  headers?: Record<string, string>;
  raw?: BodyInit;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...(opts.headers ?? {}),
  };

  let body: BodyInit | undefined;
  if (opts.raw !== undefined) {
    body = opts.raw;
  } else if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }

  // Double-submit CSRF: only state-changing verbs need the header.
  if (method !== "GET" && method !== "HEAD") {
    const token = readCsrfToken();
    if (token) headers["X-PF-CSRF"] = token;
  }

  const res = await fetch(`${BASE}${path}`, {
    method,
    headers,
    body,
    credentials: "same-origin",
    signal: opts.signal,
  });

  setCsrfFromResponse(res);

  if (!res.ok) {
    const error = await toError(res);
    // A missing model is a configuration state, not a one-off toast. The
    // workbench listens for this and keeps the refusal on screen above the
    // composer, next to the message that did not send.
    if (typeof window !== "undefined" && error.code === "no_model") {
      window.dispatchEvent(new CustomEvent("piforge:no-model", { detail: error }));
    }
    throw error;
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

export interface LoginResult {
  user: User;
  csrf: string;
}

export const auth = {
  login: (email: string, password: string) =>
    request<LoginResult>("/v1/auth/login", { method: "POST", body: { email, password } }),
  logout: () => request<{ ok: boolean }>("/v1/auth/logout", { method: "POST" }),
  me: () => request<{ user: User }>("/v1/auth/me"),
};

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

export interface CreateProjectInput {
  /** Omitted or empty derives the name on the server (repository basename, then -2). */
  name?: string;
  repo_url?: string;
  default_branch?: string;
}

export interface UpdateProjectInput {
  name?: string;
  repo_url?: string;
  default_branch?: string;
}

/**
 * Where this project opens pull requests (FR-12.6).
 *
 * `token` is write-only: omit it to keep the saved one (the form cannot read it
 * back), send "" to clear it. It is never returned by any endpoint.
 */
export interface ForgeConfigInput {
  kind: "github" | "gitlab";
  api_base: string;
  repo: string;
  base_branch?: string;
  token?: string;
}

export const projects = {
  list: () => request<{ projects: Project[] }>("/v1/projects"),
  get: (id: string) => request<{ project: Project }>(`/v1/projects/${id}`),
  create: (input: CreateProjectInput) =>
    request<{ project: Project }>("/v1/projects", { method: "POST", body: input }),
  update: (id: string, input: UpdateProjectInput) =>
    request<{ project: Project }>(`/v1/projects/${id}`, { method: "PATCH", body: input }),
  remove: (id: string) => request<{ ok: boolean }>(`/v1/projects/${id}`, { method: "DELETE" }),
  setForge: (id: string, input: ForgeConfigInput) =>
    request<{ project: Project }>(`/v1/projects/${id}/forge`, { method: "PUT", body: input }),
};

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

export interface CreateSessionInput {
  title?: string;
  model_ref?: string;
  thinking_level?: string;
  kind?: "main";
  /** Scratch session (FR-1.10): hidden from the list, swept after a few hours. */
  ephemeral?: boolean;
}

export interface SendMessageInput {
  client_msg_id: string;
  text: string;
  images?: unknown[];
  /** Required while a run is in flight: "steer" interrupts, "follow_up" queues. */
  streaming_behavior?: "steer" | "follow_up";
  /**
   * FR-12.3: a comment on one line. The server composes the prompt from the
   * anchor, the quote and the body, so the message the agent reads, the one the
   * transcript shows and the one stored are the same string.
   */
  review_comment?: {
    path: string;
    line: number;
    end_line?: number;
    side?: "new" | "old";
    hunk?: string;
    quote?: string[];
    body: string;
  };
  /**
   * The 继续 action for an interrupted run. The server composes the text from
   * the event log and the checkout, so `text` must be empty; the transcript
   * then shows exactly what the agent received.
   */
  continue_interrupted?: boolean;
}

/**
 * Who is driving the sandbox browser (FR-11.3).
 *
 * `holder` is present whenever somebody — possibly the caller — holds the
 * takeover lease. `owned_by_me` is what lets two tabs of one session agree
 * instead of each guessing from its own click.
 */
export interface BrowserMode {
  mode: "observe" | "takeover";
  /** How long a takeover survives without a heartbeat. */
  ttl_seconds?: number;
  holder?: {
    user_id: string;
    user_name: string;
    session_id: string;
    session_label?: string;
    owned_by_me: boolean;
    remaining_seconds: number;
    expires_at: string;
  };
}

export const browser = {
  mode: (sessionId: string) => request<BrowserMode>(`/v1/sessions/${sessionId}/browser/mode`),
  setMode: (sessionId: string, mode: "observe" | "takeover") =>
    request<BrowserMode>(`/v1/sessions/${sessionId}/browser/mode`, {
      method: "POST",
      body: { mode },
    }),
};

export const sessions = {
  list: (params: { project?: string; q?: string; limit?: number } = {}) => {
    const query = new URLSearchParams();
    if (params.project) query.set("project", params.project);
    if (params.q) query.set("q", params.q);
    if (params.limit) query.set("limit", String(params.limit));
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return request<{ sessions: Session[] }>(`/v1/sessions${suffix}`);
  },
  get: (id: string) =>
    request<{ session: Session; status?: RuntimeStatus; project?: Project }>(`/v1/sessions/${id}`),
  /** Two or three branches side by side (FR-2.5). */
  compare: (ids: string[]) =>
    request<{ rows: CompareRow[] }>(`/v1/sessions/compare?ids=${ids.join(",")}`),
  create: (projectId: string, input: CreateSessionInput) =>
    request<{ session: Session }>(`/v1/projects/${projectId}/sessions`, {
      method: "POST",
      body: input,
    }),
  update: (id: string, patch: Partial<Pick<Session, "title" | "model_ref" | "status">>) =>
    request<{ session: Session }>(`/v1/sessions/${id}`, { method: "PATCH", body: patch }),
  send: (id: string, input: SendMessageInput) =>
    request<PromptResult>(`/v1/sessions/${id}/messages`, { method: "POST", body: input }),
  /**
   * The sandbox behind this session: its state, and the liveness watchdog's last
   * verdict (backend/internal/httpapi/sandbox_watch.go).
   *
   * `responsive: null` means this control plane has not probed the sandbox yet —
   * unknown is not down, and the banner treats it as absent. These four methods
   * live here rather than in `git` because the routes are session-scoped
   * (`/v1/sessions/{id}/sandbox…`).
   */
  sandbox: (sessionId: string) =>
    request<{
      id: string;
      state: string;
      message?: string;
      responsive?: boolean | null;
      recovering?: boolean;
      can_recover?: boolean;
      probe?: {
        ok: boolean;
        error?: string;
        at?: string;
        recovered_at?: string;
        restarts?: number;
      };
    }>(`/v1/sessions/${sessionId}/sandbox`),
  pauseSandbox: (sessionId: string) =>
    request<{ state: string }>(`/v1/sessions/${sessionId}/sandbox/pause`, { method: "POST" }),
  resumeSandbox: (sessionId: string) =>
    request<{ state: string }>(`/v1/sessions/${sessionId}/sandbox/resume`, { method: "POST" }),
  /**
   * Stop and start the sandbox, keeping its volume.
   *
   * The one-click half of 「沙箱无响应，正在恢复」: a fork storm can exhaust the
   * sandbox's process budget so completely that it cannot fork — the container
   * is "running", nothing inside it works, and only a restart brings it back.
   * The server probes first, so pressing this on a healthy sandbox is a no-op
   * that says so instead of throwing its /tmp away.
   */
  recoverSandbox: (sessionId: string) =>
    request<{
      state: string;
      recovered: boolean;
      restarted: boolean;
      recovering?: boolean;
      elapsed_ms?: number;
      message?: string;
    }>(`/v1/sessions/${sessionId}/sandbox/recover`, { method: "POST" }),
  remove: (id: string) => request<{ ok: boolean }>(`/v1/sessions/${id}`, { method: "DELETE" }),
  /** Carries a branch's work back to the session it came from (FR-2.6). */
  adopt: (id: string, message?: string) =>
    request<{
      adopted: {
        commit: string;
        files: string[];
        /** merge | fast-forward | working-tree, plus a "+working-tree" suffix. */
        mode: string;
        commits: string[];
        merge_commit?: string;
        branch_commit?: string;
      };
      branch: string;
      target: string;
    }>(`/v1/sessions/${id}/adopt`, {
      method: "POST",
      body: { message: message ?? "" },
    }),
  /** Copies a session into a parallel attempt with its own checkout (FR-2.7). */
  clone: (id: string) =>
    request<{ session: Session }>(`/v1/sessions/${id}/clone`, { method: "POST" }),
  /** Turns a side conversation into a branch with its own checkout (FR-4.7). */
  promote: (id: string) =>
    request<{ session: Session }>(`/v1/sessions/${id}/promote`, { method: "POST" }),
  /** The pages open in the sandbox browser (FR-11.7). */
  tabs: (id: string) =>
    request<{ tabs: BrowserTab[]; default_tab: string }>(`/v1/sessions/${id}/browser/tabs`),
  openTab: (id: string, url?: string) =>
    request<{ tab: BrowserTab }>(`/v1/sessions/${id}/browser/tabs`, {
      method: "POST",
      body: { url: url ?? "" },
    }),
  closeTab: (id: string, tabId: string) =>
    request<{ ok: boolean }>(`/v1/sessions/${id}/browser/tabs/${tabId}`, { method: "DELETE" }),
  /**
   * Captures the page the panel is showing into the checkout, for the agent to
   * read. The tab is passed explicitly: with several pages open, "the page in
   * the panel" is a fact only the viewer has.
   */
  screenshot: (id: string, tabId?: string) =>
    request<{ path: string; bytes: number }>(
      `/v1/sessions/${id}/browser/screenshot${tabId ? `?tab=${encodeURIComponent(tabId)}` : ""}`,
      { method: "POST" },
    ),
  archive: (id: string, archived: boolean) =>
    request<{ session: Session }>(`/v1/sessions/${id}`, {
      method: "PATCH",
      body: { status: archived ? "archived" : "active" },
    }),
  /** The download itself, not a fetch: the browser owns the file dialog. */
  exportUrl: (id: string, format: "md" | "html" = "md") =>
    `/v1/sessions/${id}/export?format=${format}`,
  interrupt: (id: string, clearQueue = true) =>
    request<InterruptResult>(`/v1/sessions/${id}/interrupt`, {
      method: "POST",
      body: { clear_queue: clearQueue },
    }),
  /**
   * What happened and what survived after a run ended without a result. The
   * answer also says whether 继续 is possible; the UI must render the block
   * reason rather than a button that would fail.
   */
  recovery: (id: string) => request<RecoveryView>(`/v1/sessions/${id}/recovery`),
  fork: (id: string, entryId: string, label: string) =>
    request<{ session: Session }>(`/v1/sessions/${id}/fork`, {
      method: "POST",
      body: { entry_id: entryId, label },
    }),
  sidecar: (id: string, modelRef?: string) =>
    request<{ session: Session }>(`/v1/sessions/${id}/sidecar`, {
      method: "POST",
      body: { model_ref: modelRef },
    }),
  /**
   * Asks the agent to compact its context.
   *
   * `compacted:false` is an answer, not a failure: a session with nothing to
   * compact says so (`reason`) instead of erroring (FR-1.9).
   */
  compact: (id: string, instructions = "") =>
    request<{ compacted: boolean; reason?: string }>(`/v1/sessions/${id}/compact`, {
      method: "POST",
      body: { custom_instructions: instructions },
    }),
  entries: (id: string, since = "") =>
    request<{ entries: TreeEntry[]; leaf_id: string }>(
      `/v1/sessions/${id}/entries${since ? `?since=${encodeURIComponent(since)}` : ""}`,
    ),
  tree: (id: string) => request<{ tree: TreeEntry[]; leaf_id: string }>(`/v1/sessions/${id}/tree`),
  forkPoints: (id: string) => request<{ points: ForkPoint[] }>(`/v1/sessions/${id}/fork-points`),
  /** The agent's stderr tail, for explaining a run that died quietly (FR-5.10). */
  logs: (id: string) =>
    request<{ logs: { session_id: string; lines: string[]; truncated: boolean }; reason?: string }>(
      `/v1/sessions/${id}/logs`,
    ),
  stats: (id: string) => request<SessionStats>(`/v1/sessions/${id}/stats`),
  status: (id: string) => request<RuntimeStatus>(`/v1/sessions/${id}/status`),
  /** The skill versions this session was mounted with (FR-14.5). */
  skills: (id: string) =>
    request<{ skills: SessionSkill[]; mechanisms?: Record<string, string> }>(
      `/v1/sessions/${id}/skills`,
    ),
  /** Test runs derived from this session's tool events and terminal (FR-12.5). */
  testRuns: (id: string, limit = 20) =>
    request<TestRunsResponse>(`/v1/sessions/${id}/test-runs?limit=${limit}`),
};

/** URL of the SSE stream. `EventSource` handles `Last-Event-ID` reconnects. */
export function eventsUrl(sessionId: string, afterSeq = 0): string {
  const q = afterSeq > 0 ? `?after_seq=${afterSeq}` : "";
  return `${BASE}/v1/sessions/${sessionId}/events${q}`;
}

// ---------------------------------------------------------------------------
// Workspace: files
// ---------------------------------------------------------------------------

/**
 * A screenshot now lives outside the checkout, at a virtual path:
 * `artifacts/<session>/screenshots/<name>.png`. The panel keeps passing plain
 * path strings around, so the three read functions recognise that prefix and
 * use the artifacts route. Every other path — including `screenshots/…` written
 * by an older build inside the checkout — still goes to /files.
 */
const ARTIFACT_ROOT = "artifacts/";

function artifactRel(sessionId: string, path: string): string | null {
  const prefix = `${ARTIFACT_ROOT}${sessionId}/`;
  return path.startsWith(prefix) ? path.slice(prefix.length) : null;
}

function fileReadUrl(sessionId: string, path: string, flag?: "download" | "inline"): string {
  const artifact = artifactRel(sessionId, path);
  const base = artifact
    ? `/v1/sessions/${sessionId}/artifacts?path=${encodeURIComponent(artifact)}`
    : `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`;
  return flag ? `${base}&${flag}=1` : base;
}

export const files = {
  tree: (sessionId: string, path = "", depth = 2, includeIgnored = false) =>
    request<{ nodes: FileNode[] }>(
      `/v1/sessions/${sessionId}/files/tree?path=${encodeURIComponent(path)}&depth=${depth}` +
        (includeIgnored ? "&include_ignored=1" : ""),
    ),
  read: (sessionId: string, path: string) => request<FileContent>(fileReadUrl(sessionId, path)),
  write: (sessionId: string, path: string, body: string) =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`, {
      method: "PUT",
      raw: body,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    }),
  remove: (sessionId: string, path: string) =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`, {
      method: "DELETE",
    }),
  search: (sessionId: string, query: string, byContent = true) =>
    request<{ results: SearchResult[] }>(
      `/v1/sessions/${sessionId}/search?q=${encodeURIComponent(query)}&type=${byContent ? "content" : "name"}`,
    ),
  downloadUrl: (sessionId: string, path: string) =>
    `${BASE}${fileReadUrl(sessionId, path, "download")}`,
  /** Same bytes, but the browser is allowed to render them (used for PDFs). */
  inlineUrl: (sessionId: string, path: string) =>
    `${BASE}${fileReadUrl(sessionId, path, "inline")}`,
};

// ---------------------------------------------------------------------------
// Workspace: git
// ---------------------------------------------------------------------------

export interface DiffResponse {
  diffs: FileDiff[];
}

export const git = {
  status: (sessionId: string) => request<GitStatus>(`/v1/sessions/${sessionId}/git/status`),
  diff: (sessionId: string, scope: DiffScope = "unstaged", path = "", range?: DiffRange) =>
    request<DiffResponse>(
      `/v1/sessions/${sessionId}/git/diff?${diffQuery(scope, path, range).toString()}`,
    ),
  log: (sessionId: string, limit = 200) =>
    request<GitGraph>(`/v1/sessions/${sessionId}/git/log?limit=${limit}`),
  hunk: (sessionId: string, path: string, index: number, action: "stage" | "unstage" | "discard") =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/git/hunk`, {
      method: "POST",
      body: { path, index, action },
    }),
  discardFile: (sessionId: string, path: string) =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/git/discard`, {
      method: "POST",
      body: { path },
    }),
  remote: (sessionId: string) =>
    request<{ remote: boolean; ahead: number; behind: number }>(
      `/v1/sessions/${sessionId}/git/remote`,
    ),
  push: (sessionId: string) =>
    request<{ branch: string; commit: string }>(`/v1/sessions/${sessionId}/git/push`, {
      method: "POST",
    }),
  pull: (sessionId: string) =>
    request<{ branch: string; commit: string; changed: boolean }>(
      `/v1/sessions/${sessionId}/git/pull`,
      { method: "POST" },
    ),
  /**
   * What a pull request would contain (FR-12.6). Read-only: nothing is pushed
   * and no PR is opened. The response also says whether the forge already has
   * one for this branch.
   */
  prPreview: (sessionId: string) =>
    request<PRPreview>(`/v1/sessions/${sessionId}/git/pr`),
  /** Push the branch, then open (or find) the pull request. */
  createPR: (sessionId: string, input: { title: string; body: string; base_branch?: string }) =>
    request<CreatePRResult>(`/v1/sessions/${sessionId}/git/pr`, {
      method: "POST",
      body: input,
    }),
  stage: (sessionId: string, paths: string[]) =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/git/stage`, {
      method: "POST",
      body: { paths },
    }),
  unstage: (sessionId: string, paths: string[]) =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/git/unstage`, {
      method: "POST",
      body: { paths },
    }),
  commit: (sessionId: string, message: string, paths: string[] = [], amend = false) =>
    request<{ commit: string }>(`/v1/sessions/${sessionId}/git/commit`, {
      method: "POST",
      body: { message, paths, amend },
    }),
  createBranch: (sessionId: string, name: string) =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/git/branch`, {
      method: "POST",
      body: { name },
    }),
  checkout: (sessionId: string, name: string) =>
    request<{ ok: boolean }>(`/v1/sessions/${sessionId}/git/checkout`, {
      method: "POST",
      body: { name },
    }),
};

// ---------------------------------------------------------------------------
// Providers, skills, MCP
// ---------------------------------------------------------------------------

export interface CreateProviderInput {
  name: string;
  api: string;
  base_url?: string;
  api_key?: string;
  models?: { id: string; name?: string; context_window?: number }[];
}

/**
 * A patch, not a replacement: the caller only sends what changed, and an absent
 * `api_key` means "keep the stored one" so a model-list edit never asks the user
 * to paste their key again.
 */
export interface UpdateProviderInput {
  api?: string;
  base_url?: string;
  models?: { id: string; name?: string; context_window?: number }[];
  api_key?: string;
  clear_key?: boolean;
}

export const providers = {
  /**
   * `default_model_ref` is what 「默认模型」 resolves to for this account. It is
   * the server's answer, not a client-side guess from the list: a deployment
   * whose default provider lacks a key falls back to another usable provider,
   * and the picker must name the model the session will actually run on.
   */
  list: () =>
    request<{ providers: Provider[]; default_model_ref?: string }>("/v1/providers"),
  create: (input: CreateProviderInput) =>
    request<{ provider: Provider }>("/v1/providers", { method: "POST", body: input }),
  update: (id: string, input: UpdateProviderInput) =>
    request<{ provider: Provider }>(`/v1/providers/${id}`, { method: "PATCH", body: input }),
  remove: (id: string) => request<{ ok: boolean }>(`/v1/providers/${id}`, { method: "DELETE" }),
  test: (id: string) =>
    request<{ ok: boolean; message: string }>(`/v1/providers/${id}/test`, { method: "POST" }),
};

export interface SkillFile {
  /** Path inside the bundle, POSIX separators, relative to the skill root. */
  path: string;
  content: string;
}

export interface SkillImportResult {
  source: string;
  skills: {
    name: string;
    path: string;
    files: number;
    skipped: boolean;
    reason?: string;
    /** The skill's state after the call: an import re-enables a disabled skill. */
    enabled: boolean;
    /** True when the skill was disabled before this import and is enabled now. */
    reenabled?: boolean;
  }[];
}

export const skills = {
  import: (url: string, ref: string) =>
    request<SkillImportResult>("/v1/skills/import", { method: "POST", body: { url, ref } }),
  /**
   * Installs a zip of SKILL.md directories (FR-14.4).
   *
   * Multipart rather than base64-in-JSON: the browser already knows how to send a
   * `File`, and a 4 MB archive does not need to grow by a third in transit.
   */
  importZip: (file: File) => {
    const form = new FormData();
    form.append("file", file);
    return request<SkillImportResult>("/v1/skills/import/zip", { method: "POST", raw: form });
  },
  /** Turns a skill off without deleting it; a disabled skill is not mounted. */
  setEnabled: (id: string, enabled: boolean) =>
    request<{ skill: Skill }>(`/v1/skills/${id}`, { method: "PATCH", body: { enabled } }),
  list: () => request<{ skills: Skill[] }>("/v1/skills"),
  /**
   * Creates a skill. `scope: "admin"` makes it shared with every user, which
   * only an admin may ask for (FR-14.1 / FR-16.3); the server refuses anyone
   * else rather than quietly creating a private skill.
   */
  create: (input: {
    name: string;
    description: string;
    body?: string;
    scope?: "user" | "admin";
  }) => request<{ skill: Skill }>("/v1/skills", { method: "POST", body: input }),
  remove: (id: string) => request<{ ok: boolean }>(`/v1/skills/${id}`, { method: "DELETE" }),
  /** Replaces a skill's content wholesale; the server requires SKILL.md. */
  uploadBundle: (id: string, files: SkillFile[]) =>
    request<{ skill: Skill }>(`/v1/skills/${id}/bundle`, { method: "PUT", body: { files } }),
  bundle: (id: string) => request<{ files: SkillFile[] }>(`/v1/skills/${id}/bundle`),
};

export const mcp = {
  list: () => request<MCPServersResponse>("/v1/mcp/servers"),
  create: (input: Omit<MCPServer, "id" | "created_at" | "has_secrets"> & { secrets?: string }) =>
    request<{ server: MCPServer }>("/v1/mcp/servers", { method: "POST", body: input }),
  remove: (id: string) => request<{ ok: boolean }>(`/v1/mcp/servers/${id}`, { method: "DELETE" }),
};

export interface CreateUserInput {
  email: string;
  password: string;
  display_name?: string;
  role?: "user" | "admin";
}

export interface UpdateUserInput {
  role?: "user" | "admin";
  status?: "active" | "disabled";
  password?: string;
}

/** The caller's effective feature flags (PRD FR-16.7). */
export const features = () => request<{ features: Record<string, boolean> }>("/v1/features");

/** Receipts for an admin having read your conversation. */
export const support = {
  notices: () => request<{ notices: SupportNotice[]; unseen: number }>("/v1/support-notices"),
  seen: (id: string) =>
    request<{ ok: boolean }>(`/v1/support-notices/${id}/seen`, { method: "POST" }),
};

export const admin = {
  users: () => request<{ users: User[] }>("/v1/admin/users"),
  // Shared providers: one deployment-wide account everyone inherits.
  providers: () => request<{ providers: Provider[] }>("/v1/admin/providers"),
  status: () => request<DeploymentStatus>("/v1/admin/status"),
  userFeatures: (id: string) =>
    request<{ features: Record<string, boolean> }>(`/v1/admin/users/${id}/features`),
  setUserFeature: (id: string, flag: string, enabled: boolean) =>
    request<{ ok: boolean }>(`/v1/admin/users/${id}/features`, {
      method: "POST",
      body: { flag, enabled },
    }),
  // Support mode: read one session's transcript, on the record (PRD FR-16.8).
  supportAccess: (sessionId: string, reason: string) =>
    request<{ access: SupportNotice; transcript: { role: string; text: string }[] }>(
      "/v1/admin/support-access",
      { method: "POST", body: { session_id: sessionId, reason } },
    ),
  supportTranscript: (sessionId: string) =>
    request<{ transcript: { role: string; text: string }[] }>(
      `/v1/admin/support-access/${sessionId}/transcript`,
    ),
  shareProvider: (input: CreateProviderInput) =>
    request<{ provider: Provider }>("/v1/admin/providers", { method: "POST", body: input }),
  /**
   * Edits a shared provider in place. The row id and name stay the same, which is
   * what keeps every session pinned to "provider/model" working; an absent
   * `api_key` keeps the stored secret.
   */
  updateProvider: (id: string, input: UpdateProviderInput) =>
    request<{ provider: Provider }>(`/v1/admin/providers/${id}`, { method: "PATCH", body: input }),
  unshareProvider: (id: string) =>
    request<{ ok: boolean }>(`/v1/admin/providers/${id}`, { method: "DELETE" }),
  createUser: (input: CreateUserInput) =>
    request<{ users: User[] }>("/v1/admin/users", { method: "POST", body: input }),
  updateUser: (id: string, input: UpdateUserInput) =>
    request<{ users: User[] }>(`/v1/admin/users/${id}`, { method: "PATCH", body: input }),
  /**
   * One page of the audit trail: every tool call across tenants, newest first.
   *
   * The filters are the same ones the CSV export takes (`auditExportUrl`), so the
   * table on screen and the file an operator attaches to a ticket describe the
   * same query.
   */
  audit: (filter: AuditQuery = {}) =>
    request<AuditResponse>(`/v1/admin/audit?${auditQueryString(filter)}`),
};

/** The filters GET /v1/admin/audit understands. */
export interface AuditQuery {
  user_id?: string;
  project_id?: string;
  session_id?: string;
  tool?: string;
  outcome?: "ok" | "error" | "running";
  since?: string;
  until?: string;
  limit?: number;
  offset?: number;
}

/** Serialises a filter, dropping empty values so the URL stays readable. */
export function auditQueryString(filter: AuditQuery): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(filter)) {
    if (value === undefined || value === null || value === "") continue;
    params.set(key, String(value));
  }
  return params.toString();
}

/**
 * The CSV download URL for a filter.
 *
 * It is a plain link rather than a fetch: the browser streams it to disk, the
 * session cookie authenticates it, and a 200k-row export cannot be accidentally
 * held in memory by the page. The server caps one download at its page limit and
 * says so in the UI.
 */
export function auditExportUrl(filter: AuditQuery = {}): string {
  const query = auditQueryString(filter);
  return query ? `/v1/admin/audit?${query}&format=csv` : "/v1/admin/audit?format=csv";
}

// ---------------------------------------------------------------------------
// API keys (FR-13.9)
// ---------------------------------------------------------------------------

export const apiKeys = {
  list: () => request<{ api_keys: APIKey[] }>("/v1/api-keys"),
  /** The token is returned once, here; nothing can read it back later. */
  create: (name: string) =>
    request<{ api_key: APIKey; token: string }>("/v1/api-keys", {
      method: "POST",
      body: { name },
    }),
  revoke: (id: string) =>
    request<{ api_key: APIKey }>(`/v1/api-keys/${id}`, { method: "DELETE" }),
};

// ---------------------------------------------------------------------------
// First run (QA-16)
// ---------------------------------------------------------------------------

/**
 * What a signed-in account needs to send its first message.
 *
 * The provider list already says what exists; this says whether anything is
 * usable, whether the caller can fix it deployment-wide, and what a documented
 * deployment prefill offers. The home page and the composer read the same
 * answer, so they cannot disagree about "there is nothing to send to".
 */
export const setup = {
  get: () => request<SetupStatus>("/v1/setup"),
};

export const api = {
  auth,
  projects,
  sessions,
  browser,
  files,
  git,
  providers,
  skills,
  mcp,
  admin,
  support,
  features,
  apiKeys,
  setup,
};
export default api;
