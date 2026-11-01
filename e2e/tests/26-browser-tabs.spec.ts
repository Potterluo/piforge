import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-qa16");
mkdirSync(ARTIFACTS, { recursive: true });

/**
 * FR-11.7: more than one page per sandbox. The tabs must be independent — opening a
 * second one must not disturb the first, and closing one must not take the browser
 * with it.
 */
test("the browser panel keeps several pages", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  await page.getByTestId("panel-tab-browser").click();
  const tabs = page.getByTestId("browser-tab");
  await expect(tabs.first()).toBeVisible({ timeout: 40_000 });
  expect(await tabs.count()).toBeGreaterThanOrEqual(1);

  // Open a second page through the API, then find it in the strip.
  //
  // A public *literal* address, not a name: the control plane's G5 rule resolves
  // a hostname to decide whether it is loopback or private, and this suite runs
  // offline, where a name would be an unrelated DNS refusal. The page does not
  // need to load — the test is about the tab, its viewer and its close button.
  const opened = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/tabs`, {
    url: "https://93.184.216.34/second",
  });
  expect(opened.status).toBe(201);
  const id = (opened.body as { tab: { id: string } }).tab.id;

  await expect.poll(async () => tabs.count(), { timeout: 20_000 }).toBeGreaterThanOrEqual(2);
  const second = page.locator(`[data-testid="browser-tab"][data-tab="${id}"]`);
  await expect(second).toBeVisible({ timeout: 20_000 });

  // Selecting it makes it the active tab of the viewer — and the viewer must actually
  // show that page. The strip alone passing is how a blank canvas shipped once: the
  // socket was opened with `?tab=view&tab=<id>`, the server read "view", and every
  // tab looked connected while nothing was being painted.
  await second.click();
  await expect(second).toHaveAttribute("data-active", "1", { timeout: 20_000 });
  const panel = page.getByTestId("browser-panel");
  await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 60_000 });
  await expect
    .poll(
      async () =>
        page.evaluate(() => {
          const canvas = document.querySelector('[data-testid="browser-canvas"]') as HTMLCanvasElement;
          return canvas?.width ?? 0;
        }),
      { timeout: 90_000 },
    )
    .toBeGreaterThan(0);

  // Closing it goes back to one page. Asserted against the server as well: the strip
  // is a 5s poll, so reading only the DOM can report a stale count.
  await page.locator(`[data-testid="browser-tab-close"][data-tab="${id}"]`).click();
  await expect
    .poll(
      async () =>
        page.evaluate(async ({ tabId, sessionId }) => {
          const response = await fetch(`/v1/sessions/${sessionId}/browser/tabs`);
          if (!response.ok) return -1;
          const body = (await response.json()) as { tabs?: { id: string }[] };
          return (body.tabs ?? []).some((tab) => tab.id === tabId) ? 2 : 1;
        }, { tabId: id, sessionId }),
      { timeout: 45_000 },
    )
    .toBe(1);
  await expect.poll(async () => tabs.count(), { timeout: 90_000 }).toBe(1);

  // The API agrees, and refuses to close the last page.
  // A busy browser legitimately answers 503 while it starts; poll rather than
  // asserting on a body that may be an error.
  let list: { tabs: { id: string }[] } = { tabs: [] };
  await expect
    .poll(
      async () => {
        const response = await page.request.get(`/v1/sessions/${sessionId}/browser/tabs`);
        if (response.status() !== 200) return 0;
        const body = (await response.json()) as { tabs?: { id: string }[] };
        list = { tabs: body.tabs ?? [] };
        return list.tabs.length;
      },
      { timeout: 90_000 },
    )
    .toBe(1);
  const refused = await apiWrite(
    page,
    "DELETE",
    `/v1/sessions/${sessionId}/browser/tabs/${list.tabs[0].id}`,
  );
  expect(refused.status).toBe(400);
});

/**
 * 新标签页 is a blank page, not a copy of the page in front (QA-16, FIX D).
 *
 * The old plus button opened a tab pointed at the URL bar's value, so pressing it
 * twice produced the same site twice. `about:blank` was unreachable as well: the
 * address bar prefixed `https://` onto it, and the server refused every non-http
 * URL — which also made the blank-capture refusal from FR-11.8 impossible to hit
 * from the UI.
 */
test("新标签页 opens a blank page and capturing it is refused", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  await page.getByTestId("panel-tab-browser").click();
  const panel = page.getByTestId("browser-panel");
  await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 40_000 });

  // Start from a real page: a copy of the current URL is what used to happen.
  await page.getByTestId("browser-url").fill("http://127.0.0.1:3100/login");
  await page.getByTestId("browser-url").press("Enter");
  await expect(panel).toHaveAttribute("data-browser-painted", "1", { timeout: 30_000 });

  const before = await page.getByTestId("browser-tab").count();
  await page.getByTestId("browser-tab-add").click();

  // The address bar says what the new page is, and the server agrees: a blank
  // target, not the login page again.
  await expect(page.getByTestId("browser-url")).toHaveValue("about:blank", { timeout: 20_000 });
  await expect
    .poll(async () => {
      const response = await page.request.get(`/v1/sessions/${sessionId}/browser/tabs`);
      if (response.status() !== 200) return -1;
      const body = (await response.json()) as { tabs?: { id: string; url: string }[] };
      return (body.tabs ?? []).filter((tab) => tab.url === "about:blank").length;
    }, { timeout: 30_000 })
    .toBe(1);
  await expect(page.getByTestId("browser-tab")).toHaveCount(before + 1, { timeout: 20_000 });
  await page.screenshot({ path: path.join(ARTIFACTS, "D-new-tab-about-blank.png") });

  // And the blank page is capturable enough to be refused, with the message that
  // names the reason rather than "浏览器正忙".
  await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 40_000 });
  const shoot = page.getByTestId("browser-screenshot");
  await expect(shoot).toBeEnabled({ timeout: 30_000 });
  await shoot.click();
  const toast = page.locator("[data-sonner-toast]").filter({ hasText: "截图失败" });
  await expect(toast).toBeVisible({ timeout: 30_000 });
  await expect(toast).toContainText("空白");
  await expect(toast).toContainText("about:blank");
  await page.screenshot({ path: path.join(ARTIFACTS, "D-blank-capture-refused.png") });
});