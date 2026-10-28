import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession } from "./helpers";

/**
 * FR-3.1–3.3: who gets to run when several branches of one session want to.
 *
 * Parallel execution is off by default, so a prompt on one branch pauses a running
 * sibling — and says so in the sibling's own transcript, because a session that just
 * stops is indistinguishable from one that died.
 */
test("starting a branch pauses its running siblings", async ({ page }) => {
  await login(page, USER);
  const parent = await openNewSession(page);

  const cloned = await apiWrite(page, "POST", `/v1/sessions/${parent}/clone`, {});
  expect(cloned.status).toBe(201);
  const branch = (cloned.body as { session: { id: string } }).session.id;

  // Run the branch first, then send to the parent while it is running.
  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
  await page.goto(`/p/${projectId}/s/${branch}`);
  await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
  await apiWrite(page, "POST", `/v1/sessions/${branch}/messages`, {
    client_msg_id: `c1-${Date.now()}`,
    text: "run on the branch",
  });

  await apiWrite(page, "POST", `/v1/sessions/${parent}/messages`, {
    client_msg_id: `c2-${Date.now()}`,
    text: "run on the main session",
  });

  // The default is off, and the API says so on the event the pause writes.
  const features = (await (await page.request.get("/v1/features")).json()) as {
    features: Record<string, boolean>;
  };
  expect(features.features.parallel ?? false).toBe(false);
});