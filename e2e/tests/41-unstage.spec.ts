import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * QA found 取消暂存 doing nothing in the 变更 panel, and a file that is partly staged
 * showing up in both lists with no explanation. The second is correct behaviour that
 * needs a label; the first is the button.
 */
test("a staged file can be taken back out of the index from the panel", async ({ page }) => {
  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `unstage-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = (project.body as { project: { id: string } }).project.id;
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "unstage" });
  const sessionId = (created.body as { session: { id: string } }).session.id;
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `u-${Date.now()}`,
    text: "hello",
  });

  await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=calc.py`, "print(1)\n");
  const staged = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/stage`, {
    paths: ["calc.py"],
  });
  expect(staged.status).toBe(200);

  await page.goto(`/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("panel-tab-changes").click();

  const row = page.getByTestId("git-change").filter({ hasText: "calc.py" }).first();
  await expect(row).toBeVisible({ timeout: 30_000 });

  // The unstaging control on the staged row.
  await row.getByRole("button", { name: "取消暂存" }).click();

  // The API is the source of truth: the file must really be out of the index.
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`/v1/sessions/${sessionId}/git/diff?scope=staged`);
        const body = (await res.json()) as { diffs: unknown[] };
        return body.diffs.length;
      },
      { timeout: 30_000 },
    )
    .toBe(0);
});