import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  ADMIN,
  USER,
  apiWrite,
  clickWhenSettled,
  ensureProject,
  login,
  sendPrompt,
} from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-qa16");
mkdirSync(ARTIFACTS, { recursive: true });

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

  /**
   * Editing a shared provider in place (QA-16, FIX A).
   *
   * The admin page used to offer only 取消共享, so a wrong base URL or a rotated
   * key forced delete + recreate — which changes the row id every session pins
   * ("provider/model") and broke all of them. The edit must keep the id and the
   * name, keep the stored key when the field is left empty, and leave a session
   * that references the provider able to run.
   */
  test("an admin edits a shared provider and a pinned session still works", async ({ page }) => {
    await login(page, ADMIN);
    const stamp = Date.now();
    const name = `qa16-shared-${stamp}`;
    const secret = "sk-qa16-shared-key";

    const shared = await apiWrite(page, "POST", "/v1/admin/providers", {
      name,
      api: "openai-compat",
      base_url: "https://house-one.example.invalid/v1",
      api_key: secret,
      models: [{ id: "house-model" }],
    });
    expect(shared.status).toBe(200);
    const providerId = (shared.body as { provider: { id: string } }).provider.id;

    // A session pinned to this provider/model, proven to run before the edit.
    const projectId = await ensureProject(page, `qa16-shared-proj-${stamp}`);
    const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
      title: "pinned to the shared provider",
      model_ref: `${name}/house-model`,
    });
    expect(created.status).toBeLessThan(300);
    const sessionId = (created.body as { session: { id: string } }).session.id;
    const sessionUrl = `/p/${projectId}/s/${sessionId}`;
    await page.goto(sessionUrl);
    await expect(page.locator(`[data-session-id="${sessionId}"]`)).toBeVisible({ timeout: 40_000 });
    await sendPrompt(page, "before the shared provider edit");

    // The row now offers an edit path, and it opens prefilled.
    await page.goto("/settings/providers");
    await page.reload();
    const row = page.getByTestId("shared-provider-item").filter({ hasText: name });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await clickWhenSettled(row.getByTestId("shared-provider-edit"));
    const edit = page.getByTestId("shared-provider-edit-dialog");
    await expect(edit).toBeVisible();
    await expect(page.getByTestId("shared-provider-edit-base")).toHaveValue(
      "https://house-one.example.invalid/v1",
    );
    await expect(page.getByTestId("shared-provider-edit-models")).toHaveValue("house-model");
    // Leaving the key empty is the documented "keep the stored key".
    await expect(page.getByTestId("shared-provider-edit-key")).toHaveValue("");

    await page.getByTestId("shared-provider-edit-base").fill("https://house-two.example.invalid/v1");
    await page
      .getByTestId("shared-provider-edit-models")
      .fill("house-model\nhouse-model-2");
    await page.screenshot({ path: path.join(ARTIFACTS, "A-shared-provider-edit-dialog.png") });
    await page.getByTestId("shared-provider-edit-submit").click();

    const toast = page.locator("[data-sonner-toast]").filter({ hasText: "共享 provider 已更新" });
    await expect(toast).toBeVisible({ timeout: 20_000 });
    await page.reload();
    await expect(row).toContainText("https://house-two.example.invalid/v1", { timeout: 20_000 });
    await expect(row).toContainText("house-model-2");
    await expect(row).toContainText("已配置 key");

    // Same row, same id, same name — and the stored key survived the key-less edit.
    const listed = await page.request.get("/v1/admin/providers");
    const providers = ((await listed.json()) as { providers: { id: string; name: string; base_url: string; has_key: boolean; models: { id: string }[] }[] }).providers;
    const edited = providers.find((provider) => provider.name === name);
    expect(edited?.id).toBe(providerId);
    expect(edited?.base_url).toBe("https://house-two.example.invalid/v1");
    expect(edited?.has_key).toBe(true);
    expect(edited?.models.map((model) => model.id)).toEqual(["house-model", "house-model-2"]);

    // And the pinned session still resolves the provider: it runs again.
    await page.goto(sessionUrl);
    await expect(page.locator(`[data-session-id="${sessionId}"]`)).toBeVisible({ timeout: 40_000 });
    await sendPrompt(page, "after the shared provider edit");
    await page.screenshot({ path: path.join(ARTIFACTS, "A-pinned-session-after-edit.png") });

    // Clean up the deployment-wide config and the project this test created.
    const gone = await apiWrite(page, "DELETE", `/v1/admin/providers/${providerId}`);
    expect(gone.status).toBe(200);
    const deleted = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
    expect(deleted.status).toBeLessThan(300);
  });
});
