import { expect, test } from "@playwright/test";

import { USER, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-14.7: a fresh deployment ships skills, otherwise the page says "write one" and there
 * is nothing to try. They are shared, so a normal user sees them without copying anything.
 */
test("a fresh deployment ships skills", async ({ page }) => {
  await login(page, USER);
  await page.goto("/settings/skills");
  await expect(page.getByTestId("skill-item").first()).toBeVisible({ timeout: 30_000 });

  const names = await page
    .getByTestId("skill-item")
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-name") ?? ""));
  expect(names).toEqual(expect.arrayContaining(["code-review", "test-gap"]));

  // And one of them reaches a session's checkout without the user doing anything.
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");
  const tooling = await (await page.request.get(`/v1/sessions/${sessionId}/tooling`)).json();
  const mounted = (tooling as { mounted: { skills: string[] } }).mounted.skills;
  expect(mounted).toEqual(expect.arrayContaining(["code-review"]));
});