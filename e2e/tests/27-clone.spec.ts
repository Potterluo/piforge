import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-2.7: a clone keeps the whole conversation and gives you a second attempt with its
 * own checkout. Unlike a fork it truncates nothing.
 */
test("cloning a session keeps every turn", async ({ page }) => {
  await login(page, USER);
  const sourceId = await openNewSession(page);
  await sendPrompt(page, "first question for the clone");

  const cloned = await apiWrite(page, "POST", `/v1/sessions/${sourceId}/clone`, {});
  expect(cloned.status).toBe(201);
  const clone = (cloned.body as { session: { id: string; kind: string; parent_id: string } }).session;
  expect(clone.kind).toBe("branch");
  expect(clone.parent_id).toBe(sourceId);

  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
  await page.goto(`/p/${projectId}/s/${clone.id}`);
  await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
  // The whole conversation came along, not a truncated fork point.
  await expect(page.getByText("first question for the clone")).toBeVisible({ timeout: 20_000 });

  // It has its own checkout, so the two attempts cannot overwrite each other.
  const detail = await (await page.request.get(`/v1/sessions/${clone.id}`)).json();
  const original = await (await page.request.get(`/v1/sessions/${sourceId}`)).json();
  expect((detail as { session: { worktree_id: string } }).session.worktree_id).not.toBe(
    (original as { session: { worktree_id: string } }).session.worktree_id,
  );
});