import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-12.5: the test-results panel.
 *
 * The fake peer's scripted tool call is `go test ./internal/cache/...`, so a run
 * appears without a real toolchain. The fail marker asks the peer for the
 * failing variant of that output, which is how one session produces a red run
 * and then a green one — the two states the badge has to tell apart.
 */
const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fr125");
mkdirSync(ARTIFACTS, { recursive: true });

test.describe("test-results panel", () => {
  test("a failing run shows its failure, and a passing run updates the badge", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);

    // Empty state before anything ran: it must say what will appear and how to
    // trigger it, not just "no data".
    await page.getByTestId("panel-tab-tests").click();
    const empty = page.getByTestId("test-empty");
    await expect(empty).toBeVisible({ timeout: 30_000 });
    await expect(empty).toContainText("还没有测试运行");
    await expect(empty).toContainText("go test");
    await page.screenshot({ path: path.join(ARTIFACTS, "empty.png") });

    // ---- failing run -----------------------------------------------------
    await sendPrompt(page, "[[failing-tests]] run the failing tests");

    const badge = page.getByTestId("test-tab-badge");
    await expect(badge).toHaveAttribute("data-state", "fail", { timeout: 30_000 });

    const failed = page.getByTestId("test-run").first();
    await expect(failed).toHaveAttribute("data-status", "failed");
    await expect(failed).toContainText("go test ./internal/cache/...");

    // Expanding is what makes the row useful: name, file:line, assertion.
    await failed.getByRole("button").first().click();
    const failure = page.getByTestId("test-failure").first();
    await expect(failure).toBeVisible();
    await expect(failure).toContainText("TestCacheEvict");
    await expect(failure).toContainText("race detected");
    await expect(page.getByTestId("test-failure-location")).toContainText("cache_test.go:42");
    await page.screenshot({ path: path.join(ARTIFACTS, "failure-expanded.png") });

    // ---- passing run in the same session ---------------------------------
    await sendPrompt(page, "run the passing tests");
    await expect(badge).toHaveAttribute("data-state", "pass", { timeout: 30_000 });

    const passed = page.getByTestId("test-run").first();
    await expect(passed).toHaveAttribute("data-status", "passed");
    await expect(passed).toContainText("✅ 1");
    await page.screenshot({ path: path.join(ARTIFACTS, "pass.png") });

    // ---- reload keeps the runs -------------------------------------------
    await page.reload();
    await page.getByTestId("panel-tab-tests").click();
    await expect(page.getByTestId("test-run")).toHaveCount(2, { timeout: 30_000 });
    await expect(page.getByTestId("test-run").first()).toHaveAttribute("data-status", "passed");
    await expect(page.getByTestId("test-tab-badge")).toHaveAttribute("data-state", "pass");
    await page.screenshot({ path: path.join(ARTIFACTS, "after-reload.png") });

    // The API is the same data the panel read: newest first, both statuses.
    const listed = await page.request.get(`/v1/sessions/${sessionId}/test-runs`);
    expect(listed.status()).toBe(200);
    const body = (await listed.json()) as {
      count: number;
      runs: { status: string; runner: string; command: string; failures: unknown[] }[];
    };
    expect(body.count).toBe(2);
    expect(body.runs.map((run) => run.status)).toEqual(["passed", "failed"]);
    expect(body.runs[1].failures).toHaveLength(1);
  });
});
