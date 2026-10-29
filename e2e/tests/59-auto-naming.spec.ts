import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, ensureProject, login, sendPrompt } from "./helpers";

/**
 * A user should not have to invent names before starting.
 *
 * The session dialog lost its title field (the server names a session from its
 * first message); the project dialog accepts an empty name and derives it from
 * the repository address; and a project that was still named wrongly can be
 * renamed from its own header, without the command palette.
 */

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/ux-fix2");
mkdirSync(ARTIFACTS, { recursive: true });

function monthDay(date: Date): string {
  return `${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

/**
 * Project names are unique per account, so a retry of a test that creates
 * "Spoon-Knife" would otherwise fail on the row the first attempt made.
 */
async function removeProjectsNamed(page: import("@playwright/test").Page, name: string) {
  const listed = (await (await page.request.get("/v1/projects")).json()) as {
    projects: { id: string; name: string }[];
  };
  for (const project of listed.projects.filter((item) => item.name === name)) {
    const gone = await apiWrite(page, "DELETE", `/v1/projects/${project.id}`);
    expect(gone.status).toBeLessThan(300);
  }
}

test("the session dialog asks for no title and the first message names the session", async ({
  page,
}) => {
  await login(page, USER);
  const projectId = await ensureProject(page, `ux59-auto-${Date.now()}`);
  await page.goto(`/p/${projectId}`);
  await expect(page.getByTestId("new-session-button").first()).toBeVisible({ timeout: 20_000 });

  await page.getByTestId("new-session-button").first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  // The field that used to force a decision before the first token is gone; the
  // choices that matter (scratch, model, thinking) are still there.
  await expect(page.getByTestId("session-title")).toHaveCount(0);
  await expect(page.getByTestId("session-ephemeral")).toBeVisible();
  await expect(page.getByTestId("session-submit")).toBeVisible();
  await page.screenshot({ path: path.join(ARTIFACTS, "59-new-session-dialog-no-title.png") });

  await page.getByTestId("session-submit").click();
  await expect(page).toHaveURL(/\/p\/prj_[^/]+\/s\/ses_/, { timeout: 30_000 });
  const sessionId = /\/s\/([^/]+)/.exec(page.url())?.[1] ?? "";
  expect(sessionId).not.toBe("");

  // The session is visible immediately, under the placeholder rather than a
  // blank row, and only gets its real title after the first message settles.
  const row = page.locator(`[data-testid="session-link"][href$="/s/${sessionId}"]`).first();
  await expect(row).toBeVisible({ timeout: 20_000 });
  await expect(row).toContainText("新会话");

  const message = "先读一遍 README，再用一句话说明这个仓库是做什么的";
  await sendPrompt(page, message);
  await expect(row).toContainText(message.slice(0, 12), { timeout: 30_000 });
  await expect(row).not.toContainText("新会话");

  const stored = (await (await page.request.get(`/v1/sessions/${sessionId}`)).json()) as {
    session: { title: string };
  };
  expect(stored.session.title.startsWith(message.slice(0, 12))).toBe(true);

  await page.screenshot({ path: path.join(ARTIFACTS, "59-session-auto-named.png") });
});

test("a project created without a name takes it from the repository", async ({ page }) => {
  await login(page, USER);
  await removeProjectsNamed(page, "Spoon-Knife");
  await removeProjectsNamed(page, `空白工作区 · ${monthDay(new Date())}`);
  await page.goto("/");
  await page.getByTestId("new-project-button").first().click();

  // The placeholder promises the outcome, and the derived value is shown before
  // the user commits to it.
  const nameInput = page.getByTestId("project-name");
  await expect(nameInput).toHaveAttribute("placeholder", "留空则用仓库名");
  await page.getByTestId("project-repo").fill("https://github.com/octocat/Spoon-Knife.git");
  await expect(page.getByTestId("project-name-derived")).toContainText("Spoon-Knife");
  await page.screenshot({ path: path.join(ARTIFACTS, "59-project-dialog-derived-name.png") });

  await page.getByTestId("project-submit").click();
  await expect(page).toHaveURL(/\/p\/prj_/, { timeout: 30_000 });
  const projectId = /\/p\/([^/]+)/.exec(page.url())?.[1] ?? "";
  const stored = (await (await page.request.get(`/v1/projects/${projectId}`)).json()) as {
    project: { name: string; repo_url: string };
  };
  expect(stored.project.name).toBe("Spoon-Knife");
  expect(stored.project.repo_url).toBe("https://github.com/octocat/Spoon-Knife.git");

  // A client-side route change briefly keeps the previous route's shell mounted
  // (Next's cache), so scope the assertion to the sidebar the user can see.
  const sidebar = page.locator('[data-testid="app-sidebar"]:visible');
  await expect(page.getByTestId("project-title")).toHaveText("Spoon-Knife");
  await expect(sidebar.getByTestId("current-project-name")).toHaveText("Spoon-Knife");
  await page.screenshot({ path: path.join(ARTIFACTS, "59-project-named-from-repo.png") });

  // An empty repository gets the dated blank-workspace name instead of an
  // invented one, and the same value is what the API stored.
  await page.goto("/");
  await page.getByTestId("new-project-button").first().click();
  await expect(page.getByTestId("project-name-derived")).toContainText(
    `空白工作区 · ${monthDay(new Date())}`,
  );
  await page.getByTestId("project-submit").click();
  await expect(page).toHaveURL(/\/p\/prj_/, { timeout: 30_000 });
  const blankId = /\/p\/([^/]+)/.exec(page.url())?.[1] ?? "";
  const blank = (await (await page.request.get(`/v1/projects/${blankId}`)).json()) as {
    project: { name: string; repo_url: string };
  };
  expect(blank.project.repo_url).toBe("");
  expect(blank.project.name).toBe(`空白工作区 · ${monthDay(new Date())}`);
  await expect(page.getByTestId("project-title")).toHaveText(`空白工作区 · ${monthDay(new Date())}`);
  await page.screenshot({ path: path.join(ARTIFACTS, "59-project-blank-workspace-name.png") });
});

test("a project can be renamed from its header and every surface follows", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const oldName = `ux59-rename-${stamp}`;
  const newName = `ux59-renamed-${stamp}`;
  const projectId = await ensureProject(page, oldName);
  await page.goto(`/p/${projectId}`);

  await expect(page.getByTestId("project-title")).toHaveText(oldName);
  await expect(page.getByTestId("current-project-name")).toHaveText(oldName);

  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-rename").click();
  await page.getByTestId("project-rename-input").fill(newName);
  await page.getByTestId("project-rename-submit").click();

  // Same page, no reload: header and sidebar are two separate queries and both
  // must show the new name.
  await expect(page.getByTestId("project-title")).toHaveText(newName, { timeout: 20_000 });
  await expect(page.getByTestId("current-project-name")).toHaveText(newName);
  const stored = (await (await page.request.get(`/v1/projects/${projectId}`)).json()) as {
    project: { name: string };
  };
  expect(stored.project.name).toBe(newName);
  await page.screenshot({ path: path.join(ARTIFACTS, "59-project-renamed.png") });

  // The home page is the third surface; reach it by the app's own navigation so
  // this also proves the rename survives without a full page load.
  await page.getByTestId("project-switcher").click();
  await page.getByTestId("project-switcher-all").click();
  await expect(page).toHaveURL(/\/$/);
  const card = page.locator('[data-testid="project-card"]', { hasText: newName }).first();
  await expect(card).toBeVisible({ timeout: 20_000 });
  await page.screenshot({ path: path.join(ARTIFACTS, "59-project-renamed-home.png") });
});
