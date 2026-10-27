/**
 * File classification for the workspace panels.
 *
 * Pure string work: given a path (and optionally the `mime_type` the file API
 * reported), decide which renderer the preview should use and which shiki
 * language the highlighters should ask for. Keeping this here — instead of
 * inline in components — makes it testable and keeps the components short.
 */

export type FileKind =
  | "code"
  | "markdown"
  | "image"
  | "html"
  | "table"
  | "pdf"
  | "binary"
  | "text";

/** Extension -> shiki language id (also used by `@git-diff-view`). */
const LANGUAGE_BY_EXT: Record<string, string> = {
  bash: "bash",
  c: "c",
  cc: "cpp",
  conf: "ini",
  cpp: "cpp",
  cs: "csharp",
  css: "css",
  diff: "diff",
  env: "bash",
  go: "go",
  gql: "graphql",
  graphql: "graphql",
  h: "c",
  hpp: "cpp",
  html: "html",
  java: "java",
  js: "javascript",
  json: "json",
  json5: "json",
  jsx: "jsx",
  kt: "kotlin",
  less: "less",
  lua: "lua",
  md: "markdown",
  mdx: "markdown",
  mjs: "javascript",
  php: "php",
  proto: "protobuf",
  py: "python",
  rb: "ruby",
  rs: "rust",
  scss: "scss",
  sh: "bash",
  sql: "sql",
  svelte: "xml",
  swift: "swift",
  toml: "ini",
  ts: "typescript",
  tsx: "tsx",
  txt: "plaintext",
  vue: "xml",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
  zig: "zig",
  zsh: "bash",
};

const IMAGE_EXT = new Set([
  "avif",
  "bmp",
  "gif",
  "ico",
  "jpeg",
  "jpg",
  "png",
  "svg",
  "webp",
]);

const TABLE_EXT = new Set(["csv", "tsv"]);

const MARKDOWN_EXT = new Set(["md", "mdx", "markdown"]);

const HTML_EXT = new Set(["htm", "html"]);

const BINARY_EXT = new Set([
  "7z",
  "a",
  "bin",
  "class",
  "dll",
  "dmg",
  "doc",
  "docx",
  "exe",
  "gz",
  "jar",
  "mov",
  "mp3",
  "mp4",
  "o",
  "otf",
  "pdf",
  "ppt",
  "pptx",
  "rar",
  "so",
  "tar",
  "ttf",
  "wav",
  "woff",
  "woff2",
  "xls",
  "xlsx",
  "zip",
]);

/** Lowercase extension without the dot, or "" when the name has none. */
export function extensionOf(path: string): string {
  const name = path.split("/").pop() ?? path;
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return "";
  return name.slice(dot + 1).toLowerCase();
}

/** Base name of a path. */
export function baseName(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

/** shiki / diff-view language id for a path, `plaintext` when unknown. */
export function languageOf(path: string): string {
  return LANGUAGE_BY_EXT[extensionOf(path)] ?? "plaintext";
}

/**
 * Picks the preview renderer for a path.
 *
 * `mime` wins for the cases the file API knows better than the extension
 * (images served without one, `application/pdf`, ...); the extension is the
 * fallback so a mislabelled `text/plain` never hides markdown or CSV.
 */
export function kindOf(path: string, mime?: string): FileKind {
  const ext = extensionOf(path);
  const type = (mime ?? "").toLowerCase();

  if (type.startsWith("image/") || IMAGE_EXT.has(ext)) return "image";
  if (type === "application/pdf" || ext === "pdf") return "pdf";
  if (type.startsWith("text/csv") || type.startsWith("text/tab-separated") || TABLE_EXT.has(ext)) {
    return "table";
  }
  if (type.includes("markdown") || MARKDOWN_EXT.has(ext)) return "markdown";
  if (type === "text/html" || HTML_EXT.has(ext)) return "html";
  if (BINARY_EXT.has(ext) || type.startsWith("application/octet-stream")) return "binary";
  if (type.startsWith("text/") || languageOf(path) !== "plaintext") return "code";

  return "text";
}

/** `mime_type` for the image preview's `data:` URL, derived from the path. */
export function imageMimeOf(path: string, mime?: string): string {
  if (mime?.startsWith("image/")) return mime;
  const ext = extensionOf(path);
  if (ext === "svg") return "image/svg+xml";
  if (ext === "jpg" || ext === "jpeg") return "image/jpeg";
  if (ext === "ico") return "image/x-icon";
  return `image/${ext || "png"}`;
}

/** Human readable byte size, e.g. `1.4 MB`. */
export function formatBytes(bytes: number | undefined): string {
  if (bytes === undefined || Number.isNaN(bytes)) return "未知大小";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

/**
 * The single letter the file tree shows for a git change.
 * `index`/`worktree` are the porcelain columns; a conflict anywhere wins.
 */
export function gitLetter(change: { index: string; worktree: string }): string {
  const { index, worktree } = change;
  if (index === "U" || worktree === "U" || `${index}${worktree}`.includes("U")) return "!";
  if (worktree === "D" || index === "D") return "D";
  if (index === "A") return "A";
  if (index === "?" || worktree === "?") return "U";
  return "M";
}

/** Path -> git letter, for the tree's per-row chips. */
export function gitLetterMap(
  changes: { path: string; index: string; worktree: string }[],
): Map<string, string> {
  const map = new Map<string, string>();
  for (const change of changes) {
    map.set(change.path, gitLetter(change));
  }
  return map;
}
