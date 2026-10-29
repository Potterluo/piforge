import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * QA-13 finding 2: a deleted or mistyped session id rendered a fully live
 * shell — 未命名会话, an enabled composer pointed at nothing, a tree 404ing and
 * an event stream reconnecting forever, with no way back.
 *
 * The id in this test has never existed, which is the same thing the server
 * sees for a session that was just deleted.
 */
test("an unknown session renders a not-found page, not a live workspace", async ({ page }) => {
  await login(page, USER);

  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `notfound-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  expect(project.status).toBeLessThan(300);
  const projectId = (project.body as { project: { id: string } }).project.id;

  const missing = `missing-${Date.now()}`;
  const requests: string[] = [];
  const onRequest = (request: { url: () => string }) => {
    const url = request.url();
    if (url.includes(`/v1/sessions/${missing}`)) requests.push(url);
  };
  page.on("request", onRequest);

  await page.goto(`/p/${projectId}/s/${missing}`);

  const notFound = page.getByTestId("session-not-found");
  await expect(notFound).toBeVisible({ timeout: 20_000 });
  await expect(notFound).toContainText("会话不存在");

  // Nothing that pretends to be a session may be mounted.
  await expect(page.getByTestId("composer-input")).toHaveCount(0);
  await expect(page.getByTestId("workspace-panels")).toHaveCount(0);

  // Give the old reconnect loop time to prove it is gone: a 404 must not be
  // retried, and the event stream must never be opened at all.
  await page.waitForTimeout(4_000);
  expect(
    requests.filter((url) => url.includes("/events")),
    `event stream was opened for a missing session: ${requests.join(", ")}`,
  ).toHaveLength(0);

  // There is a way out, and it works.
  const back = page.getByTestId("session-not-found-project");
  await expect(back).toHaveAttribute("href", `/p/${projectId}`);
  await back.click();
  await page.waitForURL(`**/p/${projectId}`, { timeout: 20_000 });

  page.off("request", onRequest);
});
