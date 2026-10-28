import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * The file panel's "显示忽略文件" switch has to change what the server lists.
 * It used to be a client-side filter over an already-filtered response, so
 * flipping it could only ever hide more — a switch that looked broken because
 * it was.
 */
test.describe("file tree", () => {
  test("an ignored file appears only when the switch is on", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    // The first prompt provisions the checkout.
    await sendPrompt(page, "hello");

    const write = (path: string, body: string) =>
      apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`, body);

    expect((await write("visible.txt", "plain\n")).status).toBeLessThan(300);
    expect((await write(".cache/ignored.txt", "hidden\n")).status).toBeLessThan(300);

    await page.getByTestId("panel-tab-files").click();
    await expect(page.getByTestId("file-tree")).toBeVisible({ timeout: 30_000 });
    // The panel loaded its tree when the page mounted, before the files existed.
    await page.getByRole("button", { name: "刷新文件树" }).click();

    // Default: dot directories are filtered out by the server.
    await expect(page.getByText("visible.txt")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("ignored.txt")).toHaveCount(0);

    await page.getByRole("switch", { name: "显示忽略文件" }).click();
    await expect(page.getByText("ignored.txt")).toBeVisible({ timeout: 20_000 });

    // And back off again: the switch is a round trip, not a one-way latch.
    await page.getByRole("switch", { name: "显示忽略文件" }).click();
    await expect(page.getByText("ignored.txt")).toHaveCount(0);
  });
});

/**
 * A session can be pointed at any model id the endpoint serves, so "it replied"
 * is not enough: the transcript says which model replied.
 */
test("an assistant turn reports the model that produced it", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  await sendPrompt(page, "hello");

  const model = page.getByTestId("turn-model").first();
  await expect(model).toBeVisible({ timeout: 30_000 });
  await expect(model).toContainText("/");
});
