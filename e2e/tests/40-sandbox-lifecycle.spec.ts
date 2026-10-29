import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-5.2 / FR-5.9: the sandbox state machine existed and nothing could move it. Pausing
 * stops the agent too — otherwise "paused" describes a label, not a machine — and opening
 * the session again wakes it without a detour through a button.
 */
test("a paused sandbox wakes when the session is used again", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  const before = await (await page.request.get(`/v1/sessions/${sessionId}/sandbox`)).json();
  expect((before as { state: string }).state).toBe("READY");

  const paused = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/sandbox/pause`, {});
  expect(paused.status).toBe(200);
  expect((paused.body as { state: string }).state).toBe("PAUSED");

  const afterPause = await (await page.request.get(`/v1/sessions/${sessionId}/sandbox`)).json();
  expect((afterPause as { state: string }).state).toBe("PAUSED");

  // Sending a message wakes it: the user asked to use the session, which is unambiguous.
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `wake-${Date.now()}`,
    text: "after the pause",
  });

  await expect
    .poll(
      async () => {
        const state = await (await page.request.get(`/v1/sessions/${sessionId}/sandbox`)).json();
        return (state as { state: string }).state;
      },
      { timeout: 40_000 },
    )
    .toBe("READY");
});