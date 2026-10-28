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
  type PromptResult,
  type Provider,
  type RuntimeStatus,
  type SearchResult,
  type Session,
  type SessionStats,
  type Skill,
  type TreeEntry,
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

  if (!res.ok) throw await toError(res);
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
  name: string;
  repo_url?: string;
  default_branch?: string;
}

export const projects = {
  list: () => request<{ projects: Project[] }>("/v1/projects"),
  get: (id: string) => request<{ project: Project }>(`/v1/projects/${id}`),
  create: (input: CreateProjectInput) =>
    request<{ project: Project }>("/v1/projects", { method: "POST", body: input }),
};

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

export interface CreateSessionInput {
  title?: string;
  model_ref?: string;
  thinking_level?: string;
  kind?: "main";
}

export interface SendMessageInput {
  client_msg_id: string;
  text: string;
  images?: unknown[];
  /** Required while a run is in flight: "steer" interrupts, "follow_up" queues. */
  streaming_behavior?: "steer" | "follow_up";
}

export const sessions = {
  list: (params: { project?: string } = {}) => {
    const q = params.project ? `?project=${encodeURIComponent(params.project)}` : "";
    return request<{ sessions: Session[] }>(`/v1/sessions${q}`);
  },
  get: (id: string) =>
    request<{ session: Session; status?: RuntimeStatus; project?: Project }>(`/v1/sessions/${id}`),
  create: (projectId: string, input: CreateSessionInput) =>
    request<{ session: Session }>(`/v1/projects/${projectId}/sessions`, {
      method: "POST",
      body: input,
    }),
  update: (id: string, patch: Partial<Pick<Session, "title" | "model_ref" | "status">>) =>
    request<{ session: Session }>(`/v1/sessions/${id}`, { method: "PATCH", body: patch }),
  send: (id: string, input: SendMessageInput) =>
    request<PromptResult>(`/v1/sessions/${id}/messages`, { method: "POST", body: input }),
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
  compact: (id: string, instructions = "") =>
    request<{ ok: boolean }>(`/v1/sessions/${id}/compact`, {
      method: "POST",
      body: { custom_instructions: instructions },
    }),
  entries: (id: string, since = "") =>
    request<{ entries: TreeEntry[]; leaf_id: string }>(
      `/v1/sessions/${id}/entries${since ? `?since=${encodeURIComponent(since)}` : ""}`,
    ),
  tree: (id: string) => request<{ tree: TreeEntry[]; leaf_id: string }>(`/v1/sessions/${id}/tree`),
  forkPoints: (id: string) => request<{ points: ForkPoint[] }>(`/v1/sessions/${id}/fork-points`),
  stats: (id: string) => request<SessionStats>(`/v1/sessions/${id}/stats`),
  status: (id: string) => request<RuntimeStatus>(`/v1/sessions/${id}/status`),
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

export const admin = {
  users: () => request<{ users: User[] }>("/v1/admin/users"),
  createUser: (input: CreateUserInput) =>
    request<{ users: User[] }>("/v1/admin/users", { method: "POST", body: input }),
  updateUser: (id: string, input: UpdateUserInput) =>
    request<{ users: User[] }>(`/v1/admin/users/${id}`, { method: "PATCH", body: input }),
};

export const api = { auth, projects, sessions, files, git, providers, skills, mcp, admin };
export default api;
