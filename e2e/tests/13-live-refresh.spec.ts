import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, openSession, sendPrompt } from "./helpers";

/**
 * A run changes the workspace and, on the first message, the session's name.
 * Neither was pushed to the client, so both stayed stale until a reload: the
 * file tree kept saying 目录为空 after the agent created a file, and the header
 * kept saying 未命名会话 after the server had named the session.
 */
test.describe("a settled run refreshes what it changed", () => {
  test("the header picks up the title the first message produced", async ({ page }) => {
    await login(page, USER);
    // An untitled session: the dialog promises the first message names it.
    const created = await apiWrite(page, "POST", "/v1/projects", {
      name: `e2e-untitled-${Date.now()}`,
      default_branch: "main",
    });
    expect(created.status).toBeLessThan(300);
    const projectId = (created.body as { project: { id: string } }).project.id;
    const sessionId = await openSession(page, projectId, "");
    await sendPrompt(page, "name this session from my words");

    const header = page.getByTestId("session-title");
    await expect(header).toContainText("name this session", { timeout: 30_000 });
    await expect(page.getByText("未命名会话")).toHaveCount(0);
    await expect
      .poll(async () => {
        const res = await page.request.get(`/v1/sessions/${sessionId}`);
        const body = (await res.json()) as { session: { title: string } };
        return body.session.title;
      }, { timeout: 15_000 })
      .toContain("name this session");
  });

  test("the file tree picks up a file written during a run", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "first run settles the checkout");

    // The panel is mounted and has already listed an (empty) tree.
    await page.getByTestId("panel-tab-files").click();
    await expect(page.getByTestId("file-tree")).toBeVisible({ timeout: 30_000 });

    // A file appears while the panel is watching — exactly what the agent does
    // mid-run. Then a run settles, which is the signal the tree must react to.
    expect(
      (await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=written-by-agent.txt`, "hello\n")).status,
    ).toBeLessThan(300);
    // Sent through the API on purpose: this test is about what the client does
    // when a run settles, not about the composer. Waiting for a "running" badge
    // the fake peer can finish before the first poll would only add flakiness.
    const sent = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
      client_msg_id: `refresh-${Date.now()}`,
      text: "second run",
    });
    expect(sent.status).toBeLessThan(300);

    // No reload, no 刷新文件树: the settle signal is the only thing that changed.
    await expect(
      page.getByTestId("file-tree").getByText("written-by-agent.txt"),
    ).toBeVisible({ timeout: 30_000 });
  });
});