import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, ensureProject, login, openSession } from "./helpers";

/**
 * A project's lifecycle after creation: rename it from its own kebab, and delete
 * it — with a confirmation that says exactly what goes and what does not.
 *
 * Two rules are load-bearing here:
 *  - deleting a project is destructive but the remote repository is not part of
 *    it, so the dialog must say so rather than let the user guess;
 *  - a project with a turn in flight is refused, not silently killed.
 */

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/ux-fix3");
mkdirSync(ARTIFACTS, { recursive: true });
const QA16_ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-qa16");
mkdirSync(QA16_ARTIFACTS, { recursive: true });

/**
 * Renaming onto a name another project owns must show the server's exact sentence
 * under the field, the way create does. The regression was not cosmetic: the 409
 * was real and actionable, and the dialog replaced it with 「操作失败，请稍后重试」
 * — a message that tells nobody what to change (QA-16, FIX B).
 */
test("renaming to an existing name shows the server's conflict message", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const taken = `qa16-taken-${stamp}`;
  const mine = `qa16-rename-${stamp}`;
  const takenId = await ensureProject(page, taken);
  const mineId = await ensureProject(page, mine);

  await page.goto(`/p/${mineId}`);
  await expect(page.getByTestId("project-title")).toHaveText(mine);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-rename").click();

  const input = page.getByTestId("project-rename-input");
  await expect(input).toHaveValue(mine);
  await input.fill(taken);
  await page.getByTestId("project-rename-submit").click();

  const error = page.getByTestId("project-rename-error");
  await expect(error).toBeVisible({ timeout: 20_000 });
  await expect(error).toHaveText(
    `项目名称「${taken}」已存在（不区分大小写），请换一个名称`,
  );
  // The dialog stays open on the field the user has to fix, and the project did
  // not change.
  await expect(input).toBeVisible();
  await expect(input).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByTestId("project-title")).toHaveText(mine);

  // The generic failure that used to replace the server's sentence is gone.
  await expect(
    page.locator("[data-sonner-toast]").filter({ hasText: "操作失败，请稍后重试" }),
  ).toHaveCount(0);
  await page.screenshot({ path: path.join(QA16_ARTIFACTS, "B-rename-conflict-inline.png") });

  // Closing without saving leaves the name alone, and the API agrees.
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("project-title")).toHaveText(mine);
  const stored = (await (await page.request.get(`/v1/projects/${mineId}`)).json()) as {
    project: { name: string };
  };
  expect(stored.project.name).toBe(mine);

  for (const id of [mineId, takenId]) {
    const gone = await apiWrite(page, "DELETE", `/v1/projects/${id}`);
    expect(gone.status).toBeLessThan(300);
  }
});

test("a project can be renamed from its kebab", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const before = `ux61-rename-${stamp}`;
  const after = `ux61-renamed-${stamp}`;
  const projectId = await ensureProject(page, before);

  await page.goto(`/p/${projectId}`);
  await expect(page.getByTestId("project-title")).toHaveText(before);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-rename").click();
  await page.getByTestId("project-rename-input").fill(after);
  await page.getByTestId("project-rename-submit").click();

  await expect(page.getByTestId("project-title")).toHaveText(after, { timeout: 20_000 });
  const stored = (await (await page.request.get(`/v1/projects/${projectId}`)).json()) as {
    project: { name: string };
  };
  expect(stored.project.name).toBe(after);

  const gone = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
  expect(gone.status).toBeLessThan(300);
});

test("deleting a project confirms what goes, returns home and removes it", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const name = `ux61-delete-${stamp}`;
  const projectId = await ensureProject(page, name);
  await openSession(page, projectId, "session that should disappear");

  await page.goto(`/p/${projectId}`);
  await expect(page.getByTestId("project-title")).toHaveText(name);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-delete").click();

  const dialog = page.getByTestId("project-delete-dialog");
  await expect(dialog).toBeVisible();
  // What will be deleted: the sessions (count) and the sandbox.
  await expect(dialog).toContainText("1 个会话");
  await expect(dialog).toContainText("沙箱");
  // What will not: the remote repository.
  await expect(dialog).toContainText("远程仓库不会被删除");
  await page.screenshot({ path: path.join(ARTIFACTS, "61-delete-confirm.png") });

  await page.getByTestId("project-delete-confirm").click();
  await expect(page).toHaveURL(/\/$/, { timeout: 20_000 });
  await expect(
    page.locator("[data-testid=project-card]", { hasText: name }),
  ).toHaveCount(0);
  expect((await page.request.get(`/v1/projects/${projectId}`)).status()).toBe(404);
  await page.screenshot({ path: path.join(ARTIFACTS, "61-deleted-home.png") });
});

test("a project with a running turn refuses to be deleted", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const name = `ux61-running-${stamp}`;
  const projectId = await ensureProject(page, name);
  const sessionId = await openSession(page, projectId, "busy session");

  // Fake the running state through the API rather than racing a real turn: the
  // fake peer can settle between the status poll and the click, which would
  // test its speed instead of the guard. `status` is the same field the hub
  // writes on run_started, so the guard is exercised exactly as in production.
  const patched = await apiWrite(page, "PATCH", `/v1/sessions/${sessionId}`, {
    status: "running",
  });
  expect(patched.status).toBeLessThan(300);

  await page.goto(`/p/${projectId}`);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-delete").click();
  await page.getByTestId("project-delete-confirm").click();

  const toast = page.locator("[data-sonner-toast]").filter({ hasText: "删除失败" });
  await expect(toast).toBeVisible({ timeout: 20_000 });
  await expect(toast).toContainText("正在运行");
  // Still on the project: a refusal must not navigate away or half-delete.
  await expect(page).toHaveURL(new RegExp(`/p/${projectId}`));
  expect((await page.request.get(`/v1/projects/${projectId}`)).status()).toBe(200);
  await page.screenshot({ path: path.join(ARTIFACTS, "61-delete-refused-running.png") });

  // The API is the gate, not only the dialog: a direct DELETE is refused too.
  const refused = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
  expect(refused.status).toBe(409);
  expect((refused.body as { error: { message: string } }).error.message).toContain("正在运行");

  // Let the fake turn end, then clean up so the suite does not accumulate.
  const idle = await apiWrite(page, "PATCH", `/v1/sessions/${sessionId}`, { status: "idle" });
  expect(idle.status).toBeLessThan(300);
  const gone = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
  expect(gone.status).toBeLessThan(300);
});
