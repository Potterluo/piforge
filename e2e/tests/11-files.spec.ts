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

    // Scope the text lookups to the tree: the same path also appears in the
    // 变更 panel (both are mounted), and an unscoped getByText matched two
    // elements the moment both panels were loaded.
    const tree = page.getByTestId("file-tree");

    // Default: dot directories are filtered out by the server.
    await expect(tree.getByText("visible.txt")).toBeVisible({ timeout: 20_000 });
    await expect(tree.getByText("ignored.txt")).toHaveCount(0);

    await page.getByRole("switch", { name: "显示忽略文件" }).click();
    await expect(tree.getByText("ignored.txt")).toBeVisible({ timeout: 20_000 });

    // And back off again: the switch is a round trip, not a one-way latch.
    await page.getByRole("switch", { name: "显示忽略文件" }).click();
    await expect(tree.getByText("ignored.txt")).toHaveCount(0);
  });

  /**
   * Creating a file has to be possible once the tree has files (acceptance pass
   * 3, minor). The only 新建文件 control lived in the empty state, so a user
   * staring at a populated tree looked for it, found nothing and used the
   * terminal instead. The action now sits in the tree header, always.
   */
  test("the tree header creates a file when the tree already has files", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    const write = (path: string, body: string) =>
      apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`, body);
    expect((await write("existing.txt", "before\n")).status).toBeLessThan(300);

    await page.getByTestId("panel-tab-files").click();
    const tree = page.getByTestId("file-tree");
    await expect(tree).toBeVisible({ timeout: 30_000 });
    await page.getByRole("button", { name: "刷新文件树" }).click();
    await expect(tree.getByText("existing.txt")).toBeVisible({ timeout: 20_000 });

    // The empty-state button is gone (as it should be), and the header action is
    // the one that is always there.
    await expect(page.getByTestId("file-create")).toHaveCount(0);
    await page.getByTestId("file-create-header").click();
    await expect(page.getByTestId("file-create-dialog")).toBeVisible();
    await page.getByTestId("file-create-path").fill("created-from-header.txt");
    await page.getByTestId("file-create-submit").click();

    await expect(page.getByTestId("file-create-dialog")).toHaveCount(0);
    await expect(tree.getByText("created-from-header.txt")).toBeVisible({ timeout: 20_000 });

    // It is a real file, not just a row: the server lists it.
    const listing = (await (
      await page.request.get(`/v1/sessions/${sessionId}/files/tree?depth=2`)
    ).json()) as { nodes: { name: string; path: string }[] };
    expect(listing.nodes.some((node) => node.path === "created-from-header.txt")).toBe(true);
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
