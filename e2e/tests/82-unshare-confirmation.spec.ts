import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { ADMIN, apiWrite, clickWhenSettled, login } from "./helpers";
import { withSharedProvidersClean } from "./isolation";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-unshare");
mkdirSync(ARTIFACTS, { recursive: true });

interface SharedProviderBody {
  providers: { id: string; name: string; has_key: boolean }[];
}

async function sharedProviders(page: import("@playwright/test").Page): Promise<SharedProviderBody["providers"]> {
  const listed = await page.request.get("/v1/admin/providers");
  expect(listed.status()).toBe(200);
  return ((await listed.json()) as SharedProviderBody).providers;
}

/**
 * Unsharing is destructive: it removes a provider every account inherits, and
 * during acceptance pass 4 a single click on the icon removed the deployment's
 * only working provider (acceptance4/stage1.md). This spec pins the guard:
 *
 *   - the wrong API endpoint answers a typed `shared_provider` error that names
 *     the admin endpoint, instead of 404 not_found;
 *   - the row's action opens a confirmation naming the provider and its
 *     consequences;
 *   - cancelling changes nothing (neither the list nor the API);
 *   - confirming removes it — and the admin API can create it again, which is
 *     how the deployment is left as found.
 */
test.describe("unsharing a shared provider", () => {
  test("asks first, and a cancel leaves the provider in place", async ({ page }) => {
    await withSharedProvidersClean(async () => {
      await login(page, ADMIN);
      const name = `unshare-${Date.now()}`;
      const secret = "sk-unshare-e2e-must-not-leak";

      const created = await apiWrite(page, "POST", "/v1/admin/providers", {
        name,
        api: "openai-compat",
        base_url: "https://unshare.example.invalid/v1",
        api_key: secret,
        models: [{ id: "unshare-model" }],
      });
      expect(created.status, JSON.stringify(created.body)).toBe(200);
      const providerId = (created.body as { provider: { id: string } }).provider.id;

      // The per-user endpoint is the wrong one for a shared provider. It must
      // explain where to go instead of pretending the row does not exist.
      const wrongRoute = await apiWrite(page, "DELETE", `/v1/providers/${providerId}`);
      expect(wrongRoute.status).toBe(409);
      const error = (wrongRoute.body as { error: { code: string; message: string } }).error;
      expect(error.code).toBe("shared_provider");
      expect(error.message).toContain(`/v1/admin/providers/${providerId}`);
      expect(error.message).toContain(name);
      expect(await sharedProviders(page)).toEqual(
        expect.arrayContaining([expect.objectContaining({ id: providerId })]),
      );

      await page.goto("/settings/providers");
      const row = page.getByTestId("shared-provider-item").filter({ hasText: name });
      await expect(row).toBeVisible({ timeout: 20_000 });

      // The action is explicit about its scope, and it opens a dialog rather
      // than deleting.
      await clickWhenSettled(row.getByTestId("shared-provider-unshare"));
      const dialog = page.getByTestId("shared-provider-unshare-dialog");
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText(name);
      await expect(dialog).toContainText("所有用户");
      await expect(dialog).toContainText("下一次发送");
      await page.screenshot({ path: path.join(ARTIFACTS, "1-dialog.png") });

      await page.getByTestId("shared-provider-unshare-cancel").click();
      await expect(dialog).toHaveCount(0);
      await page.screenshot({ path: path.join(ARTIFACTS, "2-after-cancel.png") });

      // Cancel did not change anything: same row, same API entry.
      await expect(row).toBeVisible();
      expect(await sharedProviders(page)).toEqual(
        expect.arrayContaining([expect.objectContaining({ id: providerId, has_key: true })]),
      );

      // Confirming is the only path that removes it.
      await clickWhenSettled(row.getByTestId("shared-provider-unshare"));
      await expect(dialog).toBeVisible();
      await page.getByTestId("shared-provider-unshare-confirm").click();
      await expect(
        page.locator("[data-sonner-toast]").filter({ hasText: "已取消共享" }),
      ).toBeVisible({ timeout: 20_000 });
      await expect(row).toHaveCount(0);
      await page.screenshot({ path: path.join(ARTIFACTS, "3-after-confirm.png") });
      expect((await sharedProviders(page)).map((provider) => provider.id)).not.toContain(providerId);

      // Put the deployment back the way this spec found it, through the admin
      // API — the recovery action an operator needs after a real mistake.
      const recreated = await apiWrite(page, "POST", "/v1/admin/providers", {
        name,
        api: "openai-compat",
        base_url: "https://unshare.example.invalid/v1",
        api_key: secret,
        models: [{ id: "unshare-model" }],
      });
      expect(recreated.status, JSON.stringify(recreated.body)).toBe(200);
      const restoredId = (recreated.body as { provider: { id: string } }).provider.id;
      expect(restoredId).not.toBe(providerId);
      expect(await sharedProviders(page)).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ id: restoredId, name, has_key: true }),
        ]),
      );

      // …and remove this spec's temporary provider so the next run starts from
      // the seeded shared set (the wrapper would also catch it).
      const cleaned = await apiWrite(page, "DELETE", `/v1/admin/providers/${restoredId}`);
      expect([200, 204]).toContain(cleaned.status);
    });
  });
});
