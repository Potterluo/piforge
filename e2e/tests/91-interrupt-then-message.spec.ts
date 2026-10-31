import { expect, test } from "@playwright/test";

import { USER, login, openNewSession } from "./helpers";

/**
 * Stop a run, then send a different request at once.
 *
 * Measured on the deployment (docs/HANDOFF.md §2, probe:
 * `scripts/fix-interrupt-context-probe.mjs`), the follow-up reached the agent and
 * started its own run, but the answer came back about the interrupted request:
 * Pi keeps the truncated assistant answer in the conversation and the model
 * continued it. The control plane cannot edit a persisted Pi entry, so it marks
 * the interruption in the prompt it composes for the first message after a stop
 * — the same server-side composition the review-comment and 继续 paths use. The
 * transcript shows that sentence because it is exactly what the agent received.
 *
 * This spec pins the user-visible half of that promise:
 *   1. the follow-up is accepted and reaches the agent (it appears in the
 *      transcript, note and all, and the run settles);
 *   2. the prompt names the interruption, so the agent is not asked to continue
 *      a half-written answer;
 *   3. nothing shows a message as accepted when it was not delivered (no
 *      「发送失败」 and no queue position for a run that never runs).
 */
test.describe("interrupt, then immediately send another message", () => {
  test("the follow-up is delivered as a new run that is told about the interruption", async ({
    page,
  }) => {
    await login(page, USER);
    await openNewSession(page);

    // 1. a run that is still going when the user stops it.
    const usersBefore = await page.getByTestId("turn-user").count();
    await page.getByTestId("composer-input").fill("[[hold-run]] count to 200 and keep going");
    await page.getByTestId("composer-send").click();
    await expect(page.getByTestId("turn-user")).toHaveCount(usersBefore + 1, { timeout: 30_000 });
    await expect(page.getByTestId("tool-group").first()).toBeVisible({ timeout: 40_000 });

    await page.getByTestId("composer-stop").waitFor({ timeout: 20_000 });
    await page.getByTestId("composer-stop").click();
    await expect(page.getByTestId("run-interrupted")).toBeVisible({ timeout: 30_000 });
    await expect(
      page.locator('[data-testid="run-state"][data-state="settled"]'),
    ).toHaveCount(1, { timeout: 30_000 });

    // 2. the distinct follow-up, with no pause: the reported sequence.
    await page.getByTestId("composer-input").fill("Say beta.");
    await page.getByTestId("composer-send").click();

    // It is accepted: a new user turn appears, and no failure is shown.
    await expect(page.getByTestId("turn-user")).toHaveCount(usersBefore + 2, { timeout: 30_000 });

    // 3. The prompt the agent received names the interruption. This is the
    // sentence the control plane composes; before the fix the follow-up was sent
    // bare and the model answered the interrupted request instead.
    const followUp = page.getByTestId("turn-user").last();
    await expect(followUp).toContainText("Say beta.");
    await expect(followUp).toContainText("上一轮已被用户中断并作废");
    await expect(followUp).toContainText("只回答下面这条消息");

    // 4. The new run is answered: an assistant turn follows the follow-up and the
    // run settles again. A queued-into-a-dying-run message would leave the user
    // turn with no answer and the run settled long before.
    await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
      timeout: 90_000,
    });
    await expect(page.getByTestId("turn-assistant").last()).not.toBeEmpty();

    const stream = page.getByTestId("session-stream");
    await expect(stream).not.toContainText("发送失败");
    // A "已加入队列（第 N 位）" toast is the UI claiming delivery into a run that is
    // being aborted; the follow-up must be its own run.
    await expect(page.locator("[data-sonner-toast]").filter({ hasText: "已加入队列" })).toHaveCount(0);
  });
});
