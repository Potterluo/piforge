import { expect, test } from "@playwright/test";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-5.10: when a run dies without saying anything in the transcript, the agent's
 * stderr is where the reason is. The panel has to be reachable and honest about
 * having nothing to show.
 */
test("the agent log sheet opens and reports the tail", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  await page.getByTestId("session-logs-open").click();
  // Either the tail or an explicit "nothing written" message: both are answers,
  // and an empty box would be neither.
  await expect(
    page
      .getByTestId("session-logs")
      .or(page.getByText("还没有输出。agent 启动过、但什么都没写到标准错误。"))
      .or(page.getByText("当前运行时没有保留 agent 日志，这个会话没有可看的内容。")),
  ).toBeVisible({ timeout: 20_000 });

  // The API agrees with the panel.
  const logs = await (await page.request.get(`/v1/sessions/${sessionId}/logs`)).json();
  expect(Array.isArray((logs as { logs: { lines: string[] } }).logs.lines)).toBe(true);
});