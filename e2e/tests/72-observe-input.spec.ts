import { expect, test, type Page } from "@playwright/test";
import http from "node:http";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-11.2: observation is the default and it means the page does not receive
 * the user's clicks or keystrokes. The panel still sends them over the socket —
 * it is the server's job to drop them — so a test that only checks the mode
 * button would pass even if the guard disappeared.
 *
 * The proof is the page itself: it writes the last event it saw into its title,
 * and the tab list reports that title. In observe the title must not move; in
 * takeover the same click and key must land.
 */

interface Tab {
  id: string;
  url: string;
  title: string;
}

async function listedTab(page: Page, sessionId: string, prefix: string): Promise<Tab | undefined> {
  const response = await page.request.get(`/v1/sessions/${sessionId}/browser/tabs`);
  if (response.status() !== 200) return undefined;
  const body = (await response.json()) as { tabs?: Tab[] };
  return (body.tabs ?? []).find((tab) => tab.url.startsWith(prefix));
}

test("observe mode drops clicks and keys, takeover delivers them", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  // A page whose title is its own event log. Any click or key that reaches it
  // produces a title the test can read without screenshot OCR.
  const server = http.createServer((_req, res) => {
    res.setHeader("content-type", "text/html; charset=utf-8");
    res.end(`<!doctype html>
<html><head><title>observe-target</title></head>
<body style="margin:0;height:100vh;background:#fff">
  <p id="hint">observe target</p>
  <script>
    addEventListener("click", () => { document.title = "clicked"; });
    addEventListener("keydown", (event) => { document.title = "key-" + event.key; });
  </script>
</body></html>`);
  });
  // A host-placement browser may only open loopback ports the deployment
  // published (docs/DATA-FLOW-AUDIT.md G5). The e2e control plane publishes 3100
  // for the UI and 3101 for this fixture server (playwright.config.ts), so the
  // page is a published dev server rather than an arbitrary loopback port.
  const port = 3101;
  await new Promise<void>((resolve) => server.listen(port, "127.0.0.1", resolve));
  const origin = `http://127.0.0.1:${port}`;

  try {
    await page.getByTestId("panel-tab-browser").click();
    const panel = page.getByTestId("browser-panel");
    await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 60_000 });

    // Default is observe; ask the server rather than trusting the button text.
    const mode = await (await page.request.get(`/v1/sessions/${sessionId}/browser/mode`)).json();
    expect((mode as { mode: string }).mode).toBe("observe");

    await page.getByTestId("browser-url").fill(origin);
    await page.getByTestId("browser-url").press("Enter");
    await expect(panel).toHaveAttribute("data-browser-painted", "1", { timeout: 45_000 });

    await expect
      .poll(async () => (await listedTab(page, sessionId, origin))?.title, { timeout: 30_000 })
      .toBe("observe-target");
    const tab = await listedTab(page, sessionId, origin);
    if (!tab) throw new Error("the page never appeared in the tab list");

    // Observe: the panel sends this; the server must drop it.
    await page.getByTestId("browser-canvas").click({ position: { x: 120, y: 80 } });
    await page.getByTestId("browser-surface").focus();
    await page.keyboard.press("z");
    // Give a wrongly-forwarded event every chance to arrive before asserting
    // that none did.
    await page.waitForTimeout(1_500);
    await expect
      .poll(async () => (await listedTab(page, sessionId, origin))?.title)
      .toBe("observe-target");

    // Takeover: the same two actions must now reach the very same page.
    const taken = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/mode`, {
      mode: "takeover",
    });
    expect(taken.status).toBe(200);

    await page.getByTestId("browser-canvas").click({ position: { x: 120, y: 80 } });
    await expect
      .poll(async () => (await listedTab(page, sessionId, origin))?.title, { timeout: 20_000 })
      .toBe("clicked");

    await page.getByTestId("browser-surface").focus();
    await page.keyboard.press("b");
    await expect
      .poll(async () => (await listedTab(page, sessionId, origin))?.title, { timeout: 20_000 })
      .toBe("key-b");
  } finally {
    // Chrome keeps the page's HTTP connection alive, and `close` waits for open
    // connections: without this the test body finishes and then sits in cleanup
    // until Playwright's timeout.
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
