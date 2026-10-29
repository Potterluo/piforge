import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { apiWrite, login, openNewSession, sendPrompt, USER } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-follow-compact");
mkdirSync(ARTIFACTS, { recursive: true });

/** The 上下文用量 badge's percentage, e.g. "75%" → 75. */
async function meterPercent(page: Page): Promise<number> {
  const text = await page.getByTestId("context-meter").textContent();
  const match = /(\d+)%/.exec(text ?? "");
  return match ? Number(match[1]) : -1;
}

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

  /**
   * FR-1.9: a compaction is only "visible" if the transcript says so and the
   * context meter actually falls. Checking the entries table instead let a
   * broken marker render pass.
   */
  test("a compaction shows a marker in the transcript and drops the context meter", async ({
    page,
  }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    // The marker makes the scripted run report a nearly full context, so the
    // compaction has a "before" worth shrinking.
    await sendPrompt(page, "look at the cache [[big-context]]");

    await expect(page.getByTestId("context-meter")).toBeVisible({ timeout: 20_000 });
    const before = await meterPercent(page);
    expect(before).toBeGreaterThan(50);

    const res = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/compact`, {
      custom_instructions: "",
    });
    expect(res.status).toBe(200);
    expect((res.body as { compacted: boolean }).compacted).toBe(true);

    const marker = page.getByText(/上下文已压缩/).first();
    await expect(marker).toBeVisible({ timeout: 20_000 });
    await expect(marker).toContainText("手动触发");
    await expect(marker).toContainText("150k → 32k tokens");

    // The meter is the other half of "visible": the window really got smaller.
    await expect
      .poll(async () => meterPercent(page), { timeout: 20_000 })
      .toBeLessThan(before - 20);
    await page.screenshot({
      path: path.join(ARTIFACTS, "compaction-marker-and-meter.png"),
    });
  });

  /**
   * FR-1.9: Pi refuses to compact a session that is still small. That is an
   * answer, not a server fault: the API must return a readable no-op (it used
   * to answer 500 "Nothing to compact").
   */
  test("compacting a small session is a no-op with a reason, not a 500", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    const res = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/compact`, {
      custom_instructions: "",
    });
    expect(res.status).toBe(200);
    expect(res.body).toMatchObject({
      compacted: false,
      reason: "没有可压缩的上下文",
    });
    // A no-op must not decorate the transcript with a marker for a compaction
    // that never happened.
    await expect(page.getByText(/上下文已压缩/)).toHaveCount(0);
  });
});
