import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession } from "./helpers";

/**
 * FR-1.10: a scratch session is for a quick question. It stays out of the sidebar
 * and expires on its own, so asking something does not leave an artifact.
 */
test("a scratch session is hidden from the list and marked in the header", async ({ page }) => {
  await login(page, USER);
  // A normal session first, so the project has something to compare against.
  await openNewSession(page);
  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";

  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "quick question",
    ephemeral: true,
  });
  expect(created.status).toBe(201);
  const scratch = (created.body as { session: { id: string; ephemeral?: boolean } }).session;
  expect(scratch.ephemeral).toBe(true);

  // Not in the list the sidebar draws from...
  // The sidebar reads GET /v1/sessions (with an optional ?project=), not a
  // project-scoped route — asserting against a route that does not exist would
  // pass no matter what the server did.
  const list = (await (await page.request.get(`/v1/sessions?project=${projectId}`)).json()) as {
    sessions?: { id: string }[];
  };
  const ids = (list.sessions ?? []).map((item) => item.id);
  expect(ids).not.toContain(scratch.id);

  // ...but reachable by id, and it says what it is.
  await page.goto(`/p/${projectId}/s/${scratch.id}`);
  await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
  await expect(page.getByTestId("ephemeral-badge")).toBeVisible({ timeout: 20_000 });

  // And it is not in the sidebar either.
  await expect(page.locator(`[data-session-actions="${scratch.id}"]`)).toHaveCount(0);
});