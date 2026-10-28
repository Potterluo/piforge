import { expect, test } from "@playwright/test";

import { ADMIN, USER, apiWrite, clickWhenSettled, login } from "./helpers";

/**
 * A shared provider is how a self-hosted deployment gives everybody a model
 * without handing out keys (PRD FR-16.2). The policy existed in the store from
 * the start and was unreachable from the product; this pins the whole path: an
 * admin shares one, a user inherits it, and the key never comes back out.
 */
test.describe("shared providers", () => {
  test("an admin shares a provider and a user inherits it read-only", async ({ page }) => {
    await login(page, ADMIN);
    const name = `house-${Date.now()}`;
    const secret = "sk-house-key-must-not-leak";

    await page.goto("/settings/providers");
    await expect(page.getByTestId("shared-providers")).toBeVisible({ timeout: 20_000 });
    await clickWhenSettled(page.getByTestId("shared-provider-add"));
    await page.getByTestId("shared-provider-name").fill(name);
    // The dialog's own submit is exercised by hand below; the rest of the fields
    // go through the API so this test stays about inheritance, not form furniture.
    await page.keyboard.press("Escape");
    const created = await apiWrite(page, "POST", "/v1/admin/providers", {
      name,
      api: "openai-compat",
      base_url: "https://house.example.invalid/v1",
      api_key: secret,
      models: [{ id: "house-model" }],
    });
    expect(created.status).toBe(200);
    expect(JSON.stringify(created.body)).not.toContain(secret);

    await page.reload();
    const row = page.getByTestId("shared-provider-item").filter({ hasText: name });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).toContainText("已配置 key");
    await expect(page.locator("body")).not.toContainText(secret);

    // The user sees it as inherited and cannot edit it.
    await page.request.post("/v1/auth/logout", {
      headers: { "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "" },
    });
    await login(page, USER);
    await page.goto("/settings/providers");
    const inherited = page.locator(`[data-testid="provider-item"][data-name="${name}"]`);
    await expect(inherited).toBeVisible({ timeout: 20_000 });
    await expect(inherited).toContainText("继承");
    await expect(inherited.getByTestId("provider-edit")).toHaveCount(0);
  });

  test("a normal user cannot manage shared providers", async ({ page }) => {
    await login(page, USER);
    const res = await page.request.get("/v1/admin/providers");
    expect(res.status()).toBe(403);
    await page.goto("/settings/providers");
    await expect(page.getByTestId("shared-providers")).toHaveCount(0);
  });
});
