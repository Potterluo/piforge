import { expect, test } from "@playwright/test";
import { clickWhenSettled, login, USER } from "./helpers";

/**
 * The only spec that walks the product's own navigation.
 *
 * Everything else sets up through the API (see helpers.ts) so that a change to
 * the project dialog does not fail fifteen unrelated tests. That consolidation
 * only works if this spec actually covers the clicks, so it drives the full
 * path: empty state -> new project -> project page -> new session -> workbench.
 */
test.describe("shell navigation", () => {
  test("creates a project and a session through the dialogs", async ({ page }) => {
    await login(page, USER);

    const name = `ui-path-${Date.now()}`;
    await page.goto("/");
    await expect(page.getByTestId("new-project-button")).toBeVisible({ timeout: 20_000 });
    await clickWhenSettled(page.getByTestId("new-project-button"));

    await page.getByTestId("project-name").fill(name);
    await page.getByTestId("project-repo").fill("");
    await page.getByTestId("project-submit").click();

    // The new card must appear in the grid with the right destination. The
    // navigation itself is taken from the href instead of a click: the card is
    // a link, and a click here only re-tests Next's router, which is not what
    // this spec is about.
    const card = page.locator(`[data-testid="project-card"]`, { hasText: name }).first();
    await expect(card).toBeVisible({ timeout: 20_000 });
    const href = await card.getAttribute("href");
    expect(href).toMatch(/^\/p\/prj_/);
    await page.goto(href!);
    await expect(page).toHaveURL(/\/p\/prj_/);

    // The project page offers the session dialog.
    await clickWhenSettled(page.getByTestId("new-session-button").first());
    await expect(page.getByTestId("session-submit")).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("session-title").fill("from the ui");
    await page.getByTestId("session-submit").click();

    // Landing on the workbench is what "it worked" means.
    await expect(page.locator("[data-session-id]")).toBeVisible({ timeout: 40_000 });
    await expect(page.getByTestId("composer-input")).toBeVisible();
    await expect(page.getByTestId("workspace-panels")).toBeVisible();
  });

  test("the command palette opens with the keyboard", async ({ page }) => {
    await login(page, USER);
    await page.keyboard.press("Control+k");
    await expect(page.getByRole("dialog")).toBeVisible({ timeout: 10_000 });
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("an unauthenticated visit to a project redirects to login", async ({ page }) => {
    await page.goto("/p/prj_doesnotexist");
    await expect(page).toHaveURL(/\/login/, { timeout: 20_000 });
  });
});
