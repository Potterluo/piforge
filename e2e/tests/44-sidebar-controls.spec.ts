import { expect, test } from "@playwright/test";

import { USER, login } from "./helpers";

/**
 * The two sidebar controls QA called dead: the project switcher and the account menu.
 * Both are exercised by clicking, because "it renders" was exactly what was wrong before.
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

  // The switcher goes to the project list; a menu that opened nothing was the bug.
  await page.getByTestId("project-switcher").click();
  await expect(page).toHaveURL(/\/$/);
});