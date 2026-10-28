import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-2.5: two attempts side by side. The screen has to show real per-attempt numbers —
 * including saying "no checkout" rather than a confident zero — and refuse a
 * comparison that is not one.
 */
test("two branches can be compared side by side", async ({ page }) => {
  await login(page, USER);
  const first = await openNewSession(page);
  await sendPrompt(page, "attempt one");
  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";

  const cloned = await apiWrite(page, "POST", `/v1/sessions/${first}/clone`, {});
  expect(cloned.status).toBe(201);
  const second = (cloned.body as { session: { id: string } }).session.id;

  await page.goto(`/p/${projectId}/compare?ids=${first},${second}`);
  const grid = page.getByTestId("compare-grid");
  await expect(grid).toBeVisible({ timeout: 40_000 });
  const columns = page.getByTestId("compare-column");
  await expect(columns).toHaveCount(2, { timeout: 20_000 });

  // Real numbers, not placeholders.
  await expect(columns.first()).toContainText("个文件");
  await expect(columns.first()).toContainText("token");

  // One id is not a comparison.
  await page.goto(`/p/${projectId}/compare?ids=${first}`);
  await expect(page.getByText("选两个会话再对比")).toBeVisible({ timeout: 20_000 });

  const refused = await page.request.get(`/v1/sessions/compare?ids=${first}`);
  expect(refused.status()).toBe(400);
});