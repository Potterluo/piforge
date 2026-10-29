import { expect, test } from "@playwright/test";

import { ADMIN, apiWrite, ensureProject, login } from "./helpers";

/**
 * The browser panel is lazy (QA-16 / TASK 1).
 *
 * The panel used to be kept mounted like the file tree, so *every* session page
 * started a sandbox Chrome: nobody asked for it, and it never stopped. QA-16
 * counted 465 leaked Chrome processes. This spec pins the two halves of the
 * fix, against a real control plane:
 *
 *   1. opening a session starts no browser for its sandbox;
 *   2. selecting the browser panel starts exactly one;
 *   3. switching away and leaving it idle past PIFORGE_BROWSER_IDLE_TIMEOUT
 *      (3s in this harness) stops it again.
 *
 * "Is there a browser" is checked through the admin status endpoint, which now
 * reports `browser_sandboxes` — the sandbox API, not a host `ps`: the suite
 * runs the control plane as a real process, and the deployed app runs it in a
 * container where `docker exec ps` would be the other honest way to look.
 */
test("a session page starts no sandbox browser until the panel is selected, and the idle policy stops it", async ({
  page,
}) => {
  await login(page, ADMIN);
  const stamp = Date.now();
  const projectId = await ensureProject(page, `lazy-browser-${stamp}`);
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "lazy browser",
  });
  expect(created.status).toBeLessThan(300);
  const sessionId = (created.body as { session: { id: string } }).session.id;

  const status = async (): Promise<{ browsers_running: number; browser_sandboxes: string[] }> => {
    const res = await page.request.get("/v1/admin/status");
    expect(res.status()).toBe(200);
    return (await res.json()) as { browsers_running: number; browser_sandboxes: string[] };
  };
  const runningFor = async (sandboxId: string) => (await status()).browser_sandboxes.includes(sandboxId);

  // The page loads with the workspace mounted and the browser panel not chosen.
  await page.goto(`/p/${projectId}/s/${sessionId}`);
  await expect(page.locator(`[data-session-id="${sessionId}"]`)).toBeVisible({ timeout: 40_000 });

  const sandboxRes = await page.request.get(`/v1/sessions/${sessionId}/sandbox`);
  expect(sandboxRes.status()).toBe(200);
  const sandboxId = ((await sandboxRes.json()) as { id: string }).id;

  // No browser exists for this sandbox. The old build mounted the panel here
  // and started Chromium before this assertion could run; on that build the
  // check below reports a running browser, which is the leak in one line.
  // The short settle gives a leaked launch its startup window; a correct build
  // never starts one, so there is nothing to become true.
  await page.waitForTimeout(4_000);
  expect(
    await runningFor(sandboxId),
    "a session page with the panel unselected must not start a sandbox browser",
  ).toBe(false);

  // And the panel itself is not mounted: no socket was opened for a panel the
  // user never selected.
  await expect(page.getByTestId("browser-panel")).toHaveCount(0);

  // Selecting the panel is the first thing that may start it.
  await page.getByTestId("panel-tab-browser").click();
  await expect(page.getByTestId("browser-panel")).toBeVisible();
  await expect(page.getByTestId("browser-panel")).toHaveAttribute("data-browser-status", "open", {
    timeout: 40_000,
  });
  await expect
    .poll(async () => runningFor(sandboxId), { timeout: 30_000, intervals: [500] })
    .toBe(true);
  const withPanel = await status();
  expect(withPanel.browser_sandboxes.filter((id) => id === sandboxId)).toHaveLength(1);

  // Switching away is what makes the idle policy possible: the socket closes,
  // so no client is attached. The threshold is 3s in this harness.
  await page.getByTestId("panel-tab-files").click();
  await expect(page.getByTestId("file-tree")).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId("browser-panel")).toHaveCount(0);

  await expect
    .poll(async () => runningFor(sandboxId), { timeout: 25_000, intervals: [500] })
    .toBe(false);
  expect((await status()).browser_sandboxes).not.toContain(sandboxId);

  // The terminal and the file tree work with no browser running: they never
  // depended on one, and the lazy mount must not have introduced a dependency.
  await page.getByTestId("panel-tab-terminal").click();
  await expect(page.getByTestId("terminal-pane")).toBeVisible({ timeout: 20_000 });
  await page.getByTestId("panel-tab-files").click();
  await expect(page.getByTestId("file-tree")).toBeVisible();
});
