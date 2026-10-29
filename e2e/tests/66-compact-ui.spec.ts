import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-limit-compact");
mkdirSync(ARTIFACTS, { recursive: true });
const QA16_ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-qa16");
mkdirSync(QA16_ARTIFACTS, { recursive: true });

/** The 上下文用量 badge's percentage, e.g. "75%" → 75. */
async function meterPercent(page: Page): Promise<number> {
  const text = await page.getByTestId("context-meter").textContent();
  const match = /(\d+)%/.exec(text ?? "");
  return match ? Number(match[1]) : -1;
}

/**
 * FR-1.9 in the browser: the action exists, it is disabled with an explanation
 * when there is no agent to compact, a no-op shows the API's exact reason on
 * screen, and a real compaction moves the meter without a reload.
 *
 * The success path reuses the fake peer's `[[big-context]]` marker, the same one
 * `05-branches.spec.ts` uses, so the "before" is genuinely near the window limit.
 */
test("a session with no agent explains why compaction is unavailable", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);

  const button = page.getByTestId("compact-context");
  await expect(button).toBeVisible({ timeout: 20_000 });
  await expect(button).toBeDisabled();
  await expect(page.getByTestId("compact-disabled-reason")).toContainText("没有运行中的 agent");
  await page.screenshot({ path: path.join(ARTIFACTS, "33-compact-disabled.png") });
});

test("a no-op compaction shows the API's reason on screen", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  // The first run leaves a live Pi process behind; compaction is possible then.
  const button = page.getByTestId("compact-context");
  await expect(button).toBeEnabled({ timeout: 20_000 });

  await button.click();

  const status = page.getByTestId("compact-status");
  await expect(status).toBeVisible({ timeout: 20_000 });
  await expect(status).toHaveText("没有可压缩的上下文");

  // The API is the source of truth for whether anything was compacted: it says
  // `compacted:false`, exactly like a second attempt through the endpoint would.
  const result = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/compact`, {
    custom_instructions: "",
  });
  expect(result.status).toBe(200);
  expect((result.body as { compacted: boolean }).compacted).toBe(false);

  // A no-op must not draw a marker for a compaction that did not happen. The
  // fake peer emits the same compaction_start/end pair the real agent does, so
  // this assertion is about the transcript, not about the fake being quiet
  // (QA-16, FIX C: the marker contradicted the reason, the toast and /entries).
  //
  // Reload first: the events are replayed from seq 1 in order, so the client has
  // definitely seen both attempts before the assertion runs. An immediate
  // `toHaveCount(0)` was a race — it passed before the compaction events had been
  // rendered, which is why this test did not catch the marker at first.
  await page.reload();
  await expect(page.getByTestId("turn-user").first()).toBeVisible({ timeout: 40_000 });
  let markers = 0;
  for (let attempt = 0; attempt < 20 && markers === 0; attempt += 1) {
    markers = await page.getByText(/上下文已压缩/).count();
    if (markers === 0) await page.waitForTimeout(200);
  }
  expect(markers, "the transcript drew a compaction marker for a compacted:false attempt").toBe(0);

  // The entries have no compaction record either — the third place the old
  // marker disagreed with.
  const entries = await page.request.get(`/v1/sessions/${sessionId}/entries`);
  expect(entries.status()).toBe(200);
  expect(JSON.stringify(await entries.json())).not.toContain('"compaction"');

  await page.screenshot({ path: path.join(QA16_ARTIFACTS, "C-noop-no-marker.png") });
  await page.screenshot({ path: path.join(ARTIFACTS, "34-compact-noop-reason.png") });
});

test("compacting drops the meter without a reload", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  await sendPrompt(page, "look at the cache [[big-context]]");

  await expect(page.getByTestId("context-meter")).toBeVisible({ timeout: 20_000 });
  const before = await meterPercent(page);
  expect(before).toBeGreaterThan(50);

  const button = page.getByTestId("compact-context");
  await expect(button).toBeEnabled({ timeout: 20_000 });
  await button.click();

  await expect(page.getByTestId("compact-status")).toHaveText("上下文已压缩，用量已更新", {
    timeout: 20_000,
  });
  // The transcript marker is the other half of "visible" (the pre-existing test
  // owns its wording); here the claim under test is the meter.
  await expect(page.getByText(/上下文已压缩（手动触发）/).first()).toBeVisible({
    timeout: 20_000,
  });
  await expect
    .poll(async () => meterPercent(page), { timeout: 20_000 })
    .toBeLessThan(before - 20);
  await page.screenshot({ path: path.join(ARTIFACTS, "35-compact-success-meter.png") });
});
