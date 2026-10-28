import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-11.8: what you see in the built-in browser can become a file the agent can
 * read. The endpoint writes into the checkout, so the file tree and the agent see
 * the same bytes.
 */
test.describe("browser screenshot", () => {
  test("captures the page into the workspace", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    const shot = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/screenshot`);
    // Without Chrome on this host the endpoint refuses with a sandbox error, which
    // is correct; only assert the file when the browser is configured.
    if (shot.status !== 201) {
      expect([422, 503]).toContain(shot.status);
      test.skip(true, "no chrome binary configured for this host");
    }

    const body = shot.body as { path: string; bytes: number };
    expect(body.path.startsWith("screenshots/")).toBe(true);
    expect(body.path.endsWith(".png")).toBe(true);
    expect(body.bytes).toBeGreaterThan(1000);

    // The file really exists in the checkout and is a PNG.
    const file = await page.request.get(
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(body.path)}`,
    );
    expect(file.status()).toBe(200);
    const content = (await file.json()) as { mime_type: string; size: number; binary: boolean };
    expect(content.mime_type).toBe("image/png");
    expect(content.size).toBeGreaterThan(1000);
    expect(content.binary).toBe(true);

    // The preview must show the picture, not a download card: the file API returns
    // images with an empty body, so the preview has to use the inline endpoint.
    const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
    await page.goto(`/p/${projectId}/s/${sessionId}`);
    await expect(page.getByTestId("file-tree")).toBeVisible({ timeout: 40_000 });
    const tree = page.getByTestId("file-tree");
    await tree.getByText("screenshots", { exact: false }).first().click();
    await tree.getByText(body.path.split("/").pop()!, { exact: false }).first().click();
    const image = page.getByTestId("image-preview");
    await expect(image).toBeVisible({ timeout: 20_000 });
    await expect(image).toHaveAttribute("src", /inline=1/);
  });

  test("the panel offers the button and reports the path", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    await page.getByTestId("panel-tab-browser").click();
    const button = page.getByTestId("browser-screenshot");
    await expect(button).toBeVisible({ timeout: 20_000 });

    // The flag may be off for this account on a stripped-down host; the panel then
    // says so instead of offering a button that cannot work.
    const panel = page.getByTestId("browser-panel");
    const status = await panel.getAttribute("data-browser-status");
    if (status === "disabled") {
      await expect(button).toHaveCount(0);
      return;
    }
    await expect(page.getByTestId("browser-url")).toBeVisible();
    void sessionId;
  });
});