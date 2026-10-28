import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * The preview pane is where a user actually reads code, so "this file type cannot
 * be shown" is only acceptable for things a browser cannot render. PDFs were that
 * case; every browser worth targeting renders them, so the fallback card was a
 * missing feature wearing a technical excuse.
 */
test.describe("file preview", () => {
  test("a PDF renders instead of offering a download card", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // A minimal but valid PDF, written through the same endpoint the editor uses.
    const pdf = [
      "%PDF-1.4",
      "1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj",
      "2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj",
      "3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj",
      "trailer<</Root 1 0 R>>",
      "%%EOF",
    ].join("\n");
    const written = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=notes%2Fdoc.pdf`,
      pdf,
    );
    expect(written.status).toBeLessThan(300);

    await page.getByTestId("panel-tab-files").click();
    await page.getByRole("button", { name: "刷新文件树" }).click();
    await page.getByTestId("quick-open-input").count(); // no-op: keeps the tab settled
    await page.keyboard.press("Control+p");
    await page.getByTestId("quick-open-input").fill("doc.pdf");
    await page.getByTestId("quick-open-result").first().click();

    const frame = page.getByTestId("pdf-preview").locator("iframe");
    await expect(frame).toBeVisible({ timeout: 20_000 });
    // The frame must point at the inline endpoint: an attachment disposition makes
    // Chrome download the file instead of rendering it.
    await expect(frame).toHaveAttribute("src", /inline=1/);
  });

  test("a download streams the whole file, not the preview window", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // Larger than the preview cap, so truncation would show up as a length mismatch.
    const body = "x".repeat(300_000);
    const written = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=big.txt`,
      body,
    );
    expect(written.status).toBeLessThan(300);

    const response = await page.request.get(
      `/v1/sessions/${sessionId}/files?path=big.txt&download=1`,
    );
    expect(response.status()).toBe(200);
    expect((await response.body()).length).toBe(body.length);
    expect(response.headers()["content-disposition"]).toContain("attachment");
  });
});
