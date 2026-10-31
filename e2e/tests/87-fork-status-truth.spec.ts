import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-truth");
mkdirSync(ARTIFACTS, { recursive: true });

/** A run slow enough that a fork can be created while it is still in flight. */
const HOLD = "[[hold-run]]";

/**
 * The header must not claim a run the API says is not happening.
 *
 * The acceptance pass opened a fork and read 「运行中 · 3 个工具」 for minutes
 * while `GET /v1/sessions/{id}/status` answered `running:false`. The branch's
 * transcript is a *copy* of the parent's event log, and it can end in the middle
 * of a run — forking while the parent is working is exactly how that happens.
 * Replaying that copy used to set the header's running flag, which is a guess
 * about a fork's history, not a fact about a run.
 *
 * The status endpoint is the fact. This test forks a session mid-run, proves the
 * API says the branch is idle, and asserts the branch page agrees: no 运行中
 * badge, no Stop button — and it stays that way, rather than flickering correct.
 */
test.describe("session status truth", () => {
  test("a fork whose copied transcript ends mid-run does not claim 运行中", async ({ page }) => {
    test.setTimeout(180_000);
    await login(page, USER);
    const parentId = await openNewSession(page);
    // The branch needs history behind the fork point, so there is a settled run
    // first. Without it Pi forks at nothing and the branch is honestly empty.
    await sendPrompt(page, "first task, finish it");

    await page.getByTestId("composer-input").fill(`${HOLD} second task that is still running`);
    await page.getByTestId("composer-send").click();
    await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "running", {
      timeout: 20_000,
    });
    // Let the run's events land: the copy has to be taken from a log that ends
    // in the middle of a run, which is the shape that produced the stale badge.
    await page.waitForTimeout(1_500);

    const entryId = await forkPoint(page, parentId, 1);
    const childId = await fork(page, parentId, entryId, "forked while running");

    // Ground truth: no run is in flight for the branch. Its agent process was
    // started, but it is idle.
    const status = (await (await page.request.get(`/v1/sessions/${childId}/status`)).json()) as {
      running: boolean;
      streaming: boolean;
    };
    expect(status.running).toBe(false);
    expect(status.streaming).toBe(false);

    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${childId}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });

    // Wait for the copied transcript to paint, so the assertion is about the
    // settled page and not about a moment before the replay arrived.
    await expect(page.getByTestId("turn-user").first()).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("run-badge")).toHaveCount(0);
    await expect(page.getByTestId("composer-stop")).toHaveCount(0);
    await page.screenshot({ path: path.join(ARTIFACTS, "87-fork-idle.png") });

    // It must stay absent: the old badge appeared after the replay and never
    // cleared, so "absent for two seconds" is the property, not "absent once".
    await page.waitForTimeout(2_000);
    await expect(page.getByTestId("run-badge")).toHaveCount(0);
    await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled");
  });
});

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

async function fork(
  page: Page,
  parentId: string,
  entryId: string,
  label: string,
): Promise<string> {
  const created = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
    entry_id: entryId,
    label,
  });
  expect(created.status).toBeLessThan(300);
  return (created.body as { session: { id: string } }).session.id;
}
