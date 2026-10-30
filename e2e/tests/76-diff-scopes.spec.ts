import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * FR-12.1 / FR-12.2: the changes panel scopes a diff to 工作区, 已暂存 or a
 * commit range, and one hunk of a two-hunk file can be staged on its own.
 *
 * The assertions are about content, not about a label: the same file is shown
 * empty in 已暂存 before anything is staged, shows one edit after one hunk is
 * staged, and shows a different edit under HEAD~1..HEAD than under HEAD~2..HEAD.
 * A panel that only relabelled its header would fail every one of them.
 */
test("the three diff scopes show different content, and one hunk stages alone", async ({ page }) => {
  const root = mkdtempSync(path.join(tmpdir(), "pf-scopes-"));
  const seed = path.join(root, "seed");
  const bare = path.join(root, "origin.git");
  execFileSync("git", ["init", "--bare", "-b", "main", bare], { cwd: root, encoding: "utf8" });
  execFileSync("git", ["clone", bare, seed], { cwd: root, encoding: "utf8" });

  const lines = Array.from({ length: 24 }, (_, i) => `line ${i}`);
  writeFileSync(path.join(seed, "f.txt"), lines.join("\n") + "\n");
  execFileSync("git", ["add", "-A"], { cwd: seed });
  execFileSync("git", ["-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-m", "seed"], {
    cwd: seed,
  });
  execFileSync("git", ["push", "origin", "main"], { cwd: seed });

  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `scopes-${Date.now()}`,
    repo_url: bare,
    default_branch: "main",
  });
  const projectId = (project.body as { project: { id: string } }).project.id;
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "diff scopes",
  });
  const sessionId = (created.body as { session: { id: string } }).session.id;

  // Two edits far apart: two hunks in the same file.
  const edited = lines.slice();
  edited[1] = "line 1 EDITED-FIRST";
  edited[20] = "line 20 EDITED-SECOND";
  const wrote = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=f.txt`,
    edited.join("\n") + "\n",
  );
  expect(wrote.status).toBeLessThan(300);

  await page.goto(`/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("panel-tab-changes").click();

  const change = page.locator('[data-testid="git-change"][data-path="f.txt"]');
  await expect(change).toBeVisible({ timeout: 30_000 });
  await change.click();

  const diff = page.getByTestId("git-diff-patch").or(page.getByTestId("git-diff-view")).first();
  const scopeBadge = page.getByTestId("git-diff-scope");
  const hunkRows = page.getByTestId("git-hunk-row");

  // --- 工作区: both edits, two hunks -------------------------------------
  await expect(scopeBadge).toHaveAttribute("data-scope", "unstaged");
  await expect(scopeBadge).toHaveText("工作区");
  await expect(diff).toContainText("line 1 EDITED-FIRST", { timeout: 20_000 });
  await expect(diff).toContainText("line 20 EDITED-SECOND");
  await expect(hunkRows).toHaveCount(2);

  // --- 已暂存: the same file has nothing there yet ------------------------
  await page.getByTestId("git-scope-staged").click();
  await expect(scopeBadge).toHaveAttribute("data-scope", "staged");
  await expect(scopeBadge).toHaveText("已暂存");
  await expect(page.getByTestId("git-panel")).toContainText("这个文件在当前范围内没有差异");

  // --- stage one hunk from the diff view ---------------------------------
  await page.getByTestId("git-scope-unstaged").click();
  await expect(hunkRows).toHaveCount(2);
  await page
    .locator('[data-testid="git-hunk-row"][data-index="0"]')
    .getByTestId("git-hunk-stage")
    .click();

  // The worktree keeps the second edit only, still with one hunk.
  await expect(hunkRows).toHaveCount(1, { timeout: 20_000 });
  await expect(diff).toContainText("line 20 EDITED-SECOND");
  await expect(diff).not.toContainText("line 1 EDITED-FIRST");

  // The index has exactly the first edit.
  await page.getByTestId("git-scope-staged").click();
  await expect(diff).toContainText("line 1 EDITED-FIRST", { timeout: 20_000 });
  await expect(diff).not.toContainText("line 20 EDITED-SECOND");
  await expect(hunkRows).toHaveCount(1);

  // Unstaging the same hunk puts it back, without touching the file.
  await page
    .locator('[data-testid="git-hunk-row"][data-index="0"]')
    .getByTestId("git-hunk-unstage")
    .click();
  await expect(page.getByTestId("git-panel")).toContainText("这个文件在当前范围内没有差异");
  await page.getByTestId("git-scope-unstaged").click();
  await expect(hunkRows).toHaveCount(2, { timeout: 20_000 });

  // --- two commits, so a range has something to distinguish --------------
  await page
    .locator('[data-testid="git-hunk-row"][data-index="0"]')
    .getByTestId("git-hunk-stage")
    .click();
  await expect(hunkRows).toHaveCount(1, { timeout: 20_000 });
  await page.getByTestId("git-commit-message").fill("hunk one");
  await page.getByTestId("git-commit-submit").click();
  // The commit took the staged hunk only: the other edit is still in the
  // worktree, as one unstaged row.
  await expect(page.getByTestId("git-change")).toHaveCount(1, { timeout: 30_000 });
  await expect(change).toHaveAttribute("data-state", "unstaged");
  await expect(diff).toContainText("line 20 EDITED-SECOND", { timeout: 20_000 });
  await expect(diff).not.toContainText("line 1 EDITED-FIRST");

  await page.getByTestId("git-stage-all").click();
  await expect(page.getByTestId("git-commit-submit")).toBeEnabled({ timeout: 20_000 });
  await page.getByTestId("git-commit-message").fill("hunk two");
  await page.getByTestId("git-commit-submit").click();
  await expect(page.getByTestId("git-change")).toHaveCount(0, { timeout: 30_000 });

  // --- 范围: the last commit, then the last two --------------------------
  await page.getByTestId("git-scope-range").click();
  await expect(scopeBadge).toHaveAttribute("data-scope", "range");
  await expect(scopeBadge).toHaveText("HEAD~1..HEAD");
  // The file has no working-tree change, so the list comes from the scope's own
  // diff; without it a commit could not be reviewed at all.
  await expect(page.getByTestId("git-scope-files")).toHaveAttribute("data-files", "1", {
    timeout: 20_000,
  });
  await expect(page.getByTestId("git-scope-file")).toHaveAttribute("data-path", "f.txt");
  await expect(diff).toContainText("line 20 EDITED-SECOND", { timeout: 20_000 });
  await expect(diff).not.toContainText("line 1 EDITED-FIRST");

  await page.getByTestId("git-range-from").fill("HEAD~2");
  await page.getByTestId("git-range-apply").click();
  await expect(scopeBadge).toHaveText("HEAD~2..HEAD");
  await expect(diff).toContainText("line 1 EDITED-FIRST", { timeout: 20_000 });
  await expect(diff).toContainText("line 20 EDITED-SECOND");

  // A historical range is read-only for hunks, and says so instead of offering
  // a button that the server would refuse.
  await expect(page.getByTestId("git-hunk-readonly")).toBeVisible();
  await expect(page.getByTestId("git-hunk-stage")).toHaveCount(0);

  await page.screenshot({ path: path.join(root, "76-diff-scopes.png") });
});
