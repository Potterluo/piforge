"use client";

import * as React from "react";

import api from "@/lib/api/client";
import type { GitChange, GitGraph, GitStatus } from "@/lib/types";
import { useAsync, type AsyncResult } from "@/components/workspace/use-async";
import { useDebouncedCallback } from "@/hooks/use-debounced-callback";
import { useOptionalSessionEventState } from "@/components/session/session-events-provider";

/**
 * Shared git state for the 变更 and Git tabs.
 *
 * Both panels live in the same inspector and read the same `status` + `log`
 * responses; fetching once here keeps them consistent (staging a file must be
 * visible in the history header without a second round trip) and means the
 * 变更 tab is already warm when the user switches.
 */
export interface GitData {
  sessionId: string;
  status: AsyncResult<GitStatus>;
  log: AsyncResult<GitGraph>;
  /** Refreshes both halves after a mutating operation. */
  refresh: () => void;
  /**
   * Bumps on every refresh. Anything derived from git — ahead/behind included — hangs
   * its cache key off this, so a commit made in the panel cannot leave the remote
   * counts stale until a page reload.
   */
  revision: number;
}

const GitDataContext = React.createContext<GitData | null>(null);

export function GitProvider({
  sessionId,
  fsRevision = 0,
  children,
}: {
  sessionId: string;
  /**
   * A counter the workbench bumps when something outside git changed the
   * checkout: the terminal printed output, the editor saved, a drag-drop wrote
   * a file. The session event stream cannot carry a PTY command, so this is how
   * 变更 hears about it — debounced below, never polled.
   */
  fsRevision?: number;
  children: React.ReactNode;
}) {
  const [revision, setRevision] = React.useState(0);

  /**
   * Bumped when something outside this provider says the checkout changed.
   *
   * The acceptance pass watched a run edit `count.js` while 变更 still said
   * 工作区干净，没有改动。: the status was fetched once at mount and never again,
   * while the file tree beside it kept up because it listens to the session
   * event stream. This is the same signal, and `revision` also refetches the
   * open diff and the remote counts, so no part of the panel can keep showing
   * the old tree.
   */
  const refresh = React.useCallback(() => setRevision((current) => current + 1), []);

  // Several signals can describe one change (the tool ends, the run settles,
  // the window comes back). Coalescing them keeps a streaming run from causing
  // one status request per event.
  const scheduleRefresh = useDebouncedCallback(refresh, 400);

  const events = useOptionalSessionEventState();
  // `workspace` is the file tree's existing signal: tool.ended / run.settled.
  // `settled` is the run counter, the backstop for a run that ended without a
  // tool event being reported.
  const workspace = events?.workspace ?? 0;
  const settled = events?.state.settled ?? 0;

  React.useEffect(() => {
    if (workspace === 0 && settled === 0) return;
    scheduleRefresh();
  }, [workspace, settled, scheduleRefresh]);

  // A command typed in the terminal changes the worktree with no session event
  // addressed to this panel. The acceptance pass watched `echo … >> square.js`
  // leave 变更 saying 干净 until the refresh button was pressed; this is the
  // existing coalesced signal the file tree already listens to.
  React.useEffect(() => {
    if (fsRevision === 0) return;
    scheduleRefresh();
  }, [fsRevision, scheduleRefresh]);

  // A command typed in the terminal or a run in a background tab changes the
  // worktree with no event addressed to this panel. Regaining focus is the one
  // signal that always means "look again".
  React.useEffect(() => {
    const onFocus = () => scheduleRefresh();
    const onVisible = () => {
      if (document.visibilityState === "visible") scheduleRefresh();
    };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [scheduleRefresh]);

  // `revision` is part of the key, not a second `reload()` call: one bump
  // refetches both halves in the same render, so 变更 and Git cannot disagree
  // about which revision they are showing.
  const status = useAsync(`git-status:${sessionId}:${revision}`, () =>
    api.git.status(sessionId),
  );
  const log = useAsync(`git-log:${sessionId}:${revision}`, () => api.git.log(sessionId, 200));

  // Rebuilt every render on purpose: `useAsync` returns a fresh object anyway,
  // and the panels are cheap. A memo here would only hide the churn.
  const value: GitData = {
    sessionId,
    status,
    log,
    revision,
    refresh,
  };

  return <GitDataContext.Provider value={value}>{children}</GitDataContext.Provider>;
}

export function useGitData(): GitData {
  const value = React.useContext(GitDataContext);
  if (!value) throw new Error("useGitData must be used inside <GitProvider>");
  return value;
}

export type ChangeState = "staged" | "unstaged" | "partial" | "untracked";

export interface ChangeRowModel {
  path: string;
  change: GitChange;
  /** Exactly one of the four states, so a path is described once. */
  state: ChangeState;
  /** Staging the rest of this path is a meaningful action. */
  canStage: boolean;
  /** Taking it back out of the index is a meaningful action. */
  canUnstage: boolean;
}

/** A status letter that means "unchanged in this column". */
const UNCHANGED = new Set([" ", ".", ""]);

/**
 * One row per path, with the state it is in.
 *
 * `git status --porcelain=v2` writes `.` in an unchanged column, not a space.
 * Comparing against a space made every fully staged file look modified in the
 * worktree as well, so 全部暂存 produced four rows under 已暂存 *and* the same
 * four under 未暂存, each labelled 部分暂存 — eight rows for four changes, and no
 * way to tell what the commit would contain.
 *
 * Rows are keyed by path and never repeated: a partly staged file is one row
 * whose state is `partial`, not two rows that each tell half the story.
 */
export function changeRows(changes: GitChange[]): ChangeRowModel[] {
  const rows: ChangeRowModel[] = [];
  const seen = new Set<string>();

  for (const change of changes ?? []) {
    const path = change?.path;
    // A duplicate path would be a second row for the same file; the server
    // reports one, and a client that repeats it is the bug this fixes.
    if (!path || seen.has(path)) continue;
    seen.add(path);

    const index = change.index ?? " ";
    const worktree = change.worktree ?? " ";
    const untracked = index === "?" || worktree === "?";
    const staged = !untracked && !UNCHANGED.has(index);
    const unstaged = !untracked && !UNCHANGED.has(worktree);

    rows.push({
      path,
      change,
      state: untracked ? "untracked" : staged && unstaged ? "partial" : staged ? "staged" : "unstaged",
      canStage: untracked || unstaged,
      canUnstage: staged,
    });
  }

  return rows;
}

/** Paths already in the index — what a plain `git commit` would take. */
export function stagedPaths(changes: GitChange[]): string[] {
  return changeRows(changes)
    .filter((row) => row.canUnstage)
    .map((row) => row.path);
}

/** Status letter for a change row, mirroring the file tree's chips. */
export function changeLetter(change: GitChange): string {
  if (change.index === "U" || change.worktree === "U") return "!";
  if (change.index === "D" || change.worktree === "D") return "D";
  if (change.index === "A") return "A";
  if (change.index === "?" || change.worktree === "?") return "U";
  return "M";
}
