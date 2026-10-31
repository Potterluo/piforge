import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-accept2");
mkdirSync(ARTIFACTS, { recursive: true });

// The setup provisions a worktree, commits a baseline and then watches a run in
// flight; under a full-suite load the 60s default is the first thing to go.
test.setTimeout(150_000);

/**
 * The changes panel was fetched once and never refreshed.
 *
 * While a run edited a tracked file, 变更 kept saying 工作区干净，没有改动。 even
 * though `GET /v1/sessions/{id}/git/status` said `clean:false` with `M count.js`.
 * Only a page reload fixed it — exactly when a reviewer is watching a run. The
 * session event stream already carries the signal the file tree listens to
 * (`tool.ended` / `run.settled`); the git panel did not subscribe to it.
 *
 * The fake peer cannot write files, so the edit is made through the files API
 * while the run is still streaming. That is the honest shape of the defect: the
 * worktree changes underneath an open panel, and the panel is told nothing
 * except that the run settled. The page is never reloaded — a window marker
 * proves it, rather than the absence of an obvious `reload()` call.
 */
test("the changes panel follows the worktree during a run, without a reload", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  // The first prompt provisions the worktree.
  await sendPrompt(page, "hello");

  // A tracked file, committed, so the edit below is "M" and not "U".
  const seeded = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("count.js")}`,
    "let n = 1;\n",
  );
  expect(seeded.status).toBeLessThan(300);
  const commit = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "seed count.js",
    paths: ["count.js"],
  });
  expect(commit.status).toBe(200);

  // The panel is opened *before* the run: a panel mounted after the change
  // would fetch fresh data and pass even on the broken build.
  await page.getByTestId("panel-tab-changes").click();
  await expect(page.getByTestId("git-panel")).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('[data-testid="git-change"]')).toHaveCount(0, { timeout: 20_000 });

  // A marker that a reload would wipe. The assertion at the end is "the same
  // document is still open", not "we did not call reload".
  await page.evaluate(() => {
    (window as unknown as { __changesPanelMounted?: boolean }).__changesPanelMounted = true;
  });

  // A run slow enough to still be in flight when the file changes.
  await page.getByTestId("composer-input").fill("[[hold-run]] edit count.js while this runs");
  await page.getByTestId("composer-send").click();
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "running", {
    timeout: 20_000,
  });

  // The agent's edit, landing through a path the panel is not told about.
  const edit = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("count.js")}`,
    "let n = 2;\n",
  );
  expect(edit.status).toBeLessThan(300);

  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
    timeout: 90_000,
  });

  const row = page.locator('[data-testid="git-change"][data-path="count.js"]');
  await expect(row).toBeVisible({ timeout: 20_000 });
  // The file list carries the real counts, not just a row.
  await expect(row.getByTestId("git-line-counts")).toHaveAttribute("data-additions", "1");
  await expect(row.getByTestId("git-line-counts")).toHaveAttribute("data-deletions", "1");

  // Still the same document: the panel updated itself, no reload.
  expect(
    await page.evaluate(
      () => (window as unknown as { __changesPanelMounted?: boolean }).__changesPanelMounted,
    ),
  ).toBe(true);
  await page.screenshot({
    path: path.join(ARTIFACTS, "78-changes-live.png"),
    fullPage: true,
  });
});
