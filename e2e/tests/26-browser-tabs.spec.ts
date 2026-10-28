import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

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
  const opened = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/tabs`, {
    url: "https://example.com/second",
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
      { timeout: 30_000 },
    )
    .toBeGreaterThan(0);

  // Closing it goes back to one page.
  await page.locator(`[data-testid="browser-tab"][data-tab="${id}"]`).locator("xpath=..").getByTestId("browser-tab-close").click();
  await expect
    .poll(async () => tabs.count(), { timeout: 20_000 })
    .toBe(1);

  // The API agrees, and refuses to close the last page.
  const list = (await (await page.request.get(`/v1/sessions/${sessionId}/browser/tabs`)).json()) as {
    tabs: { id: string }[];
  };
  expect(list.tabs.length).toBe(1);
  const refused = await apiWrite(
    page,
    "DELETE",
    `/v1/sessions/${sessionId}/browser/tabs/${list.tabs[0].id}`,
  );
  expect(refused.status).toBe(400);
});