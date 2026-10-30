import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-6.6: content search on ⌘⇧F, and a result that opens the file at the line it
 * matched.
 *
 * The backend has had `?type=content` for a while; what was missing was the only
 * thing a user touches — a way to run it from the browser. This spec drives the
 * real palette, with a file written through the real files API, and asserts the
 * line highlight the reader lands on.
 */
test("Ctrl+Shift+F searches contents and opens the matching line", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  const token = `piforge-content-search-${Date.now()}`;
  const body = ["# Guide", "", `this line holds ${token}`, "", "the end", ""].join("\n");
  const written = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=docs%2Fguide.md`,
    body,
  );
  expect(written.status).toBeLessThan(300);

  await page.keyboard.press("Control+Shift+F");

  // The palette says which search it is in; without this the two shortcuts are
  // indistinguishable until the results are wrong.
  await expect(page.getByTestId("quick-open-mode")).toHaveAttribute("data-mode", "content", {
    timeout: 20_000,
  });
  const input = page.getByTestId("quick-open-input");
  await expect(input).toBeVisible();
  await input.fill(token);

  const result = page.getByTestId("quick-open-result").first();
  await expect(result).toHaveAttribute("data-path", "docs/guide.md", { timeout: 20_000 });
  await expect(result).toHaveAttribute("data-line", "3");
  await expect(result).toContainText("docs/guide.md:3");
  await expect(result).toContainText(token);

  await result.click();

  // Landing on line 3 — not merely on the file — is the promise of a content hit.
  const preview = page.getByTestId("code-preview");
  await expect(preview).toHaveAttribute("data-active-line", "3", { timeout: 20_000 });
  await expect(preview).toContainText(token);
  await expect(page.getByTestId("file-preview")).toContainText("docs/guide.md");
  await expect(input).toHaveCount(0);
});

test("Ctrl+P is still a name search, not a content search", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  const token = `piforge-name-vs-content-${Date.now()}`;
  expect(
    (
      await apiWrite(
        page,
        "PUT",
        `/v1/sessions/${sessionId}/files?path=docs%2Fnotes.md`,
        `hidden ${token}\n`,
      )
    ).status,
  ).toBeLessThan(300);

  await page.keyboard.press("Control+p");
  await expect(page.getByTestId("quick-open-mode")).toHaveAttribute("data-mode", "name");
  await page.getByTestId("quick-open-input").fill(token);

  // The token is file *content*, never a filename, so the name palette must stay
  // empty. If ⌘P started searching contents, this result would appear.
  await expect(page.getByTestId("quick-open-result")).toHaveCount(0, { timeout: 20_000 });
});
