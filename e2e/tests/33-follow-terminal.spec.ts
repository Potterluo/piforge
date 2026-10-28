import { expect, test } from "@playwright/test";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-9.2: the follow view mirrors what the agent's own bash calls did. It is a mirror,
 * not a second shell — it reads the same events the timeline already reduces.
 */
test("the follow panel mirrors the agent's bash output", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  // The fake peer runs a bash tool call, so the timeline has output to mirror.
  await sendPrompt(page, "run the tests");

  await page.getByTestId("panel-tab-follow").click();
  const panel = page.getByTestId("follow-terminal");
  await expect(panel).toBeVisible({ timeout: 40_000 });

  // The xterm surface is what the user reads; assert it rendered, not that it is blank.
  await expect
    .poll(
      async () =>
        page.evaluate(() => {
          const node = document.querySelector('[data-testid="follow-terminal"]');
          return (node?.textContent ?? "").length;
        }),
      { timeout: 30_000 },
    )
    .not.toBe(0);
});