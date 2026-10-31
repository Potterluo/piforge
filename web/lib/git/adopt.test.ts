import { describe, expect, test } from "vitest";

import {
  adoptFilePaths,
  adoptModeLabel,
  adoptSummary,
  previewAdopt,
  reachableFrom,
} from "./adopt";

const commit = (hash: string, parents: string[] = []): {
  hash: string;
  short: string;
  parents: string[];
  author: string;
  ts: string;
  subject: string;
} => ({
  hash,
  short: hash.slice(0, 7),
  parents,
  author: "dev",
  ts: "2026-10-31T00:00:00Z",
  subject: `commit ${hash}`,
});

/** A linear history: a → b → c (c newest). */
const linear = [commit("c", ["b"]), commit("b", ["a"]), commit("a", [])];

describe("previewAdopt", () => {
  test("a target that has not moved brings exactly the branch's commits", () => {
    // Branch is a → b → c; target stopped at a.
    const preview = previewAdopt("c", linear, "a", [commit("a", [])]);

    expect(preview.commits.map((entry) => entry.hash)).toEqual(["b", "c"]);
    expect(preview.base).toBe("a");
    expect(preview.commonHistory).toBe(true);
  });

  test("a target that moved gets only the branch-side commits, based at the fork", () => {
    // Both forked at a; target added t1, branch added b1 → b2.
    const branch = [commit("b2", ["b1"]), commit("b1", ["a"]), commit("a", [])];
    const target = [commit("t1", ["a"]), commit("a", [])];

    const preview = previewAdopt("b2", branch, "t1", target);

    expect(preview.commits.map((entry) => entry.hash)).toEqual(["b1", "b2"]);
    // The file list must be computed from the fork, not from the target's HEAD
    // (which does not contain b1's changes).
    expect(preview.base).toBe("a");
  });

  test("a branch that is already merged has nothing to adopt", () => {
    const merged = [commit("m", ["t1", "b1"]), commit("t1", ["a"]), commit("b1", ["a"]), commit("a", [])];

    expect(previewAdopt("m", merged, "m", merged).commits).toEqual([]);
  });

  test("a target log that also shows the branch commit does not move the base", () => {
    // `GET /git/log` lists refs outside the checkout's own history: the parent's
    // log contained the branch's commit, newest first. The first version took
    // that as the fork point, computed `HEAD..HEAD` and showed 0 files.
    const branch = [commit("b1", ["a"]), commit("a", [])];
    const target = [commit("b1", ["a"]), commit("a", [])];

    const preview = previewAdopt("b1", branch, "a", target);

    expect(preview.commits.map((entry) => entry.hash)).toEqual(["b1"]);
    expect(preview.base).toBe("a");
  });

  test("separate histories are reported as no common base", () => {
    const preview = previewAdopt("x", [commit("x", [])], "y", [commit("y", [])]);

    expect(preview.commonHistory).toBe(false);
    expect(preview.base).toBeNull();
    expect(preview.commits.map((entry) => entry.hash)).toEqual(["x"]);
  });

  test("reachability walks second parents of a merge commit", () => {
    const history = [
      commit("m", ["t1", "b1"]),
      commit("t1", ["a"]),
      commit("b1", ["a"]),
      commit("a", []),
    ];
    expect([...reachableFrom("m", history)].sort()).toEqual(["a", "b1", "m", "t1"]);
  });
});

describe("adopt wording", () => {
  test("each mode the server reports has a label", () => {
    expect(adoptModeLabel("merge")).toBe("合并提交");
    expect(adoptModeLabel("fast-forward")).toBe("快进合并");
    expect(adoptModeLabel("working-tree")).toBe("已应用到工作区（未提交）");
    expect(adoptModeLabel("merge+working-tree")).toContain("合并提交");
    expect(adoptModeLabel("fast-forward+working-tree")).toContain("快进合并");
  });

  test("the summary names the mode, the commit and the files", () => {
    const summary = adoptSummary({
      mode: "fast-forward",
      commit: "0123456789abcdef",
      files: ["count.js", "count.test.js"],
    });
    expect(summary).toContain("快进合并");
    expect(summary).toContain("0123456");
    expect(summary).toContain("count.js");
    expect(summary).toContain("2 个文件");
  });

  test("a long file list is capped but still counted", () => {
    const files = ["a", "b", "c", "d", "e", "f", "g"].map((name) => `${name}.txt`);
    const summary = adoptSummary({ mode: "merge", commit: "abc1234", files });
    expect(summary).toContain("7 个文件");
    expect(summary).toContain("a.txt");
    expect(summary).not.toContain("g.txt");
  });

  test("file paths are deduplicated and ordered", () => {
    expect(adoptFilePaths([{ path: "a" }, { path: "b" }, { path: "a" }])).toEqual(["a", "b"]);
  });
});
