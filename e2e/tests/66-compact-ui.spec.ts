import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { login, openNewSession, sendPrompt, USER } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-limit-compact");
mkdirSync(ARTIFACTS, { recursive: true });

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
  await openNewSession(page);
  await sendPrompt(page, "hello");

  // The first run leaves a live Pi process behind; compaction is possible then.
  const button = page.getByTestId("compact-context");
  await expect(button).toBeEnabled({ timeout: 20_000 });

  await button.click();

  const status = page.getByTestId("compact-status");
  await expect(status).toBeVisible({ timeout: 20_000 });
  await expect(status).toHaveText("没有可压缩的上下文");
  // A no-op must not draw a marker for a compaction that did not happen.
  await expect(page.getByText(/上下文已压缩/)).toHaveCount(0);
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
