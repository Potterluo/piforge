import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-2.6: the second half of "try a few approaches and pick one" — the picking. A
 * branch's work has to reach the session it came from, and a dirty target has to be
 * refused rather than quietly overwritten.
 */
test("a branch's work can be adopted back", async ({ page }) => {
  await login(page, USER);
  const sourceId = await openNewSession(page);
  await sendPrompt(page, "the parent of an adopted branch");

  const cloned = await apiWrite(page, "POST", `/v1/sessions/${sourceId}/clone`, {});
  expect(cloned.status).toBe(201);
  const branchId = (cloned.body as { session: { id: string } }).session.id;

  // Write a file in the branch's own checkout.
  const write = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${branchId}/files?path=adopted.txt`,
    "adopted from a branch\n",
  );
  expect(write.status).toBeLessThan(300);

  const adopted = await apiWrite(page, "POST", `/v1/sessions/${branchId}/adopt`, {});
  expect(adopted.status).toBe(200);
  const result = (adopted.body as { adopted: { commit: string; files: string[] } }).adopted;
  expect(result.commit).toMatch(/^[0-9a-f]{7,}$/);
  expect(result.files).toContain("adopted.txt");

  // The parent's checkout now has the file.
  const read = await page.request.get(`/v1/sessions/${sourceId}/files?path=adopted.txt`);
  expect(read.status()).toBe(200);
  const content = (await read.json()) as { body: string };
  expect(content.body).toContain("adopted from a branch");

  // Adopting a main session is a client error, not a crash.
  const refused = await apiWrite(page, "POST", `/v1/sessions/${sourceId}/adopt`, {});
  expect(refused.status).toBe(400);
});