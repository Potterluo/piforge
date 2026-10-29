import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * The model picker has to change the model (QA-14, MAJOR).
 *
 * It used to write the session row and nothing else: the live agent kept the
 * model it spawned with, so picking a provider with no key still answered from
 * the old one. Now a switch either takes effect on the next turn (a new process)
 * or fails in Chinese before anything runs — never a silent fallback.
 */
test.describe("the composer model picker", () => {
  test("picking a provider with no key fails clearly instead of answering", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    await sendPrompt(page, "hello there");
    const answeredBefore = await page.getByTestId("turn-assistant").count();
    expect(answeredBefore).toBeGreaterThan(0);

    // A provider that lists a model but has no key: the shape the QA pass picked.
    const provider = `e2enokey${Date.now()}`;
    const created = await apiWrite(page, "POST", "/v1/providers", {
      name: provider,
      api: "openai-compat",
      base_url: "https://example.invalid/v1",
      models: [{ id: "ghost-model" }],
    });
    expect(created.status).toBeLessThan(300);

    // The composer's option list is a cached provider query; reload so the new
    // provider is in it, the way an operator who just saved it would see it.
    await page.reload();
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });

    await page.getByTestId("composer-model").click();
    const option = page.getByRole("option", { name: new RegExp(`${provider}/`) }).first();
    await option.waitFor({ timeout: 20_000 });
    await option.click();
    // The switch is announced, because the next turn restarts the agent.
    await expect(page.locator("[data-sonner-toast]").filter({ hasText: "已切换模型" }).first()).toBeVisible({
      timeout: 20_000,
    });
    // The picker and the session agree about the choice.
    await expect(page.getByTestId("composer-model")).toContainText("ghost-model", { timeout: 20_000 });

    const usersBefore = await page.getByTestId("turn-user").count();
    await page.getByTestId("composer-input").fill("are you there?");
    await page.getByTestId("composer-send").click();

    const failure = page.locator("[data-sonner-toast]").filter({ hasText: "发送失败" }).first();
    await failure.waitFor({ timeout: 30_000 });
    await expect(failure).toContainText("还没有 API Key");
    // The turn did not run at all: no prompt, no answer, no silent old model.
    await expect(page.getByTestId("turn-user")).toHaveCount(usersBefore);
    await expect(page.getByTestId("turn-assistant")).toHaveCount(answeredBefore);
    await expect(page.getByTestId("turn-error")).toHaveCount(0);
  });

  test("switching back to the default model works again", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello there");

    const provider = `e2enokey${Date.now()}`;
    const created = await apiWrite(page, "POST", "/v1/providers", {
      name: provider,
      api: "openai-compat",
      base_url: "https://example.invalid/v1",
      models: [{ id: "ghost-model" }],
    });
    expect(created.status).toBeLessThan(300);
    const patched = await apiWrite(page, "PATCH", `/v1/sessions/${sessionId}`, {
      model_ref: `${provider}/ghost-model`,
    });
    expect(patched.status).toBeLessThan(300);

    // Back to 跟随默认 through the picker, then a normal turn.
    await page.getByTestId("composer-model").click();
    const inherit = page.getByRole("option", { name: "跟随默认" }).first();
    await inherit.waitFor({ timeout: 20_000 });
    await inherit.click();

    await sendPrompt(page, "still working?");
    await expect(page.getByTestId("turn-error")).toHaveCount(0);

    await expect
      .poll(
        async () => {
          const detail = (await (await page.request.get(`/v1/sessions/${sessionId}`)).json()) as {
            session: { model_ref?: string };
          };
          return detail.session.model_ref ?? "";
        },
        { timeout: 20_000 },
      )
      .toBe("");
  });
});
