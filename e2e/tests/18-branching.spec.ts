import { expect, test, type Page } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * Forking carries the conversation from the fork point — but only from a point
 * that has history behind it. Pi forks by truncating at the parent of the chosen
 * user message, so forking at the *first* message yields an empty branch. The
 * transcript has to agree with the model about that; showing a copied conversation
 * the agent never had was the bug a user test caught ("I wasn't asked to remember
 * any word — this is the first message in our conversation", while the UI displayed
 * the parent's turns).
 */
test.describe("branching", () => {
  test("forking a later message keeps the conversation", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "remember the word pineapple");
    await sendPrompt(page, "now say something else");

    const entryId = await forkPoint(page, parentId, 1);
    const childId = await fork(page, parentId, entryId, "branch with history");

    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${childId}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
    await expect(page.getByText("remember the word pineapple")).toBeVisible({ timeout: 20_000 });
  });

  test("forking the first message is honest about having no history", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "the only message here");

    // Pi truncates at the parent of the chosen message; the first one has none, so
    // the branch is empty by design. The transcript must not invent one.
    const childId = await fork(page, parentId, await forkPoint(page, parentId, 0), "fresh branch");

    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${childId}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
    await expect(page.getByText("还没有消息")).toBeVisible({ timeout: 20_000 });
  });

  test("the sidebar keeps the main session as the parent of its branch", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "parent of a branch");

    const created = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
      entry_id: await forkPoint(page, parentId, 0),
      label: "child branch",
    });
    expect(created.status).toBeLessThan(300);

    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${parentId}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });

    const actions = page.locator(`[data-session-actions="${parentId}"]`);
    await expect(actions).toHaveCount(1, { timeout: 20_000 });
    await page.getByTestId("session-link").first().hover();
    await actions.click();
    await expect(page.getByTestId("session-rename")).toBeVisible({ timeout: 20_000 });
  });
});

/** Waits for the fork points the agent reports, and returns the one at `index`. */
async function forkPoint(page: Page, sessionId: string, index: number): Promise<string> {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const body = (await (
      await page.request.get(`/v1/sessions/${sessionId}/fork-points`)
    ).json()) as { points: { entry_id: string }[] };
    if (body.points.length > index) return body.points[index].entry_id;
    await page.waitForTimeout(500);
  }
  throw new Error(`no fork point at index ${index}`);
}

async function fork(
  page: Page,
  parentId: string,
  entryId: string,
  label: string,
): Promise<string> {
  const created = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
    entry_id: entryId,
    label,
  });
  expect(created.status).toBeLessThan(300);
  return (created.body as { session: { id: string } }).session.id;
}