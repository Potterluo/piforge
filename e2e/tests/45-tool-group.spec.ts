import { expect, test } from "@playwright/test";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

/**
 * Tool calls are machinery, not conversation. A run that shells out several times used to
 * put a row per call between the question and the answer; now it puts one, and the calls
 * are behind it.
 */
test("tool calls are summarised into one row per run", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  await sendPrompt(page, "run the tests");

  const group = page.getByTestId("tool-group").first();
  await expect(group).toBeVisible({ timeout: 60_000 });
  // One row, not one per call.
  await expect(page.getByTestId("tool-trace")).toHaveCount(0);
  await expect(group).toContainText("个工具调用");

  // Opening it reveals the individual calls: nothing is hidden, only folded.
  await group.click();
  await expect.poll(async () => page.getByTestId("tool-trace").count(), { timeout: 20_000 }).toBeGreaterThan(0);
});