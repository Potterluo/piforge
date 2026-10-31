import { expect, test } from "@playwright/test";
import { login, openNewSession, sendPrompt, USER } from "./helpers";

/**
 * The dual-queue semantics are the product's sharpest edge: while a run is in
 * flight, plain Enter QUEUES (follow_up) and Cmd/Ctrl+Enter INTERRUPTS and
 * redirects (steer). Both are verified through the response the server sends,
 * then through the transcript, because a queue chip is transient by design —
 * the run is only ~2s long and the queued turn is consumed almost immediately.
 */
test.describe("conversation", () => {
  test("a full run streams a reply, a tool call and settles", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);

    await page.getByTestId("composer-input").fill("fix the cache eviction race");
    await page.getByTestId("composer-send").click();

    await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "running", {
      timeout: 20_000,
    });
    await expect(page.getByTestId("turn-user").first()).toContainText("cache eviction");

    // Icon-only controls need an accessible name (acceptance pass 3, minor).
    // The stop button's visible text is 停止 plus a key hint, so its computed
    // name used to be "停止 Esc"; the explicit label makes it exact. While a run
    // is in flight the send control is the labelled 下一轮执行 button.
    await expect(page.getByRole("button", { name: "下一轮执行" })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("composer-stop")).toHaveAttribute("aria-label", "停止");

    // The scripted peer always emits one bash tool call, folded into the run's single tool
    // row. Opening it **mid-run** used to collapse again under the user's finger: the row
    // was keyed by its position, so every appended block remounted it.
    await expect(page.getByTestId("tool-group").first()).toContainText("个工具调用", {
      timeout: 30_000,
    });
    await page.getByTestId("tool-group").first().click();
    await expect
      .poll(async () => page.getByTestId("tool-trace").count(), { timeout: 20_000 })
      .toBeGreaterThan(0);

    await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
      timeout: 60_000,
    });
    // Back to the idle composer: the arrow button's accessible name is 发送.
    await expect(page.getByTestId("composer-send")).toHaveAttribute("aria-label", "发送");

    expect(await page.getByTestId("turn-assistant").count()).toBeGreaterThanOrEqual(2);
  });

  test("Enter queues a follow-up while running, Ctrl+Enter steers", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);

    await page.getByTestId("composer-input").fill("run the tests");
    await page.getByTestId("composer-send").click();
    await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "running", {
      timeout: 20_000,
    });

    // Plain Enter while running must submit a follow_up, not a new run.
    const queued = page.waitForResponse(
      (res) =>
        res.url().includes(`/v1/sessions/${sessionId}/messages`) &&
        res.request().method() === "POST",
      { timeout: 15_000 },
    );
    await page.getByTestId("composer-input").fill("also update the changelog");
    await page.getByTestId("composer-input").press("Enter");
    // Asserted while the run was observably in flight, so this must be a queue.
    expect((await (await queued).json()).disposition).toBe("queued");

    // Ctrl+Enter is the explicit "change your mind now" path.
    const steered = page.waitForResponse(
      (res) => res.url().includes(`/v1/sessions/${sessionId}/messages`),
      { timeout: 15_000 },
    );
    await page.getByTestId("composer-input").fill("stop, only analyse");
    await page.getByTestId("composer-input").press("Control+Enter");
    // The run may end between the keystroke and the request; either answer is
    // legitimate, and the transcript check below proves the message was kept.
    expect(["queued", "started"]).toContain((await (await steered).json()).disposition);

    // Both messages eventually run, so the transcript gains their user turns.
    await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
      timeout: 90_000,
    });
    await expect(page.getByTestId("turn-user")).toHaveCount(3, { timeout: 90_000 });
  });

  test("the conversation survives a reload", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "what does the cache package do");

    const before = await page.getByTestId("turn-assistant").count();
    expect(before).toBeGreaterThan(0);

    await page.reload();
    await expect(page.locator(`[data-session-id="${sessionId}"]`)).toBeVisible();
    // History is replayed from the persisted event log, so nothing is lost.
    await expect(page.getByTestId("turn-user").first()).toBeVisible({ timeout: 20_000 });
    expect(await page.getByTestId("turn-assistant").count()).toBeGreaterThanOrEqual(before);
  });

  test("the event log stores the authoritative final message", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "summarise the repo");

    const res = await page.request.get(`/v1/sessions/${sessionId}/entries`);
    expect(res.status()).toBe(200);
    const body = await res.json();
    // The wire shape is the projected TreeEntry, not Pi's raw message: role and
    // a preview live at the top level.
    const messages = body.entries.filter((e: { type: string }) => e.type === "message");
    expect(messages.length).toBeGreaterThanOrEqual(3);

    const last = messages[messages.length - 1] as { role?: string; preview?: string };
    expect(last.role).toBe("assistant");
    expect(last.preview ?? "").toContain("eviction race");
  });
});
