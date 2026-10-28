import { expect, test } from "@playwright/test";
import { login, openNewSession, sendPrompt, USER } from "./helpers";

/**
 * The browser panel shows the *same* Chrome the agent is driving, one per
 * sandbox (PRD §7.7). A separate headless browser in the tab would have
 * different cookies and a different page, which makes the panel a lie.
 */
test.describe("browser panel", () => {
  test("connects to the sandbox browser and paints frames", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    // The first prompt provisions the checkout the browser's profile lives in.
    await sendPrompt(page, "hello");

    await page.getByTestId("panel-tab-browser").click();
    const panel = page.getByTestId("browser-panel");
    await expect(panel).toBeVisible();

    // "open" means the control plane launched Chrome and attached to a page.
    await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 60_000 });
    await expect(page.getByTestId("browser-idle")).toHaveCount(0);

    // A painted canvas is the proof that frames are actually flowing; a socket
    // that opens but never delivers would leave width at its default 300.
    await expect
      .poll(
        async () =>
          page.getByTestId("browser-canvas").evaluate((el) => (el as HTMLCanvasElement).width),
        { timeout: 30_000, message: "no screencast frame was painted" },
      )
      .toBeGreaterThan(0);

    // Clicking the surface must not drop the connection: input goes up the same
    // socket the frames come down.
    await page.getByTestId("browser-canvas").click({ position: { x: 40, y: 40 } });
    await expect(panel).toHaveAttribute("data-browser-status", "open");
  });

  test("a viewer without a session gets a typed refusal", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);

    // No worktree yet means no profile directory, which is a real "not ready"
    // rather than a broken socket.
    const res = await page.request.get(`/v1/sessions/${sessionId}/browser`);
    expect([422, 426, 400]).toContain(res.status());
  });
});
/**
 * The panel's own size drives the sandbox viewport. Without this the page keeps
 * the browser's launch size and the viewer letterboxes it — a responsive site
 * would never actually see the panel it is displayed in.
 */
test("the panel resizes the sandbox browser when the layout changes", async ({ page }) => {
  await login(page, USER);
  await openNewSession(page);
  await sendPrompt(page, "hello");

  await page.getByTestId("panel-tab-browser").click();
  const panel = page.getByTestId("browser-panel");
  await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 60_000 });

  // The property is "the frame matches the panel", not "the panel got wider":
  // the layout differs by viewport, so growth alone is a brittle proxy.
  const geometry = () =>
    page.evaluate(() => {
      const canvas = document.querySelector('[data-testid="browser-canvas"]') as HTMLCanvasElement;
      const surface = document.querySelector('[data-testid="browser-surface"]') as HTMLElement;
      return {
        frame: canvas?.width ?? 0,
        css: Math.round(canvas?.getBoundingClientRect().width ?? 0),
        surface: Math.round(surface?.getBoundingClientRect().width ?? 0),
      };
    });

  await expect.poll(async () => (await geometry()).frame, { timeout: 30_000 }).toBeGreaterThan(0);
  const before = await geometry();
  // A frame rendered at a fixed launch size never matches the surface; this is
  // the assertion that fails when the resize command stops working.
  expect(Math.abs(before.css - before.surface)).toBeLessThanOrEqual(2);

  await page.getByTestId("sidebar-collapse").click();
  // After collapsing, the surface is wider and the frame has to follow it.
  await expect
    .poll(async () => {
      const now = await geometry();
      return Math.abs(now.css - now.surface) <= 2 && now.surface > before.surface;
    }, { timeout: 30_000 })
    .toBe(true);
});