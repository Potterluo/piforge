import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-truth");
mkdirSync(ARTIFACTS, { recursive: true });

/**
 * The changes panel must follow a write made through the terminal.
 *
 * The acceptance pass ran `echo … >> square.js` in the real PTY and 变更 kept
 * saying 干净 / 0 until the refresh button was pressed. The session event stream
 * cannot carry a shell command — that socket is a byte relay, not an event — but
 * the terminal already tells the workbench when it prints something (the file
 * tree refreshes from exactly that signal). The git panel simply was not
 * subscribed to it.
 *
 * No polling and no manual refresh: the panel is opened before the write, the
 * document is never reloaded, and the row has to appear on its own.
 */
test.describe("changes panel after a terminal write", () => {
  test("a file written in the terminal shows up without pressing 刷新", async ({ page }) => {
    test.setTimeout(150_000);
    await login(page, USER);
    const sessionId = await openNewSession(page);
    // The first prompt provisions the worktree.
    await sendPrompt(page, "hello");

    // A tracked file, committed, so the terminal edit is "M count.js" — a row
    // the panel can only show if it re-reads git status.
    const seeded = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("count.js")}`,
      "let n = 1;\n",
    );
    expect(seeded.status).toBeLessThan(300);
    const commit = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
      message: "seed count.js",
      paths: ["count.js"],
    });
    expect(commit.status).toBe(200);
    // Setup reload: the panel's cached status was fetched while count.js was
    // untracked and nothing invalidated it on this API-only commit. The
    // assertion below still happens in the document opened here.
    await page.reload();
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });

    await page.getByTestId("panel-tab-changes").click();
    await expect(page.getByTestId("git-panel")).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('[data-testid="git-change"]')).toHaveCount(0, { timeout: 20_000 });

    // A marker a reload would wipe: the update must happen in this document.
    await page.evaluate(() => {
      (window as unknown as { __changesPanelMounted?: boolean }).__changesPanelMounted = true;
    });

    // A real shell command in the real PTY.
    await page.getByTestId("panel-tab-terminal").click();
    await expect(page.getByTestId("terminal-pane").first()).toHaveAttribute(
      "data-pty-status",
      "open",
      { timeout: 30_000 },
    );
    // The socket being open is not the shell being ready: typing before the
    // prompt is painted lost the command in an earlier run. Wait for the prompt
    // the way a person does.
    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              document.querySelector('[data-slot="xterm-surface"] .xterm-rows')?.textContent ?? "",
          ),
        { timeout: 30_000 },
      )
      .toContain(">");
    const surface = page
      .locator('[data-testid="terminal-tab-panel"] [data-slot="xterm-surface"]')
      .first();
    await surface.click();
    // Spaces around `>>`: PowerShell parses `CHANGE>>count.js` as one argument
    // and only echoes it. The shell in this environment is PowerShell, so the
    // command is written the way it actually redirects there. (PowerShell writes
    // UTF-16, so the file becomes binary to the preview API — git still sees a
    // modified tracked file, which is what the panel reports.)
    await page.keyboard.type("echo CHANGE >> count.js", { delay: 20 });
    await page.keyboard.press("Enter");
    await page.screenshot({ path: path.join(ARTIFACTS, "88-terminal-typed.png") });

    // The command really ran: git reports the modification before the panel is
    // even looked at, so a green test cannot be the panel inventing a row.
    await expect
      .poll(
        async () => {
          const res = await page.request.get(`/v1/sessions/${sessionId}/git/status`);
          if (res.status() !== 200) return false;
          const body = (await res.json()) as { changes?: { path: string }[] };
          return (body.changes ?? []).some((change) => change.path === "count.js");
        },
        { timeout: 30_000 },
      )
      .toBe(true);

    // Back to the panel that was mounted before the write. No 刷新 click.
    await page.getByTestId("panel-tab-changes").click();
    const row = page.locator('[data-testid="git-change"][data-path="count.js"]');
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("git-panel")).toContainText("1 个改动");
    await page.screenshot({ path: path.join(ARTIFACTS, "88-terminal-changes.png") });

    expect(
      await page.evaluate(
        () => (window as unknown as { __changesPanelMounted?: boolean }).__changesPanelMounted,
      ),
    ).toBe(true);
  });
});
