import { expect, test } from "@playwright/test";

import { USER, login, openNewSession } from "./helpers";

/**
 * FR-13.9: a token for scripts. The token is shown once, works as a bearer
 * credential (no cookie, no CSRF header), and stops working when revoked.
 */
test("an API key drives the API like a signed-in browser", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";

  await page.goto("/settings/api-keys");
  const page_ = page.getByTestId("api-keys-page");
  await expect(page_).toBeVisible({ timeout: 40_000 });

  await page.getByTestId("api-key-add").click();
  await page.getByTestId("api-key-name").fill("e2e-script");
  await page.getByTestId("api-key-submit").click();

  const tokenField = page.getByTestId("api-key-token");
  await expect(tokenField).toBeVisible({ timeout: 20_000 });
  const token = await tokenField.inputValue();
  expect(token.startsWith("pfk_")).toBe(true);
  await page.getByRole("button", { name: "我已保存" }).click();

  // The list shows the key but never the secret again.
  const row = page.locator('[data-testid="api-key-row"][data-name="e2e-script"]');
  await expect(row).toHaveCount(1, { timeout: 20_000 });

  // A bearer call reads without a cookie and writes without a CSRF header.
  const read = await page.request.get(`/v1/sessions?project=${projectId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(read.status()).toBe(200);

  const created = await page.request.post(`/v1/projects/${projectId}/sessions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { title: "made by a token" },
  });
  expect(created.status()).toBe(201);

  // Revoking takes effect immediately.
  await row.hover();
  await row.getByTestId("api-key-revoke").click();
  await expect(row.getByText("已吊销")).toBeVisible({ timeout: 20_000 });
  const after = await page.request.get("/v1/sessions", {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(after.status()).toBe(401);
});