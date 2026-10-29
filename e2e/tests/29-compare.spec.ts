import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-2.5: two attempts side by side. This spec used to assert the *word* 个文件,
 * which is why "0 个文件 / +0 -0" for a branch full of commits stayed green. It now
 * asserts the numbers themselves:
 *
 *   - a branch whose work is committed shows committed files and lines,
 *   - a branch with only uncommitted work is still counted,
 *   - an untouched branch is honestly zero,
 *   - a clone's inherited turns/tokens are not reported as zero.
 */
type CompareRowBody = {
  session_id: string;
  user_messages: number;
  tokens_in: number;
  tokens_out: number;
  inherited_user_messages: number;
  inherited_tokens_in: number;
  files_changed: number;
  additions: number;
  deletions: number;
  committed_files: number;
  committed_additions: number;
  uncommitted_files: number;
  uncommitted_additions: number;
  diff_error?: string;
};

test("two branches can be compared side by side with real numbers", async ({ page }) => {
  await login(page, USER);
  const main = await openNewSession(page);
  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
  // One real turn, so the clones have a transcript (and usage) to inherit.
  await sendPrompt(page, "attempt one");

  const cloneOne = await apiWrite(page, "POST", `/v1/sessions/${main}/clone`, {});
  expect(cloneOne.status).toBe(201);
  const committedBranch = (cloneOne.body as { session: { id: string } }).session.id;
  // A clone's branch name is derived from the source label, so two clones of the
  // same session would collide on one checkout. Give the second one its own label
  // (and therefore its own worktree) so there is a genuinely untouched attempt.
  const renamed = await apiWrite(page, "PATCH", `/v1/sessions/${main}`, {
    title: "compare trunk two",
  });
  expect(renamed.status).toBe(200);
  const cloneTwo = await apiWrite(page, "POST", `/v1/sessions/${main}/clone`, {});
  expect(cloneTwo.status).toBe(201);
  const untouchedBranch = (cloneTwo.body as { session: { id: string } }).session.id;

  // The first branch commits one file through the product's own commit endpoint,
  // then leaves a second file uncommitted. The old compare screen saw neither the
  // commit nor the untracked file and reported 0 个文件 / +0 -0.
  const committedWrite = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${committedBranch}/files?path=committed.txt`,
    "one\ntwo\n",
  );
  expect(committedWrite.status).toBeLessThan(300);
  const commit = await apiWrite(page, "POST", `/v1/sessions/${committedBranch}/git/commit`, {
    message: "the branch commits its work",
    paths: ["committed.txt"],
  });
  expect(commit.status).toBe(200);
  const dirtyWrite = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${committedBranch}/files?path=still-dirty.txt`,
    "not committed\n",
  );
  expect(dirtyWrite.status).toBeLessThan(300);

  const fetchRows = async (): Promise<CompareRowBody[]> => {
    const res = await page.request.get(
      `/v1/sessions/compare?ids=${main},${committedBranch},${untouchedBranch}`,
    );
    expect(res.status()).toBe(200);
    return ((await res.json()) as { rows: CompareRowBody[] }).rows;
  };

  // Tokens land when the run settles in the pump; wait for the inherited numbers
  // rather than racing it.
  await expect
    .poll(async () => {
      const rows = await fetchRows();
      return rows.find((row) => row.session_id === committedBranch)?.inherited_tokens_in ?? 0;
    })
    .toBeGreaterThan(0);

  const rows = await fetchRows();
  const bySession = new Map(rows.map((row) => [row.session_id, row]));
  const branch = bySession.get(committedBranch)!;
  const untouched = bySession.get(untouchedBranch)!;

  // Committed half: one file, two added lines.
  expect(branch.committed_files).toBe(1);
  expect(branch.committed_additions).toBe(2);
  // Uncommitted half: one untracked file, one added line.
  expect(branch.uncommitted_files).toBe(1);
  expect(branch.uncommitted_additions).toBe(1);
  // Totals are the union, not a zero.
  expect(branch.files_changed).toBe(2);
  expect(branch.additions).toBe(3);
  expect(branch.deletions).toBe(0);
  expect(branch.diff_error ?? "").toBe("");

  // An untouched branch is still zero: the fix must not invent work.
  expect(untouched.files_changed).toBe(0);
  expect(untouched.additions).toBe(0);
  expect(untouched.deletions).toBe(0);

  // A clone inherits the turn it was cloned from instead of starting at 0.
  expect(branch.inherited_user_messages).toBe(1);
  expect(branch.user_messages).toBe(1);
  expect(branch.inherited_tokens_in).toBeGreaterThan(0);
  expect(branch.tokens_in).toBeGreaterThan(0);
  expect(untouched.inherited_user_messages).toBe(1);

  // And the page renders those exact numbers, not just the word 个文件.
  await page.goto(
    `/p/${projectId}/compare?ids=${main},${committedBranch},${untouchedBranch}`,
  );
  await expect(page.getByTestId("compare-grid")).toBeVisible({ timeout: 40_000 });
  await expect(page.getByTestId("compare-column")).toHaveCount(3, { timeout: 20_000 });

  const committedColumn = page.locator(
    `[data-testid="compare-column"][data-session="${committedBranch}"]`,
  );
  await expect(committedColumn).toContainText("2 个文件");
  await expect(committedColumn).toContainText("+3 / -0");
  await expect(committedColumn).toContainText("已提交 1 · 未提交 1");
  await expect(committedColumn).toContainText("含继承 1");

  const untouchedColumn = page.locator(
    `[data-testid="compare-column"][data-session="${untouchedBranch}"]`,
  );
  await expect(untouchedColumn).toContainText("0 个文件");
  await expect(untouchedColumn).toContainText("+0 / -0");

  // One id is not a comparison.
  await page.goto(`/p/${projectId}/compare?ids=${main}`);
  await expect(page.getByText("选两个会话再对比")).toBeVisible({ timeout: 20_000 });

  const refused = await page.request.get(`/v1/sessions/compare?ids=${main}`);
  expect(refused.status()).toBe(400);
});
