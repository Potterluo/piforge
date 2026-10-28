import { expect, test } from "@playwright/test";
import { apiWrite, login, openNewSession, sendPrompt, USER } from "./helpers";

// Creating a project and a session before the panels can render takes a few
// round trips; under a full-suite load the 60s budget on this file was the first
// thing to run out. The assertions are unchanged.
test.setTimeout(120_000);

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

  test("the terminal opens a real PTY in the sandbox", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    await page.getByTestId("panel-tab-terminal").click();

    // "open" means the WebSocket relay accepted, the PTY was spawned and the
    // pane is forwarding bytes — not that a placeholder is showing.
    await expect(page.getByTestId("terminal-pane")).toHaveAttribute(
      "data-pty-status",
      "open",
      { timeout: 30_000 },
    );
    await expect(page.getByTestId("terminal-disconnected")).toHaveCount(0);
  });

  test("keystrokes reach the shell and output comes back", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);

    // Drive the protocol directly: this asserts the relay contract (binary
    // frames up, binary frames down) without depending on how xterm paints.
    const output = await page.evaluate(async (id) => {
      return await new Promise<string>((resolve) => {
        const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
        const ws = new WebSocket(`${proto}//${window.location.host}/v1/sessions/${id}/pty?tab=e2e`);
        ws.binaryType = "arraybuffer";
        let text = "";
        const done = (value: string) => {
          try {
            ws.close();
          } catch {
            /* already closed */
          }
          resolve(value);
        };
        const timer = setTimeout(() => done(text || "TIMEOUT"), 20_000);

        ws.onmessage = (event) => {
          if (typeof event.data === "string") {
            const msg = JSON.parse(event.data) as { type?: string };
            if (msg.type === "ready") {
              ws.send(new TextEncoder().encode("echo piforge-pty-ok\r"));
            }
            return;
          }
          text += new TextDecoder().decode(new Uint8Array(event.data));
          if (text.includes("piforge-pty-ok")) {
            clearTimeout(timer);
            done(text);
          }
        };
        ws.onerror = () => done("SOCKET_ERROR");
      });
    }, sessionId);

    expect(output).toContain("piforge-pty-ok");
  });
});
