import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * QA-13 finding 3: the review list could not be trusted.
 *
 * After 全部暂存 the same four files appeared under both 已暂存 (4) and 未暂存
 * (4), every one of the eight labelled 部分暂存: `git status --porcelain=v2`
 * marks an unchanged column with `.`, and the panel only treated a space as
 * "unchanged". Clicking an untracked file said 这个文件在当前范围内没有差异,
 * because `git diff` has nothing to say about a path the index has never seen.
 */
test.describe("the change list says what git says", () => {
  test("a fully staged file is listed once, and an untracked file has a diff", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    const files: [string, string][] = [
      ["fresh.txt", "fresh\n"],
      ["hello.txt", "hello\n"],
      ["notes.md", "# notes\n"],
      ["src/a.ts", "export const a = 1;\n"],
    ];
    for (const [path, body] of files) {
      const write = await apiWrite(
        page,
        "PUT",
        `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`,
        body,
      );
      expect(write.status).toBeLessThan(300);
    }

    // The panel reads status on mount; a reload models reopening the session.
    await page.reload();
    await page.getByTestId("panel-tab-changes").click();
    await expect(page.getByTestId("git-change").first()).toBeVisible({ timeout: 30_000 });

    // A new file must be reviewable before it is staged.
    await page.locator('[data-testid="git-change"][data-path="fresh.txt"]').first().click();
    // The pane shows the patch: `git diff --no-index` produced a real one, and
    // the diff view falls back to the patch text when it cannot draw lines.
    const diff = page.getByTestId("git-diff-patch").or(page.getByTestId("git-diff-view"));
    await expect(diff.first()).toBeVisible({ timeout: 20_000 });
    await expect(diff.first()).toContainText(/\+fresh/, { timeout: 20_000 });
    await expect(page.getByTestId("git-panel")).not.toContainText("这个文件在当前范围内没有差异");

    // 全部暂存.
    await page.getByTestId("git-stage-all").click();
    await expect
      .poll(
        async () => {
          const res = await page.request.get(`/v1/sessions/${sessionId}/git/status`);
          const body = (await res.json()) as { changes: { index: string }[] };
          return body.changes.every((change) => change.index !== "?");
        },
        { timeout: 30_000 },
      )
      .toBe(true);

    // One row per path, each in exactly one state.
    const rows = page.getByTestId("git-change");
    await expect(rows).toHaveCount(files.length, { timeout: 20_000 });
    const paths = await rows.evaluateAll((els) => els.map((el) => el.getAttribute("data-path")));
    expect(new Set(paths).size).toBe(files.length);

    const states = await page.getByTestId("git-state").allInnerTexts();
    expect(states).toHaveLength(files.length);
    expect(states).not.toContain("部分暂存");
    expect(states.every((state) => state === "已暂存")).toBe(true);

    // Four staged paths means the commit button has something to commit — the
    // same `.`-versus-space bug used to enable it from a file nobody staged.
    await expect(page.getByTestId("git-commit-submit")).toBeEnabled();

    // And the panel header agrees with git: four changes, not eight.
    await expect(page.getByTestId("git-panel")).toContainText("4 个改动");
  });

  test("a worktree-only change is not treated as staged", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // Commit a file, then modify it without staging: index `.`, worktree `M`.
    await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=tracked.txt`, "one\n");
    await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/stage`, { paths: ["tracked.txt"] });
    await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
      message: "add tracked",
      paths: [],
    });
    await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=tracked.txt`, "one\ntwo\n");

    await page.reload();
    await page.getByTestId("panel-tab-changes").click();
    const row = page.getByTestId("git-change").filter({ hasText: "tracked.txt" }).first();
    await expect(row).toBeVisible({ timeout: 30_000 });
    await expect(row).toHaveAttribute("data-state", "unstaged");
    await expect(page.getByTestId("git-state").filter({ hasText: "未暂存" })).toHaveCount(1);
    // Nothing is in the index, so the commit button must stay disabled.
    await expect(page.getByTestId("git-commit-submit")).toBeDisabled();
  });
});
