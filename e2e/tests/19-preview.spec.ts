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

  test("a binary file shows a download card instead of an error", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // PNG bytes with a non-image name: a .png keeps its image preview (20-screenshot
    // asserts that), so the binary card's own state needs a file the panel cannot
    // render. Both modes classify it binary; the remote one used to answer 500.
    const png = Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
      "base64",
    );
    const written = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("icon.bin")}`,
      png,
    );
    expect(written.status).toBeLessThan(300);

    const preview = await (
      await page.request.get(
        `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("icon.bin")}`,
      )
    ).json();
    expect((preview as { binary: boolean }).binary).toBe(true);
    expect((preview as { body: string }).body).toBe("");

    await page.getByTestId("panel-tab-files").click();
    await page.getByRole("button", { name: "刷新文件树" }).click();
    await page.keyboard.press("Control+p");
    await page.getByTestId("quick-open-input").fill("icon.bin");
    await page.getByTestId("quick-open-result").first().click();

    const card = page.getByTestId("preview-binary");
    await expect(card).toBeVisible({ timeout: 20_000 });
    await expect(card).toContainText("二进制文件，不可预览");
    await expect(card).toContainText("70 B");
    await expect(page.getByTestId("preview-binary-download")).toHaveAttribute("href", /download=1/);
    await expect(page.getByTestId("preview-error")).toHaveCount(0);
  });

  test("a download streams the whole file, not the preview window", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // Above the 2 MiB inline cap, so the preview body is truncated while the
    // download must not be. (A 300 KB file fits inline, which would make this
    // assertion pass without proving anything.)
    const body = "x".repeat(3 * 1024 * 1024);
    const written = await apiWrite(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=big.txt`,
      body,
    );
    expect(written.status).toBeLessThan(300);

    const preview = await (await page.request.get(`/v1/sessions/${sessionId}/files?path=big.txt`)).json();
    expect((preview as { body: string }).body.length).toBeLessThan(body.length);

    const response = await page.request.get(
      `/v1/sessions/${sessionId}/files?path=big.txt&download=1`,
    );
    expect(response.status()).toBe(200);
    expect((await response.body()).length).toBe(body.length);
    expect(response.headers()["content-disposition"]).toContain("attachment");
  });
});
