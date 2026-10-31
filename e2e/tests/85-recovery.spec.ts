import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession } from "./helpers";

/**
 * The recovery path after a stop.
 *
 * Stopping a run is a normal outcome, but the transcript used to say only
 * 「已中断」. A person who came back to that session had to work out for
 * themselves what the checkout held and what to type next. This spec is the
 * user-visible promise: the marker names the cause (停止), states what survived
 * (dirty files and the last commit) from the real checkout, and 继续 sends one
 * server-composed follow-up that names the interrupted request and tells the
 * agent to verify the current state first.
 *
 * The fake peer holds a run with [[hold-run]], so the stop lands while a tool
 * call is genuinely in flight rather than after the scripted run finished.
 */
test.describe("recovering an interrupted run", () => {
  test("a stop explains itself, shows what survived, and 继续 sends the recorded request", async ({
    page,
  }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);

    // A real untracked change in the checkout, written through the same API the
    // file panel uses. This is what the marker must count; a canned number in
    // the UI would not prove anything.
    const wrote = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=recovery-dirty.txt`,
      "left behind by the interrupted run\n",
    );
    expect(wrote.status).toBeLessThan(300);

    const request = "[[hold-run]] 修复缓存淘汰的竞态，并补一个测试";
    await page.getByTestId("composer-input").fill(request);
    await page.getByTestId("composer-send").click();
    await expect(page.getByTestId("turn-user")).toHaveCount(1, { timeout: 30_000 });

    await expect(page.getByTestId("tool-group").first()).toBeVisible({ timeout: 40_000 });
    await page.getByTestId("composer-stop").click();

    const marker = page.getByTestId("run-interrupted");
    await expect(marker).toBeVisible({ timeout: 30_000 });
    // The cause is rendered from the recorded event, not from the toast.
    await expect(page.getByTestId("recovery-cause")).toContainText("停止");
    await expect(page.getByTestId("recovery-reason")).toContainText("用户停止");
    // What survived comes from git: one dirty path and a real commit hash.
    await expect(page.getByTestId("recovery-survival")).toContainText(
      "工作区有 1 个未提交改动",
      { timeout: 20_000 },
    );
    await expect(page.getByTestId("recovery-survival")).toContainText("最后一次提交");

    await page.getByTestId("recovery-continue").click();

    // The transcript shows exactly what was sent. The server composed it: the
    // last request, the cause, and the instruction to check state before
    // continuing, so this is the message the agent received.
    await expect(page.getByTestId("turn-user")).toHaveCount(2, { timeout: 30_000 });
    const sent = page.getByTestId("turn-user").last();
    await expect(sent).toContainText("修复缓存淘汰的竞态，并补一个测试");
    await expect(sent).toContainText("用户停止，本次运行被中断");
    await expect(sent).toContainText("请先检查当前状态");
    await expect(sent).toContainText("git status");

    // A new run started, so the marker must stop offering the same action.
    await expect(page.getByTestId("recovery-continue")).toHaveCount(0, { timeout: 20_000 });
  });
});
