import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * FR-12.3: a comment on a diff line becomes a steering message.
 *
 * What is asserted, in the order a user hits it:
 *
 *  1. the diff offers a comment affordance on an addressable line, and the
 *     editor quotes that line;
 *  2. saving keeps the comment as a draft, and the diff shows a marker/count for
 *     the line (so a comment is findable without scrolling back);
 *  3. a reload keeps the unsent draft — the draft lives in the session's
 *     localStorage, not in component state;
 *  4. sending posts through the ordinary message endpoint, clears the draft, and
 *     the transcript (which is built from the *agent's* event stream, not from
 *     what the browser thinks it sent) shows `path:line`, the quoted code and
 *     the comment. That is the evidence the agent received a prompt containing
 *     the quote.
 */
test("a line comment survives a reload and reaches the agent with its quote", async ({ page }) => {
  const root = mkdtempSync(path.join(tmpdir(), "pf-comment-"));
  const seed = path.join(root, "seed");
  const bare = path.join(root, "origin.git");
  execFileSync("git", ["init", "--bare", "-b", "main", bare], { cwd: root, encoding: "utf8" });
  execFileSync("git", ["clone", bare, seed], { cwd: root, encoding: "utf8" });

  const lines = Array.from({ length: 12 }, (_, i) => `line ${i}`);
  writeFileSync(path.join(seed, "f.txt"), lines.join("\n") + "\n");
  execFileSync("git", ["add", "-A"], { cwd: seed });
  execFileSync("git", ["-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-m", "seed"], {
    cwd: seed,
  });
  execFileSync("git", ["push", "origin", "main"], { cwd: seed });

  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `comments-${Date.now()}`,
    repo_url: bare,
    default_branch: "main",
  });
  const projectId = (project.body as { project: { id: string } }).project.id;
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "inline comments",
  });
  const sessionId = (created.body as { session: { id: string } }).session.id;

  // One edited line, so the diff has a hunk and the new-file line number is 3.
  const edited = lines.slice();
  edited[2] = "line 2 EDITED-FOR-COMMENT";
  const wrote = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=f.txt`,
    edited.join("\n") + "\n",
  );
  expect(wrote.status).toBeLessThan(300);

  const openDiff = async () => {
    await page.getByTestId("panel-tab-changes").click();
    const change = page.locator('[data-testid="git-change"][data-path="f.txt"]');
    await expect(change).toBeVisible({ timeout: 30_000 });
    await change.click();
    const row = page.locator('[data-testid="diff-line-row"][data-new-line="3"]');
    await expect(row).toBeVisible({ timeout: 30_000 });
    return row;
  };

  await page.goto(`/p/${projectId}/s/${sessionId}`);
  const row = await openDiff();

  // --- 1. the affordance is on the line, and the editor quotes it ----------
  await row.hover();
  await row.getByTestId("diff-line-comment").click();
  const editor = page.getByTestId("review-comment-editor");
  await expect(editor).toBeVisible();
  await expect(editor).toContainText("f.txt:3");
  await expect(page.getByTestId("review-comment-quote")).toContainText("line 2 EDITED-FOR-COMMENT");

  const comment = "这一行不要动：客户端依赖原来的文案。";
  await page.getByTestId("review-comment-input").fill(comment);

  // --- 2. saving shows a marker and a count -------------------------------
  await page.getByTestId("review-comment-save").click();
  await expect(page.getByTestId("review-comment-row")).toContainText(comment);
  await expect(page.getByTestId("diff-comment-count")).toContainText("1 条行内评论");
  await expect(page.locator('[data-testid="diff-line-marker"][data-line="3"]')).toHaveAttribute(
    "data-count",
    "1",
  );

  // --- 3. a reload keeps the unsent draft ---------------------------------
  await page.reload();
  await openDiff();
  await expect(page.getByTestId("review-comment-row")).toContainText(comment, { timeout: 20_000 });
  await expect(page.getByTestId("diff-comment-count")).toContainText("1 条行内评论");

  // --- 4. sending turns it into the agent's prompt ------------------------
  const posted = page.waitForResponse(
    (res) =>
      res.url().includes(`/v1/sessions/${sessionId}/messages`) &&
      res.request().method() === "POST",
    { timeout: 30_000 },
  );
  await page.getByTestId("review-comment-row-send").click();
  const response = await posted;
  const payload = await response.json();
  expect(response.ok()).toBeTruthy();
  expect(JSON.stringify(payload)).toContain("accepted");
  // The draft is gone once it is sent: offering it again would send it twice.
  await expect(page.getByTestId("review-comment-row")).toHaveCount(0);
  await expect(page.getByTestId("diff-comment-count")).toHaveCount(0);

  // The transcript is fed by the agent's own event stream, so a user turn that
  // carries the anchor, the quote and the comment is what "the agent received a
  // prompt containing the quote" looks like from the outside.
  const turn = page.getByTestId("turn-user").first();
  await expect(turn).toContainText("行内评论 · f.txt:3", { timeout: 30_000 });
  await expect(turn).toContainText("line 2 EDITED-FOR-COMMENT");
  await expect(turn).toContainText(comment);
  await expect(turn).toContainText("评论：");
});
