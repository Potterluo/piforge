import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * FR-11.2 / FR-11.4: watching is the default, taking over is explicit, and the server —
 * not the panel — decides whether input is allowed.
 */
test("the browser panel starts in observe mode and takeover is explicit", async ({ page }) => {
  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `mode-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = (project.body as { project: { id: string } }).project.id;
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "mode" });
  const sessionId = (created.body as { session: { id: string } }).session.id;
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `m-${Date.now()}`,
    text: "hello",
  });

  const initial = await (await page.request.get(`/v1/sessions/${sessionId}/browser/mode`)).json();
  expect((initial as { mode: string }).mode).toBe("observe");

  const taken = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/mode`, {
    mode: "takeover",
  });
  expect(taken.status).toBe(200);
  expect((taken.body as { mode: string }).mode).toBe("takeover");

  const back = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/browser/mode`, {
    mode: "observe",
  });
  expect((back.body as { mode: string }).mode).toBe("observe");

  // The panel shows the switch, and it says which mode you are in.
  await page.goto(`/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("panel-tab-browser").click();
  await expect(page.getByTestId("browser-mode")).toHaveText("观察", { timeout: 30_000 });
  await page.getByTestId("browser-mode").click();
  // Taking over interrupts the agent, so it asks first.
  await expect(page.getByText("接管浏览器？")).toBeVisible({ timeout: 10_000 });
  await page.getByRole("button", { name: "接管" }).click();
  await expect(page.getByTestId("browser-mode")).toHaveText("控制中", { timeout: 20_000 });
});