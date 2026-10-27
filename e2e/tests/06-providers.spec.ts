import { expect, test } from "@playwright/test";
import { apiWrite, apiHeaders, clickWhenSettled, login, USER } from "./helpers";

/**
 * Bring-your-own-model is the product's core promise (PRD §5.7), and the one
 * property that must never regress is that the key the user types never comes
 * back out — not in a response, not in the UI.
 */
test.describe("providers", () => {
  test("a key is stored sealed and never returned", async ({ page }) => {
    await login(page, USER);
    const name = `prov-${Date.now()}`;
    const secret = "sk-ant-api03-e2e-secret-value-123";

    const created = await apiWrite(page, "POST", "/v1/providers", {
      name,
      api: "anthropic-messages",
      api_key: secret,
    });
    expect(created.status).toBeLessThan(300);
    expect(JSON.stringify(created.body)).not.toContain(secret);

    const listed = await page.request.get("/v1/providers");
    expect(listed.status()).toBe(200);
    const payload = await listed.text();
    expect(payload).not.toContain(secret);
    expect(payload).toContain(name);
  });

  test("the settings page creates a provider through the form", async ({ page }) => {
    await login(page, USER);
    const name = `ui-prov-${Date.now()}`;

    await page.goto("/settings/providers");
    const add = page.getByTestId("provider-add").or(page.getByTestId("provider-add-empty"));
    await expect(add).toBeVisible({ timeout: 20_000 });

    await clickWhenSettled(add);
    await expect(page.getByTestId("provider-name")).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("provider-name").fill(name);
    await page.getByTestId("provider-api-key").fill("sk-test-not-a-real-key");
    await clickWhenSettled(page.getByTestId("provider-submit"));

    // The row appears and reports that a key is on file, without showing it.
    const row = page.locator(`[data-testid="provider-item"][data-name="${name}"]`);
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).toContainText("已配置 key");
    await expect(row).not.toContainText("sk-test-not-a-real-key");
  });

  test("a provider without a key is flagged as missing one", async ({ page }) => {
    await login(page, USER);
    const name = `nokey-${Date.now()}`;

    const created = await apiWrite(page, "POST", "/v1/providers", {
      name,
      api: "openai-compat",
      base_url: "https://example.invalid/v1",
    });
    expect(created.status).toBeLessThan(300);

    await page.goto("/settings/providers");
    const row = page.locator(`[data-testid="provider-item"][data-name="${name}"]`);
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).toContainText("缺少 key");
  });

  test("a cross-site provider write is refused without the CSRF token", async ({ page }) => {
    await login(page, USER);
    const headers = await apiHeaders(page);
    // Same request, minus the double-submit token: this is what a cross-site
    // form post looks like, and it must not create anything.
    const res = await page.request.fetch("/v1/providers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      data: { name: `csrf-${Date.now()}`, api: "openai-compat", api_key: "x" },
    });
    expect(res.status()).toBe(403);
    expect(headers["X-PF-CSRF"]).not.toBe("");
  });
});
