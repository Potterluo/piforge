import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-12.4: the 变更 tab is an overview, not just a list of names. It must say
 * how much changed and call out the two kinds of change that are never routine:
 * a lockfile and a CI definition.
 *
 * The numbers are asserted against git's own numstat through the API as well as
 * on screen, so a frontend that invents a plausible total cannot pass.
 */
test("the change overview counts lines and flags lockfiles and CI", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  // A committed file to modify, so the deletion side is real too.
  await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=base.txt`, "one\ntwo\nthree\n");
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/stage`, { paths: ["base.txt"] });
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "base",
    paths: [],
  });

  // One line replaced: +1/−1 against the commit.
  await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=base.txt`, "one\nTWO\nthree\n");
  await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=notes.txt`, "a\nb\n");
  await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=package-lock.json`, "{\n}\n");
  await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(".github/workflows/ci.yml")}`,
    "name: ci\n",
  );

  // The API is the source of truth for the same numbers the panel draws.
  const statusResponse = await page.request.get(`/v1/sessions/${sessionId}/git/status`);
  expect(statusResponse.status()).toBe(200);
  const status = (await statusResponse.json()) as {
    additions: number;
    deletions: number;
    changes: { path: string; additions: number; deletions: number; risk?: string }[];
  };
  expect(status.additions).toBe(6);
  expect(status.deletions).toBe(1);
  const byPath = new Map(status.changes.map((change) => [change.path, change]));
  expect(byPath.get("base.txt")).toMatchObject({ additions: 1, deletions: 1 });
  expect(byPath.get("notes.txt")).toMatchObject({ additions: 2, deletions: 0 });
  expect(byPath.get("package-lock.json")?.risk).toBe("lockfile");
  expect(byPath.get(".github/workflows/ci.yml")?.risk).toBe("ci");

  await page.reload();
  await page.getByTestId("panel-tab-changes").click();

  // Totals in the header: +6 −1.
  const totals = page.getByTestId("git-change-totals");
  await expect(totals).toBeVisible({ timeout: 30_000 });
  await expect(totals).toContainText("+6");
  await expect(totals).toContainText("−1");

  // One row, one count, for the modified file.
  const base = page.locator('[data-testid="git-change"][data-path="base.txt"]');
  await expect(base).toBeVisible({ timeout: 20_000 });
  const baseCounts = base.getByTestId("git-line-counts");
  await expect(baseCounts).toHaveAttribute("data-additions", "1");
  await expect(baseCounts).toHaveAttribute("data-deletions", "1");

  // The risky rows say why they are risky, not just that they changed.
  const lock = page.locator('[data-testid="git-change"][data-path="package-lock.json"]');
  await expect(lock).toBeVisible();
  const lockRisk = lock.getByTestId("git-risk");
  await expect(lockRisk).toHaveAttribute("data-risk", "lockfile");
  await expect(lockRisk).toHaveText("锁文件");

  const ci = page.locator('[data-testid="git-change"][data-path=".github/workflows/ci.yml"]');
  await expect(ci).toBeVisible();
  const ciRisk = ci.getByTestId("git-risk");
  await expect(ciRisk).toHaveAttribute("data-risk", "ci");
  await expect(ciRisk).toHaveText("CI");

  // A routine text change carries no risk marker at all.
  await expect(base.getByTestId("git-risk")).toHaveCount(0);
});
