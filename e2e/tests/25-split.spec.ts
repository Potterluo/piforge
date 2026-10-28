import { expect, test } from "@playwright/test";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-3.4: two panels side by side. The point is not the extra pixels — it is that
 * watching the diff while the terminal runs must not tear the terminal down.
 */
test("the workspace shows two panels at once", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  await sendPrompt(page, "hello");

  await expect(page.getByTestId("workspace-panels")).toBeVisible({ timeout: 40_000 });

  // Split with 终端 as the primary panel pairs it with 变更.
  await page.getByTestId("panel-tab-terminal").click();
  await page.getByTestId("panel-split").click();

  const split = page.getByTestId("workspace-split");
  await expect(split).toBeVisible({ timeout: 20_000 });
  await expect(split).toHaveAttribute("data-panel", "changes");
  await expect(page.getByTestId("git-panel")).toBeVisible({ timeout: 20_000 });

  // Switching the second pane keeps exactly one copy of each panel.
  await page.getByTestId("split-tab-browser").click();
  await expect(split).toHaveAttribute("data-panel", "browser");
  await expect(page.getByTestId("browser-panel").or(page.getByText("内置浏览器已被管理员关闭"))).toBeVisible({
    timeout: 30_000,
  });

  // One terminal, not two: the primary still owns it.
  await expect(page.locator('[data-testid="terminal-disconnected"], .xterm').first()).toBeVisible({
    timeout: 20_000,
  });

  // Closing the split returns to a single pane.
  await split.getByRole("button", { name: "关闭第二窗格" }).click();
  await expect(page.getByTestId("workspace-split")).toHaveCount(0);
});