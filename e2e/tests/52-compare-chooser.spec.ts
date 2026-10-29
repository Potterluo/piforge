import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * QA-13 finding D: 对比 on a single session compared it with itself.
 *
 * The old action fell back to `session.id` when the session had no parent, so
 * the URL was `ids=<x>,<x>` and the page rendered two identical columns under
 * "对比 2 个尝试". The action now opens a chooser, and the route rejects a
 * duplicate id instead of drawing it.
 */
test("compare from one session offers the other sessions and never compares it with itself", async ({
  page,
}) => {
  await login(page, USER);

  const created = await apiWrite(page, "POST", "/v1/projects", {
    name: `compare-chooser-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  expect(created.status).toBeLessThan(300);
  const projectId = (created.body as { project: { id: string } }).project.id;

  const first = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "尝试 A",
  });
  expect(first.status).toBeLessThan(300);
  const firstId = (first.body as { session: { id: string } }).session.id;

  const second = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "尝试 B",
  });
  expect(second.status).toBeLessThan(300);
  const secondId = (second.body as { session: { id: string } }).session.id;

  await page.goto(`/p/${projectId}/s/${firstId}`);
  await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
  await expect(page.locator(`[data-session-actions="${firstId}"]`)).toBeVisible({
    timeout: 30_000,
  });

  await page.locator(`[data-session-actions="${firstId}"]`).click();
  await page.getByTestId("session-compare").click();

  const chooser = page.getByTestId("compare-chooser");
  await expect(chooser).toBeVisible();
  // The session being compared is not a candidate for itself.
  await expect(page.locator(`[data-testid="compare-candidate"][data-session-id="${firstId}"]`)).toHaveCount(0);
  const candidate = page.locator(`[data-testid="compare-candidate"][data-session-id="${secondId}"]`);
  await expect(candidate).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId("compare-start")).toBeDisabled();

  await candidate.click();
  await expect(candidate).toHaveAttribute("aria-pressed", "true");
  await page.getByTestId("compare-start").click();

  await expect(page).toHaveURL(
    new RegExp(`/p/${projectId}/compare\\?ids=${firstId},${secondId}$`),
    { timeout: 20_000 },
  );
  const columns = page.getByTestId("compare-column");
  await expect(columns).toHaveCount(2, { timeout: 40_000 });
  await expect(page.locator(`[data-testid="compare-column"][data-session="${firstId}"]`)).toHaveCount(1);
  await expect(page.locator(`[data-testid="compare-column"][data-session="${secondId}"]`)).toHaveCount(1);
  await expect(page.getByTestId("compare-invalid")).toHaveCount(0);

  // A hand-written duplicate URL is rejected with a reason, not drawn twice.
  await page.goto(`/p/${projectId}/compare?ids=${firstId},${firstId}`);
  await expect(page.getByTestId("compare-invalid")).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId("compare-invalid")).toContainText("不能拿同一个会话和自己对比");
  await expect(page.getByTestId("compare-column")).toHaveCount(0);

  // The API refuses the same thing, so the guard is not only in the UI.
  const refused = await page.request.get(`/v1/sessions/compare?ids=${firstId},${firstId}`);
  expect(refused.status()).toBe(400);
  expect((await refused.json()).error.code).toBe("invalid_argument");
});
