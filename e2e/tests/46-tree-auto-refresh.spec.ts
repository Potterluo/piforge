import { expect, test } from "@playwright/test";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

// Opening a PTY, typing into it and waiting for a debounced refetch is three
// round trips on top of the session setup; under a full-suite load the default
// 60s budget is the first thing to go.
test.setTimeout(120_000);

/**
 * QA-13 finding 1: the tree lied.
 *
 * A file created outside the editor — by the terminal or by the agent — did not
 * appear until someone clicked the small refresh icon; the panel said 目录为空
 * for as long as the user trusted it. And the empty state itself was a dead
 * end with nothing to do about being empty.
 *
 * Both tests here create the file through a path the app is not told about:
 * the terminal (a real PTY in the worktree) and the create-file action.
 */
test.describe("the file tree keeps itself current", () => {
  test("a file created in the terminal shows up without clicking refresh", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    // The first prompt provisions the worktree.
    await sendPrompt(page, "hello");

    await page.getByTestId("panel-tab-files").click();
    await expect(page.getByTestId("file-tree")).toBeVisible({ timeout: 30_000 });

    // A real shell in the worktree, writing a file the files API never sees.
    await page.getByTestId("panel-tab-terminal").click();
    await expect(page.getByTestId("terminal-pane")).toHaveAttribute("data-pty-status", "open", {
      timeout: 60_000,
    });
    // Focus the xterm input directly: clicking it re-checks the viewport, and
    // under a full-suite load the panel can still be settling its height.
    const input = page.locator(".xterm-helper-textarea").first();
    await input.focus();
    await page.keyboard.type("echo terminal-was-here > terminal-created.txt");
    await page.keyboard.press("Enter");

    // Back to the tree. No reload, no 刷新文件树: the PTY output is the signal.
    await page.getByTestId("panel-tab-files").click();
    await expect(
      page.locator('[data-testid="file-node"][data-path="terminal-created.txt"]'),
    ).toBeVisible({ timeout: 20_000 });
  });

  test("an empty directory offers creating a file, and the tree shows it", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    await sendPrompt(page, "hello");

    await page.getByTestId("panel-tab-files").click();
    await expect(page.getByTestId("file-tree")).toBeVisible({ timeout: 30_000 });

    // The empty state's action, not a dead end.
    const create = page.getByTestId("file-create");
    await expect(create).toBeVisible({ timeout: 20_000 });
    await create.click();

    await expect(page.getByTestId("file-create-dialog")).toBeVisible();
    await page.getByTestId("file-create-path").fill("from-the-empty-state.txt");
    await page.getByTestId("file-create-submit").click();

    await expect(
      page.locator('[data-testid="file-node"][data-path="from-the-empty-state.txt"]'),
    ).toBeVisible({ timeout: 20_000 });
    // Creating opens it, so the user lands somewhere useful.
    await expect(page.getByTestId("file-preview")).toBeVisible({ timeout: 20_000 });
  });
});
