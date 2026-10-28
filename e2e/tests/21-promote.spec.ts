import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-4.7: a side conversation that turned out to matter becomes a branch — with its
 * own checkout and its own transcript. The point is that the thought is not lost
 * when it graduates.
 */
test.describe("promote a sidecar", () => {
  test("promoting keeps the conversation and gives it its own worktree", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "parent session");

    const created = await apiWrite(page, "POST", `/v1/sessions/${parentId}/sidecar`, {});
    expect(created.status).toBeLessThan(300);
    const sidecarId = (created.body as { session: { id: string } }).session.id;

    await sendPromptOn(page, sidecarId, "a thought worth keeping");

    const promoted = await apiWrite(page, "POST", `/v1/sessions/${sidecarId}/promote`, {});
    expect(promoted.status).toBe(201);
    const branch = (promoted.body as { session: { id: string; kind: string } }).session;
    expect(branch.kind).toBe("branch");
    expect(branch.id).not.toBe(sidecarId);

    // The conversation came along.
    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${branch.id}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
    await expect(page.getByText("a thought worth keeping")).toBeVisible({ timeout: 20_000 });

    // The sidecar is archived rather than deleted: the record stays.
    const sidecar = await (await page.request.get(`/v1/sessions/${sidecarId}`)).json();
    expect((sidecar as { session: { status: string } }).session.status).toBe("archived");

    // And only a sidecar can be promoted.
    const refused = await apiWrite(page, "POST", `/v1/sessions/${parentId}/promote`, {});
    expect(refused.status).toBe(400);
  });
});

/** Sends a prompt directly to a session (the composer only drives the open one). */
async function sendPromptOn(
  page: import("@playwright/test").Page,
  sessionId: string,
  text: string,
): Promise<void> {
  const sent = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `promote-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    text,
  });
  expect(sent.status).toBeLessThan(300);
  // Wait for the run to settle so the transcript exists before promoting.
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const status = await (await page.request.get(`/v1/sessions/${sessionId}/status`)).json();
    if ((status as { running?: boolean }).running === false) return;
    await page.waitForTimeout(250);
  }
}