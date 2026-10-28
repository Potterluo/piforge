import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * A finished session has to be manageable: rename it, archive it, export it,
 * delete it. None of the four existed, so the list could only grow.
 */
test.describe("session actions", () => {
  test("rename from the sidebar reaches the header and survives a reload", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    await page.getByTestId("session-link").first().hover();
    await page.getByTestId("session-actions").first().click();
    await page.getByTestId("session-rename").click();
    await page.getByTestId("session-title-input").fill("重命名后的会话");
    await page.getByTestId("session-rename-submit").click();

    await expect(page.getByTestId("session-title")).toContainText("重命名后的会话", {
      timeout: 20_000,
    });

    await page.reload();
    await expect(page.getByTestId("session-title")).toContainText("重命名后的会话", {
      timeout: 20_000,
    });
    const res = await page.request.get(`/v1/sessions/${sessionId}`);
    expect(((await res.json()) as { session: { title: string } }).session.title).toBe(
      "重命名后的会话",
    );
  });

  test("archive hides it from the list until asked for", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    const list = await (await page.request.get("/v1/sessions")).json();
    const target = (list as { sessions: { id: string; title: string }[] }).sessions[0];

    await page.getByTestId("session-link").first().hover();
    await page.getByTestId("session-actions").first().click();
    await page.getByTestId("session-archive").click();

    await expect(page.getByTestId("session-link")).toHaveCount(0, { timeout: 20_000 });
    await page.getByTestId("toggle-archived").click();
    await expect(page.getByTestId("session-link").first()).toBeVisible({ timeout: 20_000 });

    const res = await page.request.get(`/v1/sessions/${target.id}`);
    expect(((await res.json()) as { session: { status: string } }).session.status).toBe("archived");
  });

  test("export downloads a markdown transcript", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "export this please");

    const res = await page.request.get(`/v1/sessions/${sessionId}/export?format=md`);
    expect(res.status()).toBe(200);
    expect(res.headers()["content-disposition"]).toContain("attachment");
    const body = await res.text();
    expect(body.startsWith("# ")).toBe(true);
    expect(body).toContain("export this please");
  });

  test("delete removes it and returns to the project", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "delete me");

    await page.getByTestId("session-link").first().hover();
    await page.getByTestId("session-actions").first().click();
    await page.getByTestId("session-delete").click();
    await page.getByTestId("session-delete-confirm").click();

    await expect
      .poll(async () => (await page.request.get(`/v1/sessions/${sessionId}`)).status(), {
        timeout: 20_000,
      })
      .toBe(404);
  });
});