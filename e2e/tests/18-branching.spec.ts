import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * A branch is a continuation, not a restart: it has to open with the conversation
 * it came from. Pi forked the agent's session file from the beginning, so the
 * model had the history — but the transcript is this control plane's event log,
 * which started empty. Asking the branch about the parent conversation produced
 * "I never saw that", which is the worst possible answer.
 */
test.describe("branching", () => {
  test("a branch opens with the conversation it was forked from", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "remember the word pineapple");

    // Fork at the user message (the only kind Pi accepts). The list can lag the
    // run by a moment while the agent finishes settling, so poll for it.
    const forkPoint = async (): Promise<string> => {
      for (let attempt = 0; attempt < 20; attempt += 1) {
        const points = (await (
          await page.request.get(`/v1/sessions/${parentId}/fork-points`)
        ).json()) as { points: { entry_id: string }[] };
        const last = points.points.at(-1);
        if (last?.entry_id) return last.entry_id;
        await page.waitForTimeout(500);
      }
      throw new Error("no fork point appeared for a settled run");
    };
    const entryId = await forkPoint();

    const forked = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
      entry_id: entryId,
      label: "branch with history",
    });
    expect(forked.status).toBeLessThan(300);
    const childId = (forked.body as { session: { id: string } }).session.id;

    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${childId}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });

    // The transcript shows the parent's message, so the branch is not amnesiac.
    await expect(page.getByText("remember the word pineapple")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("还没有消息")).toHaveCount(0);
  });

  test("the sidebar keeps the main session as the parent of its branch", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "parent of a branch");

    const points = (await (
      await page.request.get(`/v1/sessions/${parentId}/fork-points`)
    ).json()) as { points: { entry_id: string }[] };
    const created = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
      entry_id: points.points.at(-1)!.entry_id,
      label: "child branch",
    });
    expect(created.status).toBeLessThan(300);

    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${parentId}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });

    // The row actions belong to the main session, and the branch is nested under
    // it — the grouping once made the newest child the parent, which mis-bound
    // every per-row action.
    const actions = page.locator(`[data-session-actions="${parentId}"]`);
    await expect(actions).toHaveCount(1, { timeout: 20_000 });
    await page.getByTestId("session-link").first().hover();
    await actions.click();
    await expect(page.getByTestId("session-rename")).toBeVisible({ timeout: 20_000 });
  });
});
