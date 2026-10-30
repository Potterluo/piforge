import { expect, test } from "@playwright/test";

import { USER, login, openNewSession } from "./helpers";

/**
 * FR-10.2: several terminal tabs (≥5), each with its own PTY, renameable and
 * closable.
 *
 * The transport already keyed PTYs by `?tab=`; the UI mounted one pane with the
 * fixed name `main`. So the user-visible assertions are: five tabs exist, each
 * pane carries a distinct PTY name, a command typed in one tab lands in *that*
 * shell, a label can be changed, and closing removes only that tab.
 */
test("five terminal tabs, each with its own shell, rename and close", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await page.getByTestId("panel-tab-terminal").click();

  // Starting state: one tab, one live shell.
  await expect(page.getByTestId("terminal-tab")).toHaveCount(1);
  await expect(page.getByTestId("terminal-pane").first()).toHaveAttribute(
    "data-pty-status",
    "open",
    { timeout: 30_000 },
  );

  for (let index = 0; index < 4; index += 1) {
    await page.getByTestId("terminal-tab-add").click();
  }
  await expect(page.getByTestId("terminal-tab")).toHaveCount(5);
  await expect(page.getByTestId("terminal-pane")).toHaveCount(5);

  const tabIds = await page
    .getByTestId("terminal-tab")
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-tab-id") ?? ""));
  expect(new Set(tabIds).size).toBe(5);

  // Every pane owns a different tab id — that id is the PTY key the server uses.
  const ptyTabs = await page
    .getByTestId("terminal-pane")
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-pty-tab") ?? ""));
  expect(new Set(ptyTabs).size).toBe(5);
  expect([...ptyTabs].sort()).toEqual([...tabIds].sort());

  // ...and every one of those PTYs really opened, not just rendered.
  await expect
    .poll(
      () =>
        page
          .getByTestId("terminal-pane")
          .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-pty-status"))),
      { timeout: 30_000 },
    )
    .toEqual(["open", "open", "open", "open", "open"]);

  // The newly added tab is the selected one.
  await expect(page.locator('[data-testid="terminal-tab"][data-tab-id="t5"]')).toHaveAttribute(
    "aria-selected",
    "true",
  );

  // Type into the selected tab: a real keystroke path, not just a socket probe.
  // Scoped to the tab's own panel: the follow pane keeps a hidden xterm surface
  // mounted in the same DOM, and `[data-slot=xterm-surface]` matches it too.
  const fifthSurface = page.locator(
    '[data-testid="terminal-tab-panel"][data-tab-id="t5"] [data-slot="xterm-surface"]',
  );
  await fifthSurface.click();
  await page.keyboard.type("echo five > tab-five.txt");
  await page.keyboard.press("Enter");
  await expect
    .poll(
      async () =>
        (await page.request.get(`/v1/sessions/${sessionId}/files?path=tab-five.txt`)).status(),
      { timeout: 30_000 },
    )
    .toBe(200);

  // Switch back to the first tab and run a different command there. Both files
  // existing proves the two tabs are two shells over one worktree.
  await page.locator('[data-testid="terminal-tab"][data-tab-id="t1"]').click();
  const firstSurface = page.locator(
    '[data-testid="terminal-tab-panel"][data-tab-id="t1"] [data-slot="xterm-surface"]',
  );
  await firstSurface.click();
  await page.keyboard.type("echo one > tab-one.txt");
  await page.keyboard.press("Enter");
  await expect
    .poll(
      async () =>
        (await page.request.get(`/v1/sessions/${sessionId}/files?path=tab-one.txt`)).status(),
      { timeout: 30_000 },
    )
    .toBe(200);
  expect(
    (await page.request.get(`/v1/sessions/${sessionId}/files?path=tab-five.txt`)).status(),
  ).toBe(200);

  // Rename: double click the label, type, Enter. The PTY id stays `t1`, so the
  // shell — and its scrollback — survives the rename.
  await page.locator('[data-testid="terminal-tab"][data-tab-id="t1"]').dblclick();
  const rename = page.getByTestId("terminal-tab-rename");
  await expect(rename).toBeVisible();
  await rename.fill("构建");
  await rename.press("Enter");
  await expect(page.locator('[data-testid="terminal-tab"][data-tab-id="t1"]')).toHaveText("构建");
  await expect(page.locator('[data-testid="terminal-pane"][data-pty-tab="t1"]')).toHaveAttribute(
    "data-pty-status",
    "open",
  );

  // Close: distinct tab, label and pane go away; the other four stay live.
  await page.locator('[data-testid="terminal-tab-close"][data-tab-id="t1"]').click();
  await expect(page.getByTestId("terminal-tab")).toHaveCount(4);
  await expect(page.locator('[data-testid="terminal-tab"][data-tab-id="t1"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="terminal-pane"][data-pty-tab="t1"]')).toHaveCount(0);
  await expect(page.getByTestId("terminal-pane")).toHaveCount(4);

  // The closed tab's file is still on disk: closing a tab is not deleting work.
  expect(
    (await page.request.get(`/v1/sessions/${sessionId}/files?path=tab-one.txt`)).status(),
  ).toBe(200);
});
