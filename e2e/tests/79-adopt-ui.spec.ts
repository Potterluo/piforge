import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-accept2");
mkdirSync(ARTIFACTS, { recursive: true });

/**
 * Forking is effortless in this product; merging the result back was not
 * reachable. The acceptance pass opened the *main* session's ⋯ menu, saw 对比,
 * and concluded adopt did not exist — the only affordance was a hover menu item
 * on the branch's own sidebar row, with no confirmation at all.
 *
 * This test drives the path a reviewer actually takes: it opens the branch's
 * page, uses the visible header action, reads the confirmation (source →
 * target, commits, files), adopts, and then proves the file is on the target
 * and the toast says how the work travelled. The backend merge is already
 * covered by 28-adopt.spec.ts; this spec is about reachability and the words on
 * screen.
 */
test("a branch is adopted from the session header, with a confirmation and an outcome", async ({
  page,
}) => {
  await login(page, USER);
  const main = await openNewSession(page);
  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
  expect(projectId).not.toBe("");
  await sendPrompt(page, "the parent of an adopted branch");

  // A branch with one commit of its own. Created through the API so the test is
  // about the adopt UI, not the fork dialog (05-branches covers forking).
  const cloned = await apiWrite(page, "POST", `/v1/sessions/${main}/clone`, {});
  expect(cloned.status).toBe(201);
  const branch = (cloned.body as { session: { id: string } }).session.id;

  const write = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${branch}/files?path=${encodeURIComponent("branch-note.txt")}`,
    "from the branch\n",
  );
  expect(write.status).toBeLessThan(300);
  const commit = await apiWrite(page, "POST", `/v1/sessions/${branch}/git/commit`, {
    message: "branch commits its file",
    paths: ["branch-note.txt"],
  });
  expect(commit.status).toBe(200);

  // The main session is not a branch and must not offer the action.
  expect(await page.getByTestId("session-adopt-open").count()).toBe(0);

  await page.goto(`/p/${projectId}/s/${branch}`);
  await expect(page.locator(`[data-session-id="${branch}"]`)).toBeVisible({ timeout: 40_000 });

  // The visible affordance, in the branch's own header.
  const open = page.getByTestId("session-adopt-open");
  await expect(open).toBeVisible({ timeout: 30_000 });
  await open.click();

  const dialog = page.getByTestId("adopt-dialog");
  await expect(dialog).toBeVisible({ timeout: 20_000 });
  await expect(dialog).toContainText("采纳这个分支");
  // Source branch → target session, both named.
  await expect(dialog).toContainText("来源");
  await expect(dialog).toContainText("目标");
  await expect(dialog.getByTestId("adopt-target-session")).not.toBeEmpty();
  // What comes over: one commit and its file.
  await expect(dialog.getByTestId("adopt-commits")).toContainText("1 个");
  await expect(dialog.getByTestId("adopt-commits")).toContainText("branch commits its file");
  await expect(dialog.getByTestId("adopt-files")).toContainText("branch-note.txt");
  await page.screenshot({ path: path.join(ARTIFACTS, "79-adopt-confirm.png"), fullPage: true });

  await page.getByTestId("adopt-submit").click();

  // The outcome names how the work travelled; the summary names the file.
  await expect(page.getByText(/采纳完成：(快进合并|合并提交)/)).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText(/1 个文件：branch-note\.txt/)).toBeVisible({ timeout: 20_000 });
  await page.screenshot({ path: path.join(ARTIFACTS, "79-adopt-done.png"), fullPage: true });

  // And it really is on the target, not only in the toast.
  const read = await page.request.get(`/v1/sessions/${main}/files?path=branch-note.txt`);
  expect(read.status()).toBe(200);
  expect(((await read.json()) as { body: string }).body).toContain("from the branch");
});
