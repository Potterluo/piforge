import { describe, expect, test } from "vitest";

import { fileStatusWord, fileSummary, forgeLabel, stepLabel, tokenHint } from "./pr";

describe("pull-request wording (FR-12.6)", () => {
  test("each step the server can name has a label", () => {
    expect(stepLabel("config")).toBe("第 1 步 · 配置");
    expect(stepLabel("push")).toBe("第 2 步 · 推送");
    expect(stepLabel("create")).toBe("第 3 步 · 创建 PR");
  });

  test("an unknown or missing step does not render as undefined", () => {
    expect(stepLabel(undefined)).toBe("出错的步骤");
    expect(stepLabel("something-new")).toBe("出错的步骤");
    expect(stepLabel(7)).toBe("出错的步骤");
  });

  test("the summary counts files and keeps the sign of each line count", () => {
    expect(fileSummary([], 0, 0)).toBe("没有文件改动");
    expect(
      fileSummary(
        [
          { path: "a.ts", status: "modified", additions: 10, deletions: 2 },
          { path: "b.ts", status: "added", additions: 2, deletions: 0 },
        ],
        12,
        2,
      ),
    ).toBe("2 个文件 · +12 −2");
  });

  test("status words cover the four diff statuses git reports", () => {
    expect(fileStatusWord("added")).toBe("新增");
    expect(fileStatusWord("deleted")).toBe("删除");
    expect(fileStatusWord("renamed")).toBe("重命名");
    expect(fileStatusWord("modified")).toBe("修改");
  });

  test("the forge label names the dialect, and the token hint says it is write-only", () => {
    expect(forgeLabel("gitlab")).toBe("GitLab");
    expect(forgeLabel("github")).toBe("GitHub / Gitea");
    expect(tokenHint(true)).toContain("不会回显");
    expect(tokenHint(false)).toContain("加密存储");
  });
});
