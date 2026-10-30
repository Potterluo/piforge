import { expect, test } from "@playwright/test";

import { ADMIN, apiWrite, ensureProject, login, openSession } from "./helpers";

/**
 * A fresh account has no provider. Its first message used to die with 400
 * `no_model`, shown only as a toast that vanished, while the composer kept the
 * text and the header said 跟随默认. The failure now has a permanent home:
 *
 *   - before typing, the composer says this account has no usable model and
 *     links to 模型与凭据;
 *   - after a refused send, the same bar carries the server's reason and the
 *     message is still in the input;
 *   - the header says 未配置, never `unknown`.
 */
test("a new account sees the missing model before and after sending", async ({ page, browser }) => {
  // An admin creates the account through the API: this spec is about the first
  // message, not the admin dialog.
  await login(page, ADMIN);
  const stamp = Date.now();
  const email = `ux-nomodel-${stamp}@example.test`;
  const password = "ux-nomodel-pass-123";
  const created = await apiWrite(page, "POST", "/v1/admin/users", {
    email,
    password,
    display_name: "No model",
    role: "user",
  });
  expect(created.status).toBeLessThan(300);

  const context = await browser.newContext();
  const fresh = await context.newPage();
  try {
    await login(fresh, { email, password });

    // A project and a session are all a user can still create without a model.
    const projectId = await ensureProject(fresh, `ux-nomodel-${stamp}`);
    await openSession(fresh, projectId, "no model");

    // The header states a conclusion, not an internal value.
    const meta = fresh.getByTestId("session-meta");
    await expect(meta).toContainText("未配置", { timeout: 20_000 });
    await expect(meta).not.toContainText("unknown");

    // The bar is there *before* anything is typed.
    const bar = fresh.getByTestId("no-model-bar");
    await expect(bar).toBeVisible({ timeout: 20_000 });
    await expect(bar).toContainText("这个账号还没有可用模型");
    await expect(fresh.getByTestId("no-model-settings-link")).toHaveAttribute(
      "href",
      "/settings/providers",
    );
    await expect(fresh.getByTestId("composer-input")).toHaveValue("");

    // A send is refused for the same reason. The fake peer does not need a
    // model, so the refusal itself is injected with the exact 400 `no_model`
    // body the deployed control plane returns (verified against :7722).
    await fresh.route("**/v1/sessions/**/messages", (route) =>
      route.fulfill({
        status: 400,
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "no_model",
            message: 'the provider "volc" is not configured: add it in Settings → Models',
          },
        }),
      }),
    );
    const message = "你好，先介绍一下你能做什么";
    await fresh.getByTestId("composer-input").fill(message);
    await fresh.getByTestId("composer-send").click();

    await expect(fresh.getByTestId("no-model-error")).toBeVisible({ timeout: 20_000 });
    await expect(fresh.getByTestId("no-model-error")).toContainText("模型与凭据");
    await expect(fresh.getByTestId("composer-input")).toHaveValue(message);
    // Nothing was accepted, so no user turn was recorded.
    await expect(fresh.getByTestId("turn-user")).toHaveCount(0);
  } finally {
    await context.close();
  }
});
