import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openSession } from "./helpers";

/**
 * QA-13 finding C: a project accepted a nonsense repository address.
 *
 * `not-a-repo-url` created a project, then every session in it had no worktree
 * and every panel answered 422. This spec covers both halves of the fix: the
 * address is refused before a project exists, and a session that already has no
 * worktree offers recovery actions instead of a retry loop.
 */
test("a nonsense repository URL is rejected by the form and the API", async ({ page }) => {
  await login(page, USER);
  const name = `bad-repo-${Date.now()}`;

  await page.goto("/");
  await page.getByRole("button", { name: "新建项目" }).first().click();
  await page.getByTestId("project-name").fill(name);
  await page.getByTestId("project-repo").fill("not-a-repo-url");
  await page.getByTestId("project-submit").click();

  const fieldError = page.getByTestId("project-repo-error");
  await expect(fieldError).toBeVisible({ timeout: 10_000 });
  await expect(fieldError).toContainText("仓库地址格式不支持");
  // The dialog stays open; nothing was created behind the error.
  await expect(page.getByTestId("project-name")).toHaveValue(name);

  const listed = await page.request.get("/v1/projects");
  const projects = (await listed.json()) as { projects: { name: string }[] };
  expect(projects.projects.some((project) => project.name === name)).toBe(false);

  // The API refuses it too, so a script cannot bypass the form.
  const direct = await apiWrite(page, "POST", "/v1/projects", {
    name: `${name}-api`,
    repo_url: "not-a-repo-url",
    default_branch: "main",
  });
  expect(direct.status).toBe(400);
  const body = direct.body as { error: { code: string; message: string } };
  expect(body.error.code).toBe("invalid_argument");
  expect(body.error.message).toContain("repo_url");

  const after = await page.request.get("/v1/projects");
  const afterBody = (await after.json()) as { projects: { name: string }[] };
  expect(afterBody.projects.some((project) => project.name === `${name}-api`)).toBe(false);

  await page.getByRole("button", { name: "取消" }).click();
});

test("a session with no worktree explains itself and offers fix-or-delete", async ({ page }) => {
  await login(page, USER);
  const name = `no-worktree-${Date.now()}`;

  // A syntactically valid address that cannot be cloned: the project is allowed,
  // but provisioning fails, which is the legacy row this recovery state is for.
  const created = await apiWrite(page, "POST", "/v1/projects", {
    name,
    repo_url: "https://127.0.0.1:1/acme/nope.git",
    default_branch: "main",
  });
  expect(created.status).toBeLessThan(300);
  const projectId = (created.body as { project: { id: string } }).project.id;
  const sessionId = await openSession(page, projectId, "no worktree session");

  const state = page.getByTestId("no-worktree-state");
  await expect(state).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("no-worktree-title")).toContainText("还没有工作区");
  // No 422 panel and no retry loop: the workspace panels are not mounted at all.
  await expect(page.getByTestId("workspace-panels")).toHaveCount(0);
  await expect(page.getByText("this session has no worktree yet")).toHaveCount(0);
  await expect(state.getByRole("button", { name: "重试" })).toHaveCount(0);
  await expect(page.getByTestId("no-worktree-fix")).toBeVisible();
  await expect(page.getByTestId("no-worktree-delete")).toBeVisible();
  await expect(page.getByTestId("no-worktree-repo")).toContainText("127.0.0.1:1");

  // Fix the address; the saved value is what the API now reports.
  await page.getByTestId("no-worktree-fix").click();
  await page.getByTestId("no-worktree-repo-input").fill("https://example.com/acme/fixed.git");
  await page.getByTestId("no-worktree-save").click();
  await expect(page.locator("[data-sonner-toast]").filter({ hasText: "仓库地址已保存" })).toBeVisible({
    timeout: 20_000,
  });
  const reloaded = await page.request.get(`/v1/projects/${projectId}`);
  expect(((await reloaded.json()) as { project: { repo_url: string } }).project.repo_url).toBe(
    "https://example.com/acme/fixed.git",
  );

  // Delete the broken project and land back on the project list.
  await page.getByTestId("no-worktree-delete").click();
  await page.getByTestId("no-worktree-delete-confirm").click();
  await expect(page).toHaveURL(/\/$/, { timeout: 20_000 });
  const gone = await page.request.get(`/v1/projects/${projectId}`);
  expect(gone.status()).toBe(404);

  // The session went with it: the whole point of deleting the broken row.
  const sessionGone = await page.request.get(`/v1/sessions/${sessionId}`);
  expect(sessionGone.status()).toBe(404);
});
