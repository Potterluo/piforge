import { expect, test } from "@playwright/test";

import { USER, apiWrite, clickWhenSettled, login, openNewSession } from "./helpers";

/**
 * The product's promise is "bring any OpenAI- or Anthropic-shaped endpoint", so
 * a model id the provider list has never seen must be usable without editing
 * anything by hand. Pi takes the id verbatim; what this spec pins is that the
 * product actually hands it over instead of quietly falling back to a default.
 */
test.describe("model management", () => {
  test("a provider's model list can be edited after creation", async ({ page }) => {
    await login(page, USER);
    const name = `edit-${Date.now()}`;

    const created = await apiWrite(page, "POST", "/v1/providers", {
      name,
      api: "openai-compat",
      base_url: "https://example.invalid/v1",
      api_key: "sk-e2e-not-a-real-key",
      models: [{ id: "first-model" }],
    });
    expect(created.status).toBeLessThan(300);

    await page.goto("/settings/providers");
    const row = page.locator(`[data-testid="provider-item"][data-name="${name}"]`);
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).toContainText("first-model");

    await clickWhenSettled(row.getByTestId("provider-edit"));
    await expect(page.getByTestId("edit-provider-models")).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("edit-provider-models").fill("first-model\nsecond-model");
    await clickWhenSettled(page.getByRole("button", { name: "保存" }));

    await expect(row).toContainText("second-model", { timeout: 20_000 });
    // Editing the list must not have dropped the key: that would turn a typo fix
    // into "paste your credential again".
    await expect(row).toContainText("已配置 key");
  });

  test("a custom model id typed in the composer is what the session uses", async ({ page }) => {
    await login(page, USER);
    const provider = `custom-${Date.now()}`;
    const modelId = `${provider}/whatever-the-endpoint-calls-it`;

    const created = await apiWrite(page, "POST", "/v1/providers", {
      name: provider,
      api: "openai-compat",
      base_url: "https://example.invalid/v1",
      api_key: "sk-e2e-not-a-real-key",
      models: [{ id: "seed-model" }],
    });
    expect(created.status).toBeLessThan(300);

    // Its own project: this test must not depend on whatever project another
    // spec happened to leave in first position.
    const sessionId = await openNewSession(page, `custom-model-${Date.now()}`);

    await clickWhenSettled(page.getByTestId("composer-model"));
    await clickWhenSettled(page.getByRole("option", { name: "自定义模型 id…" }));
    await page.getByTestId("custom-model-input").fill(modelId);
    await clickWhenSettled(page.getByTestId("custom-model-submit"));

    // The composer reflects it…
    await expect(page.getByTestId("composer-model")).toContainText(modelId, { timeout: 20_000 });

    // …and so does the session, which is what the control plane hands to Pi.
    const session = await page.request.get(`/v1/sessions/${sessionId}`);
    expect(session.status()).toBe(200);
    const body = (await session.json()) as { session: { model_ref: string } };
    expect(body.session.model_ref).toBe(modelId);
  });
});
