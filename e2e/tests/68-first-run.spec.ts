import { expect, test } from "@playwright/test";

import { ADMIN, apiWrite, ensureProject, login, openSession } from "./helpers";
import { withSharedProvidersClean } from "./isolation";

/**
 * First run is a step, not a dead composer (QA-16 / TASK 2).
 *
 * On a fresh volume there is no usable provider: every send answers 400
 * `no_model`, and the only explanation used to be a toast that vanished while
 * the text stayed in the input. This spec creates brand-new accounts and drives
 * the real first-run flow:
 *
 *   - a new user sees the setup panel on the home page and above the composer,
 *     with a link to 模型与凭据 and no admin-only button;
 *   - after an admin adds a shared provider (through the API here), the panel
 *     disappears for that user;
 *   - a new *admin* gets the one-click path, and adding the shared provider from
 *     the composer makes the panel disappear in place and on the home page.
 *
 * Shared providers are deployment-wide, so this test needs a deployment without
 * a *usable* one. It used to delete every shared provider it found, which
 * silently destroyed configuration another spec (or another person's run) had
 * created — and left its own behind for the next run. The seeded
 * `shared-anthropic` has no key, so it never counts as usable: the panel still
 * appears, and it is left alone. `withSharedProvidersClean` makes the
 * precondition explicit (only the seeded provider, or a failure naming the spec
 * that left one) and removes only what this test created, even when it throws.
 */
test("a brand-new account sees first-run setup on home and in the composer until a provider exists", async ({
  page,
  browser,
}) => {
  await withSharedProvidersClean(async () => {
    await login(page, ADMIN);

    // The provider this test creates through the API, so the second half can
    // remove it without touching anything it did not create.
    const created: string[] = [];
    const deleteCreated = async () => {
      for (const id of created.splice(0)) {
        const removed = await apiWrite(page, "DELETE", `/v1/admin/providers/${id}`);
        expect([200, 404]).toContain(removed.status);
      }
    };

    const stamp = Date.now();
    const userEmail = `first-run-user-${stamp}@example.test`;
    const userPassword = "first-run-user-pass-123";
    const createdUser = await apiWrite(page, "POST", "/v1/admin/users", {
      email: userEmail,
      password: userPassword,
      display_name: "First Run User",
      role: "user",
    });
    expect(createdUser.status).toBeLessThan(300);

    const userContext = await browser.newContext();
    const freshUser = await userContext.newPage();
    try {
      await login(freshUser, { email: userEmail, password: userPassword });

      // Home page: the setup panel is the first thing a new account sees.
      const homePanel = freshUser.getByTestId("first-run-panel");
      await expect(homePanel).toBeVisible({ timeout: 20_000 });
      await expect(homePanel.getByTestId("first-run-title")).toContainText("先配置一个模型服务");
      await expect(homePanel).toContainText("400 no_model");
      await expect(homePanel.getByTestId("first-run-settings-link")).toHaveAttribute(
        "href",
        "/settings/providers",
      );
      // Not an admin: no deployment-wide shortcut is offered.
      await expect(homePanel.getByTestId("first-run-add-shared")).toHaveCount(0);

      // Composer: the same panel above an input that cannot send yet.
      const projectId = await ensureProject(freshUser, `first-run-user-${stamp}`);
      await openSession(freshUser, projectId, "first run");
      await expect(freshUser.getByTestId("composer-input")).toBeVisible({ timeout: 20_000 });
      const composerPanel = freshUser.getByTestId("no-model-slot").getByTestId("first-run-panel");
      await expect(composerPanel).toBeVisible({ timeout: 20_000 });
      await expect(composerPanel.getByTestId("first-run-settings-link")).toBeVisible();

      // An admin fixes it deployment-wide. The user's next page load is ready.
      const shared = await apiWrite(page, "POST", "/v1/admin/providers", {
        name: `first-run-shared-${stamp}`,
        api: "openai-compat",
        base_url: "https://first-run.example.invalid/v1",
        api_key: "sk-first-run-shared",
        models: [{ id: "first-run-model" }],
      });
      expect(shared.status).toBe(200);
      created.push((shared.body as { provider: { id: string } }).provider.id);

      await freshUser.reload();
      await expect(freshUser.getByTestId("first-run-panel")).toHaveCount(0, { timeout: 20_000 });
      await freshUser.goto("/");
      await expect(freshUser.getByTestId("first-run-panel")).toHaveCount(0, { timeout: 20_000 });
    } finally {
      await userContext.close();
    }

    // The admin one-click path, from a second brand-new account. Remove the
    // provider the previous half created, otherwise this account is already ready.
    await deleteCreated();

    const adminEmail = `first-run-admin-${stamp}@example.test`;
    const adminPassword = "first-run-admin-pass-123";
    const createdAdmin = await apiWrite(page, "POST", "/v1/admin/users", {
      email: adminEmail,
      password: adminPassword,
      display_name: "First Run Admin",
      role: "admin",
    });
    expect(createdAdmin.status).toBeLessThan(300);

    const adminContext = await browser.newContext();
    const freshAdmin = await adminContext.newPage();
    try {
      await login(freshAdmin, { email: adminEmail, password: adminPassword });

      const homePanel = freshAdmin.getByTestId("first-run-panel");
      await expect(homePanel).toBeVisible({ timeout: 20_000 });
      await expect(homePanel.getByTestId("first-run-add-shared")).toBeVisible();

      // Also present in the composer, where the dead send used to happen.
      const projectId = await ensureProject(freshAdmin, `first-run-admin-${stamp}`);
      await openSession(freshAdmin, projectId, "first run admin");
      const composerPanel = freshAdmin.getByTestId("no-model-slot").getByTestId("first-run-panel");
      await expect(composerPanel).toBeVisible({ timeout: 20_000 });
      await expect(composerPanel.getByTestId("first-run-add-shared")).toBeVisible();

      await composerPanel.getByTestId("first-run-add-shared").click();
      const dialog = freshAdmin.getByTestId("first-run-dialog");
      await expect(dialog).toBeVisible();
      await freshAdmin.getByTestId("first-run-name").fill(`first-run-click-${stamp}`);
      await freshAdmin
        .getByTestId("first-run-base-url")
        .fill("https://first-run-click.example.invalid/v1");
      await freshAdmin.getByTestId("first-run-model").fill("first-run-click-model");
      await freshAdmin.getByTestId("first-run-api-key").fill("sk-first-run-click");
      await freshAdmin.getByTestId("first-run-submit").click();

      // No reload: the same queries the panel reads are invalidated by the flow.
      await expect(dialog).toHaveCount(0, { timeout: 20_000 });
      await expect(freshAdmin.getByTestId("first-run-panel")).toHaveCount(0, { timeout: 20_000 });

      await freshAdmin.goto("/");
      await expect(freshAdmin.getByTestId("first-run-panel")).toHaveCount(0, { timeout: 20_000 });
      await expect(freshAdmin.getByTestId("project-card").first()).toBeVisible({ timeout: 20_000 });
    } finally {
      await adminContext.close();
    }

    // The provider the dialog created is not tracked by id; the wrapper removes
    // whatever appeared during this test, so there is nothing to do here.
  });
});
