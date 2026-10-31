import type { GitCommit } from "@/lib/types";

/**
 * What an adopt would bring over, computed from the two histories the UI can
 * already read (PRD FR-2.6).
 *
 * The backend owns the merge. This is the *preview* a confirmation needs: which
 * commits are on the branch but not on the target, and where the two checkouts
 * parted. It deliberately does not guess: a commit is "coming over" only when it
 * is not reachable from the target's HEAD, which is the same rule `git merge`
 * applies.
 */
export interface AdoptPreview {
  /**
   * Commits the branch has that the target does not, oldest first, so the list
   * reads like history.
   */
  commits: GitCommit[];
  /**
   * The commit the two checkouts share: the base of the range a file list is
   * computed against. Null when the histories do not meet within the log window.
   */
  base: string | null;
  /** False when no commit is shared, which adopt will refuse as 没有共同历史. */
  commonHistory: boolean;
}

/** Every commit reachable from `head` through first- and second-parent links. */
export function reachableFrom(head: string, commits: GitCommit[]): Set<string> {
  const byHash = new Map(commits.map((commit) => [commit.hash, commit]));
  const seen = new Set<string>();
  const pending = head ? [head] : [];
  while (pending.length > 0) {
    const hash = pending.pop();
    if (!hash || seen.has(hash)) continue;
    seen.add(hash);
    for (const parent of byHash.get(hash)?.parents ?? []) pending.push(parent);
  }
  return seen;
}

/**
 * The commits and merge base a confirmation should show.
 *
 * `branchLog` and `targetLog` are each newest-first (`git log` order). The
 * merge base is the newest commit on the target's history that is also an
 * ancestor of the branch — for a branch forked from the target that is the fork
 * point, and for a target that has not moved it is the target's HEAD.
 */
export function previewAdopt(
  branchHead: string,
  branchLog: GitCommit[],
  targetHead: string,
  targetLog: GitCommit[],
): AdoptPreview {
  const targetReachable = reachableFrom(targetHead, targetLog);
  const branchReachable = reachableFrom(branchHead, branchLog);

  const commits = branchLog.filter((commit) => !targetReachable.has(commit.hash)).reverse();

  let base: string | null = null;
  for (const commit of targetLog) {
    // Both conditions matter: the server's log includes refs outside the target's
    // own history (the branch's commit is visible from the parent's checkout),
    // and taking the newest *logged* commit instead of the newest *reachable*
    // one set the base to the branch's own HEAD — every file list came back
    // empty because the range was `HEAD..HEAD`.
    if (targetReachable.has(commit.hash) && branchReachable.has(commit.hash)) {
      base = commit.hash;
      break;
    }
  }

  return { commits, base, commonHistory: base !== null };
}

/** How a branch's work travelled into the target, in the UI's words. */
export function adoptModeLabel(mode: string): string {
  switch (mode) {
    case "merge":
      return "合并提交";
    case "fast-forward":
      return "快进合并";
    case "working-tree":
      return "已应用到工作区（未提交）";
    case "merge+working-tree":
      return "合并提交，剩余改动在工作区";
    case "fast-forward+working-tree":
      return "快进合并，剩余改动在工作区";
    default:
      return "已采纳";
  }
}

/**
 * The one line under the toast that says what actually landed.
 *
 * The file list is the part a user checks: "12 files" is not a review, and the
 * adopt result already carries the exact paths the operation touched.
 */
export function adoptSummary(result: {
  mode: string;
  commit: string;
  files: string[];
}): string {
  const files = result.files ?? [];
  const short = result.commit ? result.commit.slice(0, 7) : "—";
  if (files.length === 0) return `${adoptModeLabel(result.mode)} · ${short} · 没有文件改动`;
  const shown = files.slice(0, 6).join("、");
  const more = files.length > 6 ? ` 等 ${files.length} 个` : "";
  return `${adoptModeLabel(result.mode)} · ${short} · ${files.length} 个文件：${shown}${more}`;
}

/** Unique paths touched by a diff, in git's order. */
export function adoptFilePaths(diffs: { path: string }[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const diff of diffs ?? []) {
    if (!diff?.path || seen.has(diff.path)) continue;
    seen.add(diff.path);
    out.push(diff.path);
  }
  return out;
}
