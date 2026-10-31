import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-truth");
mkdirSync(ARTIFACTS, { recursive: true });

/**
 * FR-12.5, the acceptance pass's exact miss.
 *
 * The agent was asked for "a Node script, with a test, run it" and it wrote
 * `primes.test.js` (node:test) and ran `node primes.js && node primes.test.js`.
 * node:test executed the file and printed a full TAP summary, but the panel
 * stayed empty: `testrun.Detect` only knew the `node --test` flag, so no run was
 * ever recorded for the shape an agent actually uses.
 *
 * The fake peer reproduces that shape (`[[node-test-file]]` → the command and
 * the TAP bytes node v22 prints for a directly executed test file), so this test
 * is about detection and parsing, not about a mock summary. On the pre-fix
 * detector it fails at the empty panel: no badge, no row.
 */
test.describe("test panel — a directly executed node:test file", () => {
  test("the agent's `node x.test.js` run appears with its parsed counts", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);

    await page.getByTestId("panel-tab-tests").click();
    const empty = page.getByTestId("test-empty");
    await expect(empty).toBeVisible({ timeout: 30_000 });
    // The empty state names this shape, so the promise and the detector agree.
    await expect(empty).toContainText("node x.test.js");

    await sendPrompt(page, "[[node-test-file]] run the test file the way the agent did");

    const badge = page.getByTestId("test-tab-badge");
    await expect(badge).toHaveAttribute("data-state", "pass", { timeout: 30_000 });

    const row = page.getByTestId("test-run").first();
    await expect(row).toHaveAttribute("data-status", "passed");
    await expect(row).toHaveAttribute("data-runner", "node --test");
    await expect(row).toContainText("node primes.test.js");
    // Two subtests, both passing: the counts come from the TAP summary, not
    // from the exit code.
    await expect(row).toContainText("✅ 2");
    await page.screenshot({ path: path.join(ARTIFACTS, "86-node-test-file.png") });

    // The API and the panel read the same rows.
    const listed = await page.request.get(`/v1/sessions/${sessionId}/test-runs`);
    expect(listed.status()).toBe(200);
    const body = (await listed.json()) as {
      count: number;
      runs: { runner: string; command: string; status: string; total: number; passed: number }[];
    };
    expect(body.count).toBe(1);
    expect(body.runs[0]).toMatchObject({
      runner: "node --test",
      command: "node primes.test.js",
      status: "passed",
      total: 2,
      passed: 2,
    });
  });
});
