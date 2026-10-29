import { readFileSync } from "node:fs";

import { expect, test, type Page } from "@playwright/test";

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

/**
 * FR-2.7, the collision: a clone derives its branch from the source's title, so
 * cloning the same session twice used to produce the same `piforge/<label>` and
 * the same worktree path. The second clone was attached to the first clone's
 * checkout — two sessions writing one set of files — which is the opposite of
 * "a second attempt that cannot overwrite the first".
 *
 * The assertions are the three things a user would notice: a separate checkout
 * row, a separate branch, and a separate `pwd` for the agent.
 */
test("cloning one session twice gives each clone its own checkout", async ({ page }) => {
  await login(page, USER);
  const sourceId = await openNewSession(page);
  await sendPrompt(page, "clone me twice");

  const first = await apiWrite(page, "POST", `/v1/sessions/${sourceId}/clone`, {});
  expect(first.status).toBe(201);
  const second = await apiWrite(page, "POST", `/v1/sessions/${sourceId}/clone`, {});
  expect(second.status).toBe(201);

  const firstId = (first.body as { session: { id: string } }).session.id;
  const secondId = (second.body as { session: { id: string } }).session.id;
  expect(secondId).not.toBe(firstId);

  const left = (await (await page.request.get(`/v1/sessions/${firstId}`)).json()) as {
    session: { worktree_id: string };
  };
  const right = (await (await page.request.get(`/v1/sessions/${secondId}`)).json()) as {
    session: { worktree_id: string };
  };
  expect(left.session.worktree_id).toBeTruthy();
  expect(right.session.worktree_id).toBeTruthy();
  expect(right.session.worktree_id).not.toBe(left.session.worktree_id);

  // Distinct branches, both namespaced, and each checkout is on its own one.
  const branches = await Promise.all(
    [firstId, secondId].map(async (id) => {
      const status = await page.request.get(`/v1/sessions/${id}/git/status`);
      expect(status.status()).toBe(200);
      return ((await status.json()) as { branch: string }).branch;
    }),
  );
  expect(branches[0]).toMatch(/^piforge\//);
  expect(branches[1]).toMatch(/^piforge\//);
  expect(branches[1]).not.toBe(branches[0]);

  // The directory the agent's tools run in comes from the session file's header;
  // it must be a different checkout for each clone.
  const [leftCwd, rightCwd] = await Promise.all([
    sessionCwd(page, firstId),
    sessionCwd(page, secondId),
  ]);
  expect(leftCwd).toMatch(/[\\/]worktrees[\\/]/);
  expect(rightCwd).toMatch(/[\\/]worktrees[\\/]/);
  expect(rightCwd).not.toBe(leftCwd);
});

/** The cwd recorded in the session file Pi opens for this session. */
async function sessionCwd(page: Page, sessionId: string): Promise<string> {
  const res = await page.request.get(`/v1/sessions/${sessionId}/status`);
  expect(res.status()).toBe(200);
  const status = (await res.json()) as { session_file?: string };
  expect(status.session_file, `session ${sessionId} has no session file`).toBeTruthy();
  const first = readFileSync(status.session_file as string, "utf8").split("\n")[0];
  const header = JSON.parse(first) as { type: string; cwd?: string };
  expect(header.type).toBe("session");
  return header.cwd ?? "";
}