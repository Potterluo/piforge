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
  type DiffScope,
  type FileContent,
  type FileDiff,
  type FileNode,
  type ForkPoint,
  type GitGraph,
  type GitStatus,
  type InterruptResult,
  type MCPServer,
  type Project,
  type DeploymentStatus,
  type SupportNotice,
  type PromptResult,
  type Provider,
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

export const projects = {
  list: () => request<{ projects: Project[] }>("/v1/projects"),
  get: (id: string) => request<{ project: Project }>(`/v1/projects/${id}`),
  create: (input: CreateProjectInput) =>
    request<{ project: Project }>("/v1/projects", { method: "POST", body: input }),
  update: (id: string, input: UpdateProjectInput) =>
    request<{ project: Project }>(`/v1/projects/${id}`, { method: "PATCH", body: input }),
  remove: (id: string) => request<{ ok: boolean }>(`/v1/projects/${id}`, { method: "DELETE" }),
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
}

export const browser = {
  mode: (sessionId: string) =>
    request<{ mode: string }>(`/v1/sessions/${sessionId}/browser/mode`),
  setMode: (sessionId: string, mode: "observe" | "takeover") =>
    request<{ mode: string }>(`/v1/sessions/${sessionId}/browser/mode`, {
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
  skills: (id: string) => request<{ skills: SessionSkill[] }>(`/v1/sessions/${id}/skills`),
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

export const files = {
  tree: (sessionId: string, path = "", depth = 2, includeIgnored = false) =>
    request<{ nodes: FileNode[] }>(
      `/v1/sessions/${sessionId}/files/tree?path=${encodeURIComponent(path)}&depth=${depth}` +
        (includeIgnored ? "&include_ignored=1" : ""),
    ),
  read: (sessionId: string, path: string) =>
    request<FileContent>(`/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`),
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
    `${BASE}/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}&download=1`,
  /** Same bytes, but the browser is allowed to render them (used for PDFs). */
  inlineUrl: (sessionId: string, path: string) =>
    `${BASE}/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}&inline=1`,
};

// ---------------------------------------------------------------------------
// Workspace: git
// ---------------------------------------------------------------------------

export interface DiffResponse {
  diffs: FileDiff[];
}

export const git = {
  status: (sessionId: string) => request<GitStatus>(`/v1/sessions/${sessionId}/git/status`),
  diff: (sessionId: string, scope: DiffScope = "unstaged", path = "") => {
    const q = new URLSearchParams({ scope });
    if (path) q.set("path", path);
    return request<DiffResponse>(`/v1/sessions/${sessionId}/git/diff?${q.toString()}`);
  },
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
  sandbox: (sessionId: string) =>
    request<{ id: string; state: string; message?: string }>(`/v1/sessions/${sessionId}/sandbox`),
  pauseSandbox: (sessionId: string) =>
    request<{ state: string }>(`/v1/sessions/${sessionId}/sandbox/pause`, { method: "POST" }),
  resumeSandbox: (sessionId: string) =>
    request<{ state: string }>(`/v1/sessions/${sessionId}/sandbox/resume`, { method: "POST" }),
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
  list: () => request<{ providers: Provider[] }>("/v1/providers"),
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

export const skills = {
  import: (url: string, ref: string) =>
    request<{
      source: string;
      skills: { name: string; path: string; files: number; skipped: boolean; reason?: string }[];
    }>("/v1/skills/import", { method: "POST", body: { url, ref } }),
  list: () => request<{ skills: Skill[] }>("/v1/skills"),
  create: (input: { name: string; description: string; body?: string }) =>
    request<{ skill: Skill }>("/v1/skills", { method: "POST", body: input }),
  remove: (id: string) => request<{ ok: boolean }>(`/v1/skills/${id}`, { method: "DELETE" }),
  /** Replaces a skill's content wholesale; the server requires SKILL.md. */
  uploadBundle: (id: string, files: SkillFile[]) =>
    request<{ skill: Skill }>(`/v1/skills/${id}/bundle`, { method: "PUT", body: { files } }),
  bundle: (id: string) => request<{ files: SkillFile[] }>(`/v1/skills/${id}/bundle`),
};

export const mcp = {
  list: () => request<{ servers: MCPServer[] }>("/v1/mcp/servers"),
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
};

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
