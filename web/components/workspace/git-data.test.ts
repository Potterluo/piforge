import { describe, expect, it } from "vitest";

import { changeRows, stagedPaths } from "@/components/workspace/git-data";
import type { GitChange } from "@/lib/types";

/**
 * Regression for the review list that showed every file twice.
 *
 * `git status --porcelain=v2` marks an unchanged column with `.`. The panel
 * compared against `" "`, so a fully staged file (`A.`) also counted as
 * unstaged: 全部暂存 produced 4 + 4 rows and a 部分暂存 badge on all eight.
 * QA saw exactly that (13-staged.png: 已暂存 (4), 未暂存 (4)).
 */

const change = (path: string, index: string, worktree: string): GitChange => ({
  path,
  index,
  worktree,
});

describe("changeRows", () => {
  it("describes a fully staged porcelain-v2 change once, as staged", () => {
    const rows = changeRows([change("fresh.txt", "A", ".")]);

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      path: "fresh.txt",
      state: "staged",
      canStage: false,
      canUnstage: true,
    });
  });

  it("does not duplicate a staged file into the unstaged list", () => {
    // The exact state after 全部暂存 of four new files.
    const changes = [
      change("fresh.txt", "A", "."),
      change("hello.txt", "A", "."),
      change("notes.md", "A", "."),
      change("src/a.ts", "A", "."),
    ];

    const rows = changeRows(changes);

    expect(rows).toHaveLength(4);
    expect(rows.every((row) => row.state === "staged")).toBe(true);
    expect(new Set(rows.map((row) => row.path)).size).toBe(4);
  });

  it("calls a path that is both staged and dirty partial, once", () => {
    const rows = changeRows([change("a.ts", "M", "M")]);

    expect(rows).toHaveLength(1);
    expect(rows[0].state).toBe("partial");
    expect(rows[0].canStage).toBe(true);
    expect(rows[0].canUnstage).toBe(true);
  });

  it("keeps untracked files untracked and stagable", () => {
    const rows = changeRows([change("new.md", "?", "?")]);

    expect(rows[0]).toMatchObject({ state: "untracked", canStage: true, canUnstage: false });
  });

  it("still reads a porcelain-v1 style space column", () => {
    // The panel's own mocks and older servers use " " for "unchanged".
    const rows = changeRows([change("a.ts", " ", "M")]);

    expect(rows[0]).toMatchObject({ state: "unstaged", canStage: true, canUnstage: false });
  });

  it("collapses a duplicated path instead of drawing it twice", () => {
    const rows = changeRows([change("a.ts", "M", "M"), change("a.ts", "M", "M")]);

    expect(rows).toHaveLength(1);
  });

  it("treats a deleted-from-index file as staged", () => {
    const rows = changeRows([change("gone.txt", "D", ".")]);

    expect(rows[0]).toMatchObject({ state: "staged", canUnstage: true, canStage: false });
  });
});

describe("stagedPaths", () => {
  it("lists only what is really in the index", () => {
    const changes = [
      change("staged.txt", "A", "."),
      change("dirty.txt", ".", "M"),
      change("both.txt", "M", "M"),
      change("new.txt", "?", "?"),
    ];

    // The old implementation returned the dirty file too (`"." !== " "`), so
    // the commit button was enabled by a file nobody had staged.
    expect(stagedPaths(changes)).toEqual(["staged.txt", "both.txt"]);
  });

  it("is empty when only the worktree is dirty", () => {
    expect(stagedPaths([change("dirty.txt", ".", "M")])).toEqual([]);
  });
});
