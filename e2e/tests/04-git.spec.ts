import { expect, test } from "@playwright/test";
import { apiWrite, login, openNewSession, sendPrompt, USER } from "./helpers";

test.describe("git", () => {
  test("an untracked file is staged, committed and appears in the graph", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // 1. Write a file -> it must show up as untracked in status.
    const write = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("NOTES.md")}`,
      "notes from the e2e suite\n",
    );
    expect(write.status).toBeLessThan(300);

    const status = await page.request.get(`/v1/sessions/${sessionId}/git/status`);
    expect(status.status()).toBe(200);
    const before = await status.json();
    expect(before.clean).toBe(false);
    const change = before.changes.find((c: { path: string }) => c.path === "NOTES.md");
    expect(change, `NOTES.md missing from ${JSON.stringify(before.changes)}`).toBeTruthy();

    // 2. Stage it.
    const staged = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/stage`, {
      paths: ["NOTES.md"],
    });
    expect(staged.status).toBeLessThan(300);

    // 3. Commit it.
    const committed = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
      message: "test: add notes",
      paths: ["NOTES.md"],
    });
    expect(committed.status).toBeLessThan(300);
    const sha = (committed.body as { commit: string }).commit;
    expect(sha).toMatch(/^[0-9a-f]{7,40}$/);

    // 4. The commit must be in the graph the panel renders.
    const graph = await page.request.get(`/v1/sessions/${sessionId}/git/log?limit=20`);
    expect(graph.status()).toBe(200);
    const gitGraph = await graph.json();
    expect(gitGraph.head).toBe(sha);
    const hasCommit = gitGraph.commits.some(
      (c: { hash: string; subject: string }) => c.hash === sha && c.subject.includes("add notes"),
    );
    expect(hasCommit).toBe(true);

    // 5. And the panel shows it. The commit was made through the API, so the
    //    open panel is stale by design; reload to model reopening the session.
    await page.reload();
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("panel-tab-git").click();
    await expect(page.getByTestId("git-graph")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator(`[data-testid="git-graph-row"][data-hash="${sha}"]`)).toBeVisible({
      timeout: 20_000,
    });
  });

  test("the branch is reported and platform files stay out of the diff", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    const status = await page.request.get(`/v1/sessions/${sessionId}/git/status`);
    expect(status.status()).toBe(200);
    const body = await status.json();

    expect(body.branch).toBeTruthy();
    // The agent's own edits belong in the diff — that is the point of the
    // panel. What must never appear is the platform's tooling: a mounted
    // skill, an MCP config or a browser profile in someone's working tree is
    // the platform quietly committing itself into their repository.
    const paths = (body.changes as { path: string }[]).map((c) => c.path).join("\n");
    expect(paths).not.toContain(".pi/");
    expect(paths).not.toContain(".mcp.json");
    expect(paths).not.toContain(".piforge-browser");
  });
  test("a diff is produced for an unstaged modification", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // Create, commit, then modify: this gives a real unstaged diff.
    await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("A.txt")}`, "one\n");
    await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/stage`, { paths: ["A.txt"] });
    await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
      message: "add A",
      paths: ["A.txt"],
    });
    await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("A.txt")}`, "one\ntwo\n");

    const diff = await page.request.get(`/v1/sessions/${sessionId}/git/diff?scope=unstaged`);
    expect(diff.status()).toBe(200);
    const body = await diff.json();
    const file = body.diffs.find((d: { path: string }) => d.path === "A.txt");
    expect(file, JSON.stringify(body.diffs)).toBeTruthy();
    expect(file.additions).toBeGreaterThanOrEqual(1);
    expect(file.patch).toContain("+two");
  });
});
