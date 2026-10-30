import { expect, test } from "@playwright/test";

import { USER, apiWrite, openNewSession } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * QA-13 finding B: English errors in a Chinese UI.
 *
 * Both messages used to arrive verbatim from the API. The send error also told
 * the reader to go to "Settings → Models", a page that does not exist — the
 * screen is called 模型与凭据.
 */
test("a wrong password is reported in Chinese", async ({ page }) => {
  await page.goto("/login");
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill("definitely-not-the-password");
  await page.getByTestId("login-submit").click();

  const error = page.getByTestId("login-error");
  await expect(error).toBeVisible({ timeout: 20_000 });
  await expect(error).toContainText("邮箱或密码不正确");
  await expect(error).not.toContainText("invalid email or password");
});

test("a send with no usable provider names the real settings page, in Chinese", async ({
  page,
}) => {
  await freshAccount(page, "error-localization");
  await openNewSession(page);

  // The e2e deployment's fake agent does not call the model gateway, so it
  // cannot produce the real "provider has no key" failure. The wire error is
  // replayed exactly as the API sends it; what this test owns is the boundary
  // that renders it, not the backend's decision to reject.
  let intercepted = false;
  const wireMessage =
    'the provider "shared-anthropic" has no API key: add one in Settings → Models';
  await page.route("**/v1/sessions/*/messages", async (route) => {
    intercepted = true;
    await route.fulfill({
      status: 400,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "no_model", message: wireMessage } }),
    });
  });

  await page.getByTestId("composer-input").fill("你好");
  await page.getByTestId("composer-send").click();

  const toast = page.locator("[data-sonner-toast]").filter({ hasText: "发送失败" });
  await expect(toast).toBeVisible({ timeout: 20_000 });
  await expect(toast).toContainText("模型与凭据");
  await expect(toast).toContainText("API Key");
  // The exact regression: an English menu name in a Chinese sentence.
  await expect(toast).not.toContainText("Settings");
  await expect(toast).not.toContainText("Models");
  expect(intercepted).toBe(true);
});

test("a provider without a key reports that in Chinese", async ({ page }) => {
  await freshAccount(page, "error-localization");
  const name = `loc-nokey-${Date.now()}`;
  const created = await apiWrite(page, "POST", "/v1/providers", {
    name,
    api: "openai-compat",
    base_url: "https://example.invalid/v1",
  });
  expect(created.status).toBeLessThan(300);
  const id = (created.body as { provider: { id: string } }).provider.id;

  await page.goto("/settings/providers");
  const row = page.locator(`[data-testid="provider-item"][data-name="${name}"]`);
  await expect(row).toBeVisible({ timeout: 20_000 });
  await row.getByRole("button", { name: "测试" }).click();

  const toast = page.locator("[data-sonner-toast]").filter({ hasText: "连通性测试失败" });
  await expect(toast).toBeVisible({ timeout: 20_000 });
  await expect(toast).toContainText("还没有 API Key");
  await expect(toast).not.toContainText("this provider has no API key yet");

  await apiWrite(page, "DELETE", `/v1/providers/${id}`);
});
