import { expect, test } from "@playwright/test";
import { apiWrite, login, openNewSession, sendPrompt, USER } from "./helpers";

interface SessionBody {
  session: { id: string; kind: string; parent_id?: string; worktree_id?: string; label?: string };
}

test.describe("branches, forks and sidecars", () => {
  test("forking a user message creates a branch session with its own worktree", async ({
    page,
  }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "first task for the fork test");

    // Pi only allows forking at a user message, so the list must be non-empty
    // once one prompt has been sent.
    const pointsRes = await page.request.get(`/v1/sessions/${parentId}/fork-points`);
    expect(pointsRes.status()).toBe(200);
    const { points } = await pointsRes.json();
    expect(points.length).toBeGreaterThan(0);

    const forkRes = await apiWrite(page, "POST", `/v1/sessions/${parentId}/fork`, {
      entry_id: points[0].entry_id,
      label: "alt-approach",
    });
    expect(forkRes.status).toBeLessThan(300);
    const branch = (forkRes.body as SessionBody).session;

    expect(branch.kind).toBe("branch");
    expect(branch.parent_id).toBe(parentId);
    expect(branch.label).toBe("alt-approach");

    const parent = (await (await page.request.get(`/v1/sessions/${parentId}`)).json()) as {
      session: { worktree_id?: string };
    };
    // A branch must own a different worktree than its parent, otherwise the two
    // agents would fight over the same files.
    expect(branch.worktree_id).toBeTruthy();
    expect(branch.worktree_id).not.toBe(parent.session.worktree_id);
  });

  test("a sidecar shares its parent's worktree", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "sidecar parent task");

    const parent = (await (await page.request.get(`/v1/sessions/${parentId}`)).json()) as {
      session: { worktree_id?: string };
    };

    const res = await apiWrite(page, "POST", `/v1/sessions/${parentId}/sidecar`, {});
    expect(res.status).toBeLessThan(300);
    const side = (res.body as SessionBody).session;

    expect(side.kind).toBe("sidecar");
    expect(side.parent_id).toBe(parentId);
    // The whole point of a sidecar: same files, separate context.
    expect(side.worktree_id).toBe(parent.session.worktree_id);
  });

  test("the fork dialog on the workbench produces a second session", async ({ page }) => {
    await login(page, USER);
    const parentId = await openNewSession(page);
    await sendPrompt(page, "fork me from the ui");

    await page.getByTestId("fork-button").first().click();
    await page.getByTestId("fork-label").fill("ui-branch");
    await page.getByTestId("fork-submit").click();

    // The workbench navigates to the new branch.
    await expect(page.locator("[data-session-id]").first()).not.toHaveAttribute(
      "data-session-id",
      parentId,
      { timeout: 30_000 },
    );
  });

  test("opening a sidecar shows the dock and accepts a prompt", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    await sendPrompt(page, "main task");

    await page.getByTestId("sidecar-open").click();
    await expect(page.getByTestId("sidecar-dock")).toBeVisible({ timeout: 20_000 });

    await page.getByTestId("sidecar-input").fill("what does this repo do?");
    await page.getByTestId("sidecar-send").click();

    // The sidecar has its own transcript; it must not inject into the main one.
    await expect(page.getByTestId("sidecar-dock")).toContainText(/pass|fail|缓存|测试|cache/i, {
      timeout: 40_000,
    });
  });

  test("compaction is recorded as a visible context event", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "compact me");

    const res = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/compact`, {
      custom_instructions: "",
    });
    expect(res.status).toBeLessThan(300);

    const entries = await (await page.request.get(`/v1/sessions/${sessionId}/entries`)).json();
    const hasCompaction = entries.entries.some((e: { type: string }) => e.type === "compaction");
    expect(hasCompaction).toBe(true);
  });
});
