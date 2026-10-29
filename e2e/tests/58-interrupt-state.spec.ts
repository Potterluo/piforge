import { expect, test } from "@playwright/test";

import { USER, login, openNewSession } from "./helpers";

/**
 * Stopping a run is a normal outcome (QA-14, MAJOR).
 *
 * It used to render 模型调用失败 / This operation was aborted / "check this
 * provider's address and key", plus a red "1 个失败" tool group — an auth outage
 * invented by pressing Stop. The transcript must say 已中断, record no failure, and
 * the toast stays because the action did happen.
 *
 * The prompt carries a marker the fake peer turns into a slow-paced run: a stop
 * can only be tested against a run that is still in flight.
 */
test.describe("interrupting a run", () => {
  test("renders 已中断, not a model failure", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);

    const usersBefore = await page.getByTestId("turn-user").count();
    await page.getByTestId("composer-input").fill("[[hold-run]] run a long command and wait for it");
    await page.getByTestId("composer-send").click();
    await expect(page.getByTestId("turn-user")).toHaveCount(usersBefore + 1, { timeout: 30_000 });

    // Stop while a tool call is actually in flight, the way a user does: the
    // tool group is the part that used to turn red ("1 个失败").
    await expect(page.getByTestId("tool-group").first()).toBeVisible({ timeout: 40_000 });
    const stop = page.getByTestId("composer-stop");
    await stop.waitFor({ timeout: 20_000 });
    await stop.click();

    await expect(page.locator('[data-testid="run-state"][data-state="settled"]')).toHaveCount(1, {
      timeout: 60_000,
    });
    // The toast survives: the stop did happen, and it is the confirmation.
    await expect(page.locator("[data-sonner-toast]").filter({ hasText: "已中断" }).first()).toBeVisible({
      timeout: 20_000,
    });

    await expect(page.getByTestId("run-interrupted")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("run-interrupted")).toContainText("已中断");

    const stream = page.getByTestId("session-stream");
    await expect(stream).not.toContainText("模型调用失败");
    await expect(stream).not.toContainText("检查「设置");
    // The tool call that was cut short is reported as interrupted, not failed and
    // not still running.
    await expect(stream).toContainText("个已中断");
    await expect(stream).not.toContainText("个失败");
    await expect(stream).not.toContainText("个运行中");
  });
});
