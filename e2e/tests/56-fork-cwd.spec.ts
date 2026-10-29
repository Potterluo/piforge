import { readFileSync } from "node:fs";

import { expect, test, type Page } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * A fork or a clone must run in its own checkout (QA-14, blocker).
 *
 * Pi does not run its tools in the process working directory: it takes the
 * directory from the *session file's header*. A fork is written by the parent's
 * process, so its header carried the parent's `<project>/repo` cwd and the branch
 * ran `bash` on `main` — commits landed on the parent's branch while the UI showed
 * `piforge/<branch>` and an untouched worktree. A clone copies the parent's file
 * byte for byte, header included, so it had the same problem.
 *
 * The assertion is the file Pi reads, taken before any prompt: the fake peer
 * rewrites it from its own process cwd when it persists, which would hide the
 * bug if the test spoke first.
 */
test.describe("a branch's agent cwd", () => {
  test("a fork runs in the branch worktree, not the parent's repo", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "first message");
    await sendPrompt(page, "second message");
    const parentCwd = await sessionCwd(page, parentId);
    expect(parentCwd).toMatch(/[\\/]repo$/);

    const label = `forkcwd${Date.now()}`;
    const entryId = await forkPoint(page, parentId, 1);
    const forked = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
      entry_id: entryId,
      label,
    });
    expect(forked.status).toBeLessThan(300);
    const forkId = (forked.body as { session: { id: string } }).session.id;

    // Read before opening the page: no prompt has run in the branch yet, so the
    // header is exactly what the control plane handed to Pi.
    const forkCwd = await sessionCwd(page, forkId);
    expect(forkCwd).not.toBe(parentCwd);
    expect(forkCwd).toMatch(/[\\/]worktrees[\\/]/);
    expect(forkCwd).toContain(label);

    // The checkout behind that cwd really is the branch the UI names. The branch
    // carries a short session-id suffix, so two forks of one session cannot
    // collide on a title (FR-2.7).
    const status = await page.request.get(`/v1/sessions/${forkId}/git/status`);
    expect(status.status()).toBe(200);
    const branch = (await status.json()) as { branch: string };
    expect(branch.branch).toMatch(new RegExp(`^piforge/${label}-[a-z0-9]+$`));
  });

  test("a clone runs in its own worktree, not the parent's repo", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "clone me");
    const parentCwd = await sessionCwd(page, parentId);
    expect(parentCwd).toMatch(/[\\/]repo$/);

    const cloned = await apiWrite(page, "POST", `/v1/sessions/${parentId}/clone`, {});
    expect(cloned.status).toBeLessThan(300);
    const cloneId = (cloned.body as { session: { id: string } }).session.id;

    const cloneCwd = await sessionCwd(page, cloneId);
    expect(cloneCwd).not.toBe(parentCwd);
    expect(cloneCwd).toMatch(/[\\/]worktrees[\\/]/);
  });
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
