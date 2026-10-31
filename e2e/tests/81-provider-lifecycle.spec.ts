import { expect, test, type Page } from "@playwright/test";

import { apiWrite, ensureProject } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * A session has to survive a provider change (acceptance pass 3, HIGH).
 *
 * The tester deleted and re-created the deployment's provider while a session
 * existed; from then on every send to that session answered 500, the log said
 * "No API key found for the selected model", the header said
 * 跟随默认（unknown）, and retrying never helped — the stored `model_ref` named
 * a provider that no longer existed and nothing looked again.
 *
 * These two specs pin the two allowed outcomes:
 *
 *   - a working model exists: the next send re-resolves onto it, persists the
 *     new reference, says which model the turn used, and the header follows;
 *   - nothing usable exists: the failure names the provider and links to
 *     模型与凭据, and it is a 400 with a reason, never a bare 500.
 */

async function createProvider(page: Page, name: string, model: string): Promise<string> {
  const created = await apiWrite(page, "POST", "/v1/providers", {
    name,
    api: "openai-compat",
    base_url: "https://example.invalid/v1",
    api_key: "sk-e2e-provider-lifecycle",
    models: [{ id: model }],
  });
  expect(created.status, JSON.stringify(created.body)).toBeLessThan(300);
  return (created.body as { provider: { id: string } }).provider.id;
}

async function deleteProvider(page: Page, id: string): Promise<void> {
  const removed = await apiWrite(page, "DELETE", `/v1/providers/${id}`);
  expect([200, 204], JSON.stringify(removed.body)).toContain(removed.status);
}

/** A session pinned to `ref`, opened in its own project. */
async function openPinnedSession(
  page: Page,
  projectName: string,
  ref: string,
): Promise<{ projectId: string; sessionId: string }> {
  const projectId = await ensureProject(page, projectName);
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "provider lifecycle",
    model_ref: ref,
  });
  expect(created.status).toBeLessThan(300);
  const sessionId = (created.body as { session: { id: string } }).session.id;
  await page.goto(`/p/${projectId}/s/${sessionId}`);
  await expect(page.locator(`[data-session-id="${sessionId}"]`)).toBeVisible({ timeout: 40_000 });
  return { projectId, sessionId };
}

test.describe("a provider change while a session exists", () => {
  test("the next send re-resolves onto a working provider and says so", async ({ page }) => {
    await freshAccount(page, "provider-lifecycle");
    const stamp = Date.now();
    const gone = `gone-${stamp}`;
    const working = `working-${stamp}`;
    const goneId = await createProvider(page, gone, "gone-model");
    await createProvider(page, working, "working-model");

    const { sessionId } = await openPinnedSession(page, `lifecycle-${stamp}`, `${gone}/gone-model`);
    await expect(page.getByTestId("session-meta")).toContainText("gone-model", { timeout: 20_000 });

    // The provider the session pins disappears, exactly as in the acceptance run.
    await deleteProvider(page, goneId);

    const input = page.getByTestId("composer-input");
    await input.fill("after the provider was deleted");
    await page.getByTestId("composer-send").click();

    // The turn really runs, and the switch is announced with both model names.
    await expect(page.getByTestId("turn-user")).toHaveCount(1, { timeout: 30_000 });
    const switched = page
      .locator("[data-sonner-toast]")
      .filter({ hasText: "已自动切换模型" })
      .first();
    await expect(switched).toBeVisible({ timeout: 20_000 });
    await expect(switched).toContainText(gone);
    await expect(switched).toContainText("working-model");

    // The header and the picker agree with what actually ran, and never say
    // `unknown` (the acceptance pass's header did).
    const meta = page.getByTestId("session-meta");
    await expect(meta).toContainText("working-model", { timeout: 20_000 });
    await expect(meta).not.toContainText("unknown");
    await expect(page.getByTestId("composer-model")).toContainText("working-model", {
      timeout: 20_000,
    });

    // The session row carries the new reference, so the next send and a reload
    // do not fall back to the dead one.
    const detail = (await (await page.request.get(`/v1/sessions/${sessionId}`)).json()) as {
      session: { model_ref?: string };
    };
    expect(detail.session.model_ref).toBe(`${working}/working-model`);
  });

  test("without a fallback the error names the provider and links to 模型与凭据", async ({ page }) => {
    await freshAccount(page, "provider-lifecycle");
    const stamp = Date.now();
    const gone = `only-${stamp}`;
    const goneId = await createProvider(page, gone, "only-model");

    const { sessionId } = await openPinnedSession(page, `lifecycle-bare-${stamp}`, `${gone}/only-model`);

    // No usable provider is left anywhere for this account: the shared seeded
    // one has no key, and this was its only personal provider.
    await deleteProvider(page, goneId);

    await page.getByTestId("composer-input").fill("nobody left to ask");
    await page.getByTestId("composer-send").click();

    const failure = page.locator("[data-sonner-toast]").filter({ hasText: "发送失败" }).first();
    await expect(failure).toBeVisible({ timeout: 30_000 });
    await expect(failure).toContainText(gone);
    await expect(failure).toContainText("模型与凭据");
    // A refused turn is not a run, and the draft is still there to retry after
    // the operator fixes the provider.
    await expect(page.getByTestId("turn-user")).toHaveCount(0);
    await expect(page.getByTestId("composer-input")).toHaveValue("nobody left to ask");
    await expect(page.getByTestId("session-meta")).toContainText("未配置", { timeout: 20_000 });

    // Nothing was written to the row either.
    const detail = (await (await page.request.get(`/v1/sessions/${sessionId}`)).json()) as {
      session: { model_ref?: string };
    };
    expect(detail.session.model_ref).toBe(`${gone}/only-model`);
  });
});
