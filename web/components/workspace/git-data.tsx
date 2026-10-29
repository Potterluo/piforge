"use client";

import * as React from "react";

import api from "@/lib/api/client";
import type { GitChange, GitGraph, GitStatus } from "@/lib/types";
import { useAsync, type AsyncResult } from "@/components/workspace/use-async";

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
  children,
}: {
  sessionId: string;
  children: React.ReactNode;
}) {
  const [revision, setRevision] = React.useState(0);
  const status = useAsync(`git-status:${sessionId}`, () => api.git.status(sessionId));
  const log = useAsync(`git-log:${sessionId}`, () => api.git.log(sessionId, 200));

  // Rebuilt every render on purpose: `useAsync` returns a fresh object anyway,
  // and the panels are cheap. A memo here would only hide the churn.
  const value: GitData = {
    sessionId,
    status,
    log,
    revision,
    refresh: () => {
      status.reload();
      log.reload();
      setRevision((current) => current + 1);
    },
  };

  return <GitDataContext.Provider value={value}>{children}</GitDataContext.Provider>;
}

export function useGitData(): GitData {
  const value = React.useContext(GitDataContext);
  if (!value) throw new Error("useGitData must be used inside <GitProvider>");
  return value;
}

export interface ChangeGroups {
  staged: GitChange[];
  unstaged: GitChange[];
  untracked: GitChange[];
}

/**
 * Splits porcelain `XY` into the three groups the PRD asks for.
 *
 * `X` is the index column, `Y` the worktree column. `??` is untracked, and a
 * file can legitimately appear in both staged and unstaged (stage part of it,
 * keep editing) — that is why these are three lists, not a partition.
 */
export function groupChanges(changes: GitChange[]): ChangeGroups {
  const staged: GitChange[] = [];
  const unstaged: GitChange[] = [];
  const untracked: GitChange[] = [];

  for (const change of changes) {
    const index = change.index ?? " ";
    const worktree = change.worktree ?? " ";

    if (index === "?" || worktree === "?") {
      untracked.push(change);
      continue;
    }
    if (index !== " ") staged.push(change);
    if (worktree !== " ") unstaged.push(change);
  }

  return { staged, unstaged, untracked };
}

/** Status letter for a change row, mirroring the file tree's chips. */
export function changeLetter(change: GitChange): string {
  if (change.index === "U" || change.worktree === "U") return "!";
  if (change.index === "D" || change.worktree === "D") return "D";
  if (change.index === "A") return "A";
  if (change.index === "?" || change.worktree === "?") return "U";
  return "M";
}
