import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-11.8: what you see in the built-in browser can become a file the agent can
 * read. The endpoint writes into the checkout, so the file tree and the agent see
 * the same bytes.
 *
 * The old spec asserted `bytes > 1000`. The defect it missed: with the panel on a
 * real page the endpoint captured a different target and wrote a 661×750
 * all-white 3008-byte PNG of `about:blank`, which passes any byte-count check.
 * So this spec navigates first and then *decodes* the PNG: a white rectangle has
 * one colour, and a page cannot.
 */

/** The page the sandbox browser navigates to. It is served by the e2e webServer. */
const REAL_PAGE = "http://127.0.0.1:3100/login";

test.describe("browser screenshot", () => {
  test("saves the page the panel is showing, and the image is not blank", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    await page.getByTestId("panel-tab-browser").click();
    const panel = page.getByTestId("browser-panel");
    await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 40_000 });

    // Navigate the panel to a real page and wait until it has painted: the bug
    // was invisible without this step.
    await page.getByTestId("browser-url").fill(REAL_PAGE);
    await page.getByTestId("browser-url").press("Enter");
    await expect(panel).toHaveAttribute("data-browser-painted", "1", { timeout: 30_000 });

    // Two captures in a row: the second one used to fail with 422 "浏览器正忙".
    const shots: { path: string; bytes: number }[] = [];
    for (let i = 0; i < 2; i += 1) {
      const shot = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/screenshot`);
      expect(shot.status, `capture ${i + 1}: ${JSON.stringify(shot.body)}`).toBe(201);
      shots.push(shot.body as { path: string; bytes: number });
    }

    const body = shots[0];
    expect(body.path.startsWith("screenshots/")).toBe(true);
    expect(body.path.endsWith(".png")).toBe(true);
    expect(body.bytes).toBeGreaterThan(1000);

    // Decode the PNG in a page context and count distinct RGB values. A white
    // image is exactly one colour; any real page has far more.
    const inline = await page.request.get(
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(body.path)}&inline=1`,
    );
    expect(inline.status()).toBe(200);
    const base64 = Buffer.from(await inline.body()).toString("base64");
    const image = await page.evaluate(async (encoded) => {
      const bytes = Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0));
      const bitmap = await createImageBitmap(new Blob([bytes], { type: "image/png" }));
      const canvas = document.createElement("canvas");
      canvas.width = bitmap.width;
      canvas.height = bitmap.height;
      const context = canvas.getContext("2d");
      if (!context) throw new Error("no 2d context");
      context.drawImage(bitmap, 0, 0);
      const pixels = context.getImageData(0, 0, bitmap.width, bitmap.height).data;
      const colours = new Set<number>();
      for (let i = 0; i < pixels.length; i += 4) {
        colours.add((pixels[i] << 16) | (pixels[i + 1] << 8) | pixels[i + 2]);
        if (colours.size > 64) break;
      }
      return { width: bitmap.width, height: bitmap.height, colours: colours.size };
    }, base64);
    expect(image.width).toBeGreaterThan(100);
    expect(image.height).toBeGreaterThan(100);
    expect(
      image.colours,
      `the screenshot is a flat image (${image.width}x${image.height}, ${image.colours} colour(s)); ` +
        "the endpoint captured a blank page instead of the panel's page",
    ).toBeGreaterThan(8);

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
    const preview = page.getByTestId("image-preview");
    await expect(preview).toBeVisible({ timeout: 20_000 });
    await expect(preview).toHaveAttribute("src", /inline=1/);

    // And a page that really has nothing on it is refused, not saved: the
    // request names the tab explicitly, so it cannot accidentally capture the
    // real page the panel was showing.
    const blank = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/tabs`, { url: "" });
    expect(blank.status).toBe(201);
    const blankTab = (blank.body as { tab: { id: string } }).tab;

    const refused = await apiWrite(
      page,
      "POST",
      `/v1/sessions/${sessionId}/browser/screenshot?tab=${encodeURIComponent(blankTab.id)}`,
    );
    expect(refused.status).toBe(422);
    const failure = refused.body as { error?: { code?: string; message?: string } };
    expect(failure.error?.code).toBe("sandbox_unavailable");
    expect(failure.error?.message ?? "").toContain("空白");
    expect(failure.error?.message ?? "").not.toContain("正忙");
    // No path was returned and nothing new appeared in the checkout.
    expect(refused.body).not.toHaveProperty("path");

    // A bare capture follows the page the panel shows — which is now the blank
    // tab, because opening it made it active. Same refusal, no white file.
    const bare = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/screenshot`);
    expect(bare.status).toBe(422);
    expect((bare.body as { error?: { message?: string } }).error?.message ?? "").toContain("空白");

    const listed = await page.request.get(`/v1/sessions/${sessionId}/files/tree?path=screenshots`);
    expect(listed.status()).toBe(200);
    const nodes = ((await listed.json()) as { nodes?: { name: string }[] }).nodes ?? [];
    expect(nodes.length, "a blank capture wrote a file into the workspace").toBe(
      new Set(shots.map((shot) => shot.path)).size,
    );
  });

  test("the panel's camera button captures what it shows", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    await page.getByTestId("panel-tab-browser").click();
    const panel = page.getByTestId("browser-panel");
    const status = await panel.getAttribute("data-browser-status");
    if (status === "disabled") {
      // The flag may be off for this account on a stripped-down host; the panel
      // then says so instead of offering a button that cannot work.
      await expect(page.getByTestId("browser-screenshot")).toHaveCount(0);
      return;
    }
    await expect(panel).toHaveAttribute("data-browser-status", "open", { timeout: 40_000 });
    await page.getByTestId("browser-url").fill(REAL_PAGE);
    await page.getByTestId("browser-url").press("Enter");
    await expect(panel).toHaveAttribute("data-browser-painted", "1", { timeout: 30_000 });

    const toast = page.locator("[data-sonner-toast]").filter({ hasText: "已保存到工作区" });
    await page.getByTestId("browser-screenshot").click();
    await expect(toast.first()).toBeVisible({ timeout: 30_000 });
    await expect(toast.first()).toContainText("screenshots/");
    void sessionId;
  });
});
