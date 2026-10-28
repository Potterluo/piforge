import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-7.1 / 7.10: a one-line fix should not require leaving the page. The editor is a
 * plain textarea, and what matters is the round trip: open, change, save, and the
 * bytes on disk are what the tree and the agent will see.
 */
test("a file can be edited in the panel and saved", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  const written = await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=notes.txt`, "first line\n");
  expect(written.status).toBeLessThan(300);

  await page.goto(`/p/${/\/p\/([^/]+)\//.exec(page.url())?.[1] ?? ""}/s/${sessionId}`);
  await expect(page.getByTestId("file-tree")).toBeVisible({ timeout: 40_000 });

  await page.getByTestId("file-tree").getByText("notes.txt", { exact: false }).first().click();
  await expect(page.getByTestId("file-preview-body")).toBeVisible({ timeout: 20_000 });

  await page.getByTestId("preview-edit").click();
  const editor = page.getByTestId("file-editor-input");
  await expect(editor).toBeVisible();
  await editor.fill("first line\nsecond line\n");
  await page.getByTestId("file-editor-save").click();

  // Back to the rendered view, with the new bytes — read from the server, not the draft.
  await expect(page.getByTestId("file-preview-body")).toBeVisible({ timeout: 20_000 });
  const read = await (await page.request.get(`/v1/sessions/${sessionId}/files?path=notes.txt`)).json();
  expect((read as { body: string }).body).toContain("second line");

  // Cancelling leaves the file alone.
  await page.getByTestId("preview-edit").click();
  await page.getByTestId("file-editor-input").fill("this must not be saved");
  await page.getByTestId("file-editor-cancel").click();
  const after = await (await page.request.get(`/v1/sessions/${sessionId}/files?path=notes.txt`)).json();
  expect((after as { body: string }).body).not.toContain("must not be saved");
});