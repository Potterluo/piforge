import { expect, test } from "@playwright/test";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

/**
 * The transcript is read as runs, not as Pi messages.
 *
 * One reply used to render seven "PiForge" headers, four model badges and a
 * clock on every message of the same run. The unit of identity is the run:
 * one author label, at most one model badge and one timestamp, no matter how
 * many messages Pi wrote to produce the answer.
 */
test("one run renders one author label, one model badge and one timestamp", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  await sendPrompt(page, "run the tests");

  const stream = page.getByTestId("session-stream");
  await expect(stream.getByTestId("run-block")).toHaveCount(1, { timeout: 30_000 });

  // The run is still made of several Pi messages; only the chrome is shared.
  expect(await stream.getByTestId("turn-assistant").count()).toBeGreaterThanOrEqual(2);
  await expect(stream.getByTestId("run-header")).toHaveCount(1);
  await expect(stream.getByText("PiForge", { exact: true })).toHaveCount(1);
  // The new session follows the default model, so the run names it once.
  await expect(stream.getByTestId("turn-model")).toHaveCount(1);
  await expect(stream.getByTestId("run-time")).toHaveCount(1);

  // Tool calls stay folded into one summary row for the run.
  await expect(stream.getByTestId("tool-group")).toHaveCount(1);
  await expect(stream.getByTestId("tool-trace")).toHaveCount(0);
  await expect(stream.getByTestId("tool-group")).toContainText("个工具调用");

  // A second run repeats the header once, not once per message: the count is
  // per run, and it is exactly two after two prompts.
  await sendPrompt(page, "summarise the repo");
  await expect(stream.getByTestId("run-block")).toHaveCount(2, { timeout: 30_000 });
  await expect(stream.getByText("PiForge", { exact: true })).toHaveCount(2);
  await expect(stream.getByTestId("turn-model")).toHaveCount(2);
  await expect(stream.getByTestId("run-time")).toHaveCount(2);

  // `unknown` is an internal value; the header must never print it.
  await expect(page.getByTestId("session-meta")).not.toContainText("unknown");
});
