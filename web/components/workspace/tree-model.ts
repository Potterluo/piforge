/**
 * Tree model for the lazy file browser.
 *
 * `api.files.tree()` is called per directory, so the frontend keeps two pieces
 * of state and derives the render tree from them:
 *   - `root`: the children of the repository root,
 *   - `dirs`: path -> children, one entry per directory that has been loaded.
 *
 * A directory with no entry in `dirs` has never been fetched; it renders with
 * `children: []` so `react-arborist` still draws its expand chevron.
 */
import type { FileNode } from "@/lib/types";

export interface TreeItem {
  id: string;
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
  children?: TreeItem[] | null;
}

/** Directories hidden while "显示忽略文件" is off. */
const IGNORED = /(^|\/)(node_modules|\.git|\.next|dist|build|target|vendor|__pycache__)(\/|$)/;

export function isIgnoredPath(path: string): boolean {
  return IGNORED.test(path);
}

/**
 * Flattens a `depth > 1` response into `dirs` entries.
 *
 * The API returns nested `children` for the first N levels; registering each
 * of them means the tree renders those levels without extra round trips.
 */
export function collectDirs(
  nodes: FileNode[],
  into: Record<string, FileNode[]> = {},
): Record<string, FileNode[]> {
  for (const node of nodes) {
    if (!node.is_dir || !node.children) continue;
    into[node.path] = node.children;
    collectDirs(node.children, into);
  }
  return into;
}

function toItem(
  node: FileNode,
  dirs: Record<string, FileNode[]>,
  showIgnored: boolean,
): TreeItem {
  if (!node.is_dir) {
    return {
      id: node.path,
      name: node.name,
      path: node.path,
      isDir: false,
      size: node.size,
      children: null,
    };
  }

  const loaded = dirs[node.path];
  const visible = (loaded ?? []).filter((child) => showIgnored || !isIgnoredPath(child.path));

  return {
    id: node.path,
    name: node.name,
    path: node.path,
    isDir: true,
    children: visible.map((child) => toItem(child, dirs, showIgnored)),
  };
}

/** Builds the render tree from the loaded state. */
export function buildTree(
  root: FileNode[],
  dirs: Record<string, FileNode[]>,
  showIgnored: boolean,
): TreeItem[] {
  return root
    .filter((node) => showIgnored || !isIgnoredPath(node.path))
    .map((node) => toItem(node, dirs, showIgnored));
}

/** All file paths seen so far, for the inline filter. */
export function collectFiles(
  root: FileNode[],
  dirs: Record<string, FileNode[]>,
  showIgnored: boolean,
  into: string[] = [],
): string[] {
  const walk = (nodes: FileNode[]) => {
    for (const node of nodes) {
      if (!showIgnored && isIgnoredPath(node.path)) continue;
      if (node.is_dir) {
        if (dirs[node.path]) walk(dirs[node.path]);
      } else {
        into.push(node.path);
      }
    }
  };

  walk(root);
  return into;
}

/**
 * Flat, case-insensitive path search over the loaded tree.
 *
 * Filtering is intentionally *not* a tree operation: hoisting matches into a
 * flat list is what an IDE's quick-open does, it keeps the arborist's open
 * state untouched, and it stays cheap for 100k paths because we only ever look
 * at directories that were actually loaded.
 */
export function filterFiles(files: string[], query: string, limit = 500): string[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [];

  const matches: string[] = [];
  for (const path of files) {
    if (path.toLowerCase().includes(needle)) {
      matches.push(path);
      if (matches.length >= limit) break;
    }
  }
  return matches;
}

/** Directory path of a file, `""` at the root. */
export function parentOf(path: string): string {
  const index = path.lastIndexOf("/");
  return index === -1 ? "" : path.slice(0, index);
}
