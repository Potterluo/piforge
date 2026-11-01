/**
 * Draft review comments, kept per session in localStorage.
 *
 * A comment is written while the user is reading a diff, and the diff is
 * frequently not ready to be sent: the run is in flight, the user wants to write
 * three comments and send them together, or the page is reloaded by a deploy.
 * Losing what was typed is the failure this module exists to prevent, so every
 * draft is persisted the moment it is saved and restored on mount.
 *
 * The store is deliberately not React state: the diff view and the file preview
 * both write to it, and a reload has to read it before React mounts. Reads are
 * cheap (one JSON parse per browser session is cached), writes are synchronous,
 * and a storage failure (Safari private mode, a full quota) degrades to
 * "comments work for this tab, they just will not survive a reload" instead of
 * throwing inside a render.
 */

export interface ReviewDraft {
  id: string;
  path: string;
  /** 1-based line in the file; the new side unless `side` is "old". */
  line: number;
  /** Last quoted line, equal to `line` for a single-line comment. */
  endLine: number;
  side: "new" | "old";
  /** The diff hunk header the anchor came from, "" when the anchor is a file line. */
  hunk: string;
  /** The code the comment is about, one entry per line. */
  quote: string[];
  /** What the user wrote. */
  body: string;
  /** Where the comment was written: a diff line or a file-preview line. */
  origin: "diff" | "file";
  createdAt: string;
}

export const DRAFT_STORAGE_KEY = "piforge.review.drafts.v1";

type DraftBook = Record<string, ReviewDraft[]>;

let cache: DraftBook | null = null;
const listeners = new Set<() => void>();

function storage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    // Access itself throws in some privacy modes.
    return null;
  }
}

function load(): DraftBook {
  if (cache) return cache;
  const raw = (() => {
    try {
      return storage()?.getItem(DRAFT_STORAGE_KEY) ?? null;
    } catch {
      return null;
    }
  })();
  if (!raw) {
    cache = {};
    return cache;
  }
  try {
    const parsed = JSON.parse(raw) as unknown;
    cache = sanitize(parsed);
  } catch {
    // A corrupt blob is not worth a crash: the drafts are lost either way, and a
    // page that cannot render is worse than one that forgot.
    cache = {};
  }
  return cache;
}

/** sanitize drops anything that does not look like a draft. */
function sanitize(value: unknown): DraftBook {
  const out: DraftBook = {};
  if (!value || typeof value !== "object") return out;
  for (const [sessionId, rows] of Object.entries(value as Record<string, unknown>)) {
    if (!Array.isArray(rows)) continue;
    const kept: ReviewDraft[] = [];
    for (const row of rows) {
      if (!row || typeof row !== "object") continue;
      const draft = row as Partial<ReviewDraft>;
      if (typeof draft.path !== "string" || !draft.path) continue;
      if (typeof draft.line !== "number" || draft.line <= 0) continue;
      if (typeof draft.body !== "string") continue;
      kept.push({
        id: typeof draft.id === "string" && draft.id ? draft.id : newDraftId(),
        path: draft.path,
        line: draft.line,
        endLine: typeof draft.endLine === "number" && draft.endLine >= draft.line ? draft.endLine : draft.line,
        side: draft.side === "old" ? "old" : "new",
        hunk: typeof draft.hunk === "string" ? draft.hunk : "",
        quote: Array.isArray(draft.quote) ? draft.quote.filter((line): line is string => typeof line === "string") : [],
        body: draft.body,
        origin: draft.origin === "file" ? "file" : "diff",
        createdAt: typeof draft.createdAt === "string" ? draft.createdAt : new Date(0).toISOString(),
      });
    }
    if (kept.length > 0) out[sessionId] = kept;
  }
  return out;
}

function persist(): void {
  const book = load();
  try {
    storage()?.setItem(DRAFT_STORAGE_KEY, JSON.stringify(book));
  } catch {
    // See the module comment: the drafts still work for this tab.
  }
}

function notify(): void {
  for (const listener of listeners) listener();
}

export function newDraftId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `rc-${crypto.randomUUID()}`;
  }
  return `rc-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/**
 * The empty list, shared by every "no drafts" answer.
 *
 * `listDrafts` is read by `useSyncExternalStore`, which compares snapshots with
 * Object.is: returning a fresh `[]` for the empty case would look like a change
 * on every render and loop. Callers only read the array (filter/map/spread), so
 * one shared empty list is safe.
 */
export const EMPTY_DRAFTS: ReviewDraft[] = [];

/** listDrafts returns this session's drafts, oldest first. */
export function listDrafts(sessionId: string): ReviewDraft[] {
  return load()[sessionId] ?? EMPTY_DRAFTS;
}

/** draftsForPath is what the diff/file view renders markers from. */
export function draftsForPath(drafts: ReviewDraft[], path: string): ReviewDraft[] {
  return drafts.filter((draft) => draft.path === path);
}

/** draftsAt is the set of drafts on exactly one line (either side). */
export function draftsAt(drafts: ReviewDraft[], path: string, line: number): ReviewDraft[] {
  return drafts.filter((draft) => draft.path === path && line >= draft.line && line <= draft.endLine);
}

/**
 * saveDraft inserts or replaces one draft and returns the session's list.
 *
 * An empty body deletes: a comment the user cleared is not a draft, and a draft
 * that renders an empty marker is noise in the diff.
 */
export function saveDraft(sessionId: string, draft: ReviewDraft): ReviewDraft[] {
  const book = load();
  const rows = book[sessionId] ?? [];
  const body = draft.body.trim();
  const next = body
    ? rows.some((row) => row.id === draft.id)
      ? rows.map((row) => (row.id === draft.id ? { ...draft, body } : row))
      : [...rows, { ...draft, body }]
    : rows.filter((row) => row.id !== draft.id);
  if (next.length > 0) book[sessionId] = next;
  else delete book[sessionId];
  persist();
  notify();
  return next;
}

/** removeDraft deletes one draft (after it was sent, or by the user). */
export function removeDraft(sessionId: string, id: string): ReviewDraft[] {
  const book = load();
  const rows = (book[sessionId] ?? []).filter((row) => row.id !== id);
  if (rows.length > 0) book[sessionId] = rows;
  else delete book[sessionId];
  persist();
  notify();
  return rows;
}

/** clearSession drops every draft of a session (used by the panel header). */
export function clearSessionDrafts(sessionId: string): ReviewDraft[] {
  const book = load();
  delete book[sessionId];
  persist();
  notify();
  return [];
}

/** subscribeDrafts is the change feed the React hook listens to. */
export function subscribeDrafts(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** forgetDraftsCache is for tests: it drops the in-memory cache. */
export function forgetDraftsCache(): void {
  cache = null;
}
