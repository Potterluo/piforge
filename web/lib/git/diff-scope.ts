import type { DiffScope } from "@/lib/types";

/**
 * What a diff is scoped to (PRD FR-12.1).
 *
 * The scope is not a label: each value changes the git command the server runs,
 * and therefore the patch that comes back. `range` compares two revisions
 * (`HEAD~1..HEAD`, `main..HEAD`), which is how a review reads "what this commit
 * or branch added" without pretending the working tree is the only place
 * changes live.
 */
export interface DiffRange {
  from: string;
  to: string;
}

/** The panel's tabs, in the order a reviewer needs them. */
export const SCOPES: { value: DiffScope; label: string }[] = [
  { value: "unstaged", label: "工作区" },
  { value: "staged", label: "已暂存" },
  { value: "branch", label: "分支" },
  { value: "range", label: "范围" },
];

/** The range a reviewer almost always wants first: the last commit. */
export const DEFAULT_RANGE: DiffRange = { from: "HEAD~1", to: "HEAD" };

/**
 * Scopes that read history rather than the working tree. Hunk actions are
 * hidden for them: staging a hunk into a commit that already exists is not a
 * thing, and offering the button would be a lie.
 */
export function isHistoricalScope(scope: DiffScope): boolean {
  return scope === "branch" || scope === "commit" || scope === "range";
}

/** Hunk-level staging only means something in the two live scopes. */
export function canStageHunks(scope: DiffScope): boolean {
  return scope === "unstaged" || scope === "staged";
}

/**
 * The query string for one diff request. `from`/`to` are sent only for `range`:
 * a staged diff carrying a stray `from` would be a request whose meaning
 * depends on which server read it.
 */
export function diffQuery(
  scope: DiffScope,
  path = "",
  range: DiffRange = DEFAULT_RANGE,
): URLSearchParams {
  const query = new URLSearchParams({ scope });
  if (path) query.set("path", path);
  if (scope === "range") {
    query.set("from", range.from.trim() || DEFAULT_RANGE.from);
    query.set("to", range.to.trim() || DEFAULT_RANGE.to);
  }
  return query;
}

/** The badge under the tabs: what the diff below is actually comparing. */
export function scopeLabel(scope: DiffScope, range: DiffRange = DEFAULT_RANGE): string {
  switch (scope) {
    case "staged":
      return "已暂存";
    case "branch":
      return "分支（对比默认基线）";
    case "commit":
      return "某次提交";
    case "range":
      return `${range.from.trim() || DEFAULT_RANGE.from}..${range.to.trim() || DEFAULT_RANGE.to}`;
    default:
      return "工作区";
  }
}
