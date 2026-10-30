import { describe, expect, it } from "vitest";

import {
  DEFAULT_RANGE,
  canStageHunks,
  diffQuery,
  isHistoricalScope,
  scopeLabel,
} from "@/lib/git/diff-scope";

describe("diff scope", () => {
  it("changes the request, not just the label", () => {
    const unstaged = diffQuery("unstaged", "src/a.ts");
    const staged = diffQuery("staged", "src/a.ts");
    expect(unstaged.get("scope")).toBe("unstaged");
    expect(staged.get("scope")).toBe("staged");
    expect(unstaged.get("path")).toBe("src/a.ts");
    expect(staged.get("path")).toBe("src/a.ts");
    expect(unstaged.toString()).not.toBe(staged.toString());
  });

  it("sends an explicit commit range, trimmed, and nothing else", () => {
    const query = diffQuery("range", "", { from: " HEAD~2 ", to: "main" });
    expect(query.get("scope")).toBe("range");
    expect(query.get("from")).toBe("HEAD~2");
    expect(query.get("to")).toBe("main");
    expect(query.has("path")).toBe(false);

    // The default is the last commit, never an empty string the server rejects.
    const fallback = diffQuery("range", "f.txt");
    expect(fallback.get("from")).toBe(DEFAULT_RANGE.from);
    expect(fallback.get("to")).toBe(DEFAULT_RANGE.to);
  });

  it("keeps from/to off the scopes that do not use them", () => {
    const query = diffQuery("staged", "f.txt", { from: "main", to: "HEAD" });
    expect(query.has("from")).toBe(false);
    expect(query.has("to")).toBe(false);
  });

  it("labels every scope with what it compares", () => {
    expect(scopeLabel("unstaged")).toBe("工作区");
    expect(scopeLabel("staged")).toBe("已暂存");
    expect(scopeLabel("range", { from: "HEAD~1", to: "HEAD" })).toBe("HEAD~1..HEAD");
    expect(scopeLabel("branch")).toContain("分支");
  });

  it("only offers hunk actions on the live scopes", () => {
    expect(canStageHunks("unstaged")).toBe(true);
    expect(canStageHunks("staged")).toBe(true);
    expect(canStageHunks("range")).toBe(false);
    expect(isHistoricalScope("range")).toBe(true);
    expect(isHistoricalScope("branch")).toBe(true);
    expect(isHistoricalScope("unstaged")).toBe(false);
  });
});
