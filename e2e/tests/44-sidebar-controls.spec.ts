import { expect, test } from "@playwright/test";

import { USER, ensureProject, login } from "./helpers";

/**
 * The two sidebar controls QA called dead: the project switcher and the account menu.
 * Both are exercised by clicking, because "it renders" was exactly what was wrong before.
 *
 * The switcher used to be a link to the project list that looked like a menu. Now
 * it is the menu the arrow promised: the current project plus the recent ones,
 * with 所有项目 as the way home.
 */
test("the sidebar's project switcher and account menu both work", async ({ page }) => {
  await login(page, USER);

  await page.getByTestId("account-menu").click();
  const panel = page.getByTestId("account-panel");
  await expect(panel).toBeVisible();
  await expect(panel).toContainText("设置");
  await expect(panel).toContainText("退出登录");
  // Clicking the email area again closes it rather than navigating anywhere.
  await page.getByTestId("account-menu").click();
  await expect(panel).toHaveCount(0);

  // From a project, the switcher opens a real menu of projects and the way back
  // to the full list.
  const projectId = await ensureProject(page, `e2e-switcher-${Date.now()}`);
  await page.goto(`/p/${projectId}`);
  await expect(page.getByTestId("current-project-name")).toBeVisible({ timeout: 20_000 });

  await page.getByTestId("project-switcher").click();
  const menu = page.locator('[data-slot="dropdown-menu-content"]');
  await expect(menu).toBeVisible();
  await expect(menu).toContainText("切换项目");
  await expect(page.getByTestId("project-switcher-item").first()).toBeVisible();

  await page.getByTestId("project-switcher-all").click();
  await expect(page).toHaveURL(/\/$/);
});
