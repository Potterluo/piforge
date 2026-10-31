import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-fork-copy");
mkdirSync(ARTIFACTS, { recursive: true });

/**
 * The old cap in `seedForkedHistory`: `ListEvents(..., 0, 1000)`.
 *
 * The copy was bounded but the fork was not told, so a parent longer than this
 * produced a branch whose transcript stopped at event 1000 — no gap marker, no
 * warning, just a conversation that ended in the middle.
 */
const OLD_COPY_CAP = 1000;

/**
 * Forking a long session must keep the whole transcript.
 *
 * The premise is a parent with more events than the old single-page copy could
 * read. The promise is the two ends: the branch shows the first user message
 * *and* the last one (and the answer that followed it). On the copy path the
 * branch showed only the first ~1000 events, so `turn-user` had fewer rows than
 * the parent had prompts and the last prompt was not on the page at all.
 */
test.describe("forking a session longer than the old copy cap", () => {
  test("the branch keeps the first and the last message of the parent", async ({ page }) => {
    test.setTimeout(300_000);
    await login(page, USER);
    const parentId = await openNewSession(page, `e2e-forklong-${Date.now()}`);
    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";

    // Grow the parent through short, queued turns. Sending them in small
    // batches keeps the fake peer's queue busy (each run emits ~50 events) and
    // lets the test stop as soon as the log is past the old cap.
    const prompts: string[] = [];
    let lastSeq = 0;
    for (let round = 0; round < 12 && lastSeq <= 1200; round += 1) {
      for (let index = 0; index < 4; index += 1) {
        const text = `long turn ${prompts.length + 1}: reply with one short sentence`;
        const sent = await apiWrite(page, "POST", `/v1/sessions/${parentId}/messages`, {
          client_msg_id: `long-${prompts.length + 1}-${Date.now()}`,
          text,
          streaming_behavior: "follow_up",
        });
        expect(sent.status).toBe(200);
        prompts.push(text);
      }
      lastSeq = await drain(page, parentId);
    }
    expect(
      lastSeq,
      `the parent must be longer than the old ${OLD_COPY_CAP}-event copy cap`,
    ).toBeGreaterThan(OLD_COPY_CAP);
    console.log(
      `[fork-long] parent seq=${lastSeq} prompts=${prompts.length} (old copy cap ${OLD_COPY_CAP})`,
    );

    // The last user message is the fork point, so the whole conversation above
    // it has to survive the fork.
    const points = await forkPoints(page, parentId);
    expect(points.length).toBeGreaterThan(prompts.length - 2);
    const entry = points[points.length - 1];
    const created = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
      entry_id: entry.entry_id,
      label: "long fork",
    });
    expect(created.status).toBeLessThan(300);
    const childId = (created.body as { session: { id: string } }).session.id;

    await page.goto(`/p/${projectId}/s/${childId}`);
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 40_000 });
    const users = page.getByTestId("turn-user");
    await expect(users.first()).toBeVisible({ timeout: 30_000 });

    // Every prompt the parent was given must be in the branch — not a prefix.
    await expect(users).toHaveCount(prompts.length, { timeout: 60_000 });
    await expect(users.first()).toContainText(prompts[0].slice(0, 32));
    await expect(users.last()).toContainText(prompts[prompts.length - 1].slice(0, 32));
    // The tail of the parent's log is the answer after that last prompt. A copy
    // that stopped at event 1000 lost it; a complete transcript has it.
    await expect(page.getByTestId("turn-assistant").last()).toContainText("Tests pass", {
      timeout: 30_000,
    });

    await page.screenshot({ path: path.join(ARTIFACTS, "89-fork-long.png") });
    await page.getByTestId("session-stream").evaluate((node) => node.scrollTo({ top: 0 }));
    await page.waitForTimeout(200);
    await page.screenshot({ path: path.join(ARTIFACTS, "89-fork-long-top.png") });
    await page
      .getByTestId("session-stream")
      .evaluate((node) => node.scrollTo({ top: node.scrollHeight }));
    await page.waitForTimeout(200);
    await page.screenshot({ path: path.join(ARTIFACTS, "89-fork-long-bottom.png") });
  });
});

interface Status {
  running: boolean;
  queue_depth: number;
  last_seq: number;
}

async function status(page: Page, sessionId: string): Promise<Status> {
  const res = await page.request.get(`/v1/sessions/${sessionId}/status`);
  expect(res.status()).toBe(200);
  return (await res.json()) as Status;
}

/** Waits until no run or queued turn is left, and returns the newest seq. */
async function drain(page: Page, sessionId: string): Promise<number> {
  let last: Status = { running: true, queue_depth: 1, last_seq: 0 };
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    last = await status(page, sessionId);
    if (!last.running && last.queue_depth === 0) return last.last_seq;
    await page.waitForTimeout(400);
  }
  return last.last_seq;
}

async function forkPoints(
  page: Page,
  sessionId: string,
): Promise<{ entry_id: string; preview: string }[]> {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const body = (await (
      await page.request.get(`/v1/sessions/${sessionId}/fork-points`)
    ).json()) as { points: { entry_id: string; preview: string }[] };
    if (body.points.length > 0) return body.points;
    await page.waitForTimeout(500);
  }
  throw new Error("the parent never reported a fork point");
}
