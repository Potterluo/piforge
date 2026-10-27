import { expect, test } from "@playwright/test";
import { apiWrite, login, openNewSession, sendPrompt, USER } from "./helpers";

test.describe("workspace panels", () => {
  test("a written file appears in the tree and previews with highlighting", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);

    // The first prompt provisions the sandbox and the worktree.
    await sendPrompt(page, "hello");

    const write = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("internal/cache/README.md")}`,
      "# Cache\n\nThe eviction race lives in lru.go.\n",
    );
    expect(write.status).toBeLessThan(300);

    // The file was written through the API, so the open panel has no reason to
    // know about it yet; reload to model "coming back to the session later".
    await page.reload();
    await expect(page.getByTestId("composer-input")).toBeVisible({ timeout: 20_000 });

    // The tree must show it under the expanded directories.
    await page.getByTestId("panel-tab-files").click();
    await expect(page.getByTestId("file-tree")).toBeVisible();

    // Expand the whole path: the tree is lazy, so click each level. Paths are
    // workspace-relative, so a nested directory's data-path includes its parent.
    for (const dir of ["internal", "internal/cache"]) {
      const node = page.locator(`[data-testid="file-node"][data-path="${dir}"]`).first();
      await expect(node).toBeVisible({ timeout: 20_000 });
      await node.click();
    }

    const fileNode = page.locator(`[data-testid="file-node"][data-path="internal/cache/README.md"]`);
    await expect(fileNode).toBeVisible({ timeout: 20_000 });
    await fileNode.click();

    await expect(page.getByTestId("file-preview")).toBeVisible();
    await expect(page.getByTestId("file-preview-body")).toContainText("eviction race");
  });

  test("path traversal is refused by the API", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    for (const attack of [
      "../../../../etc/passwd",
      "..%2f..%2fetc%2fpasswd",
      "C:\\Windows\\win.ini",
      "/etc/passwd",
    ]) {
      const res = await page.request.get(
        `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(attack)}`,
      );
      expect([400, 403, 404, 422], `attack ${attack} leaked: ${res.status()}`).toContain(
        res.status(),
      );
    }
  });

  test("an unprovisioned session reports the missing workspace instead of failing blank", async ({
    page,
  }) => {
    await login(page, USER);

    // Create a session but never prompt it, so no worktree is attached yet.
    const res = await apiWrite(page, "POST", "/v1/projects", {
      name: `no-wt-${Date.now()}`,
      repo_url: "",
      default_branch: "main",
    });
    const project = (res.body as { project: { id: string } }).project;
    const created = await apiWrite(page, "POST", `/v1/projects/${project.id}/sessions`, {
      title: "unprovisioned",
    });
    const session = (created.body as { session: { id: string } }).session;

    const tree = await page.request.get(`/v1/sessions/${session.id}/files/tree`);
    expect([200, 422, 503]).toContain(tree.status());
    if (tree.status() !== 200) {
      const body = await tree.json();
      expect(["sandbox_unavailable", "sandbox_paused"]).toContain(body.error.code);
    }
  });

  test("the terminal surfaces its honest disconnected state", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    await page.getByTestId("panel-tab-terminal").click();
    // The PTY relay is a documented seam, not a fake shell.
    await expect(page.getByTestId("terminal-disconnected")).toBeVisible({ timeout: 20_000 });
  });
});
