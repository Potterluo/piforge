import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login } from "./helpers";

/**
 * Session search, across projects.
 *
 * A session lives in a project, but the thing a person remembers is the
 * conversation. The sidebar's search box and the command palette both go through
 * `GET /v1/sessions?q=` (the server already filters title/label) and both show
 * the project name, because two sessions in different projects may share a title.
 */

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/ux-fix2");
mkdirSync(ARTIFACTS, { recursive: true });

async function createProject(page: import("@playwright/test").Page, name: string): Promise<string> {
  const created = await apiWrite(page, "POST", "/v1/projects", {
    name,
    repo_url: "",
    default_branch: "main",
  });
  expect(created.status).toBeLessThan(300);
  return (created.body as { project: { id: string } }).project.id;
}

async function createSession(
  page: import("@playwright/test").Page,
  projectId: string,
  title: string,
): Promise<string> {
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, { title });
  expect(created.status).toBeLessThan(300);
  return (created.body as { session: { id: string } }).session.id;
}

test("the sidebar searches sessions across projects and Enter opens one", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, USER);

  const stamp = Date.now();
  const token = `Qz9-${stamp}`;
  const projectA = await createProject(page, `ux60-alpha-${stamp}`);
  const projectB = await createProject(page, `ux60-beta-${stamp}`);
  const sessionIds: string[] = [];
  for (let index = 1; index <= 20; index += 1) {
    sessionIds.push(
      await createSession(page, projectA, `${token}-alpha-${String(index).padStart(2, "0")}`),
    );
  }
  for (let index = 1; index <= 20; index += 1) {
    sessionIds.push(
      await createSession(page, projectB, `${token}-beta-${String(index).padStart(2, "0")}`),
    );
  }

  await page.goto(`/p/${projectA}`);
  const search = page.getByTestId("session-search-input");
  await expect(search).toBeVisible({ timeout: 20_000 });

  // Three characters are enough: the server matches on the title, the result
  // list carries the project name and the running state next to every row.
  await search.fill(token.slice(0, 3));
  const results = page.getByTestId("session-search-result");
  await expect(results.first()).toBeVisible({ timeout: 20_000 });
  expect(await results.count()).toBeGreaterThanOrEqual(2);
  await expect(page.getByTestId("session-search-summary")).toContainText("跨项目");

  const projectNames = new Set<string>();
  const resultCount = await results.count();
  for (let index = 0; index < resultCount; index += 1) {
    projectNames.add((await results.nth(index).getByTestId("session-search-project").innerText()).trim());
    await expect(results.nth(index).getByTestId("session-search-status")).not.toBeEmpty();
  }
  // Both projects are represented, by name, not by an id or a placeholder.
  expect(projectNames.has(`ux60-alpha-${stamp}`)).toBe(true);
  expect(projectNames.has(`ux60-beta-${stamp}`)).toBe(true);
  await page.screenshot({ path: path.join(ARTIFACTS, "60-sidebar-session-search.png") });

  // Keyboard: ArrowDown moves the highlight, Enter opens the highlighted row.
  await search.fill(token);
  await expect
    .poll(async () => results.count(), { timeout: 20_000 })
    .toBeGreaterThanOrEqual(2);
  await search.press("ArrowDown");
  const target = await results.nth(1).getAttribute("data-session-id");
  expect(sessionIds).toContain(target);
  await search.press("Enter");
  await expect(page).toHaveURL(new RegExp(`/s/${target}$`), { timeout: 20_000 });

  // A miss says so instead of leaving an empty box.
  const searchAgain = page.getByTestId("session-search-input");
  await searchAgain.fill("zzzz-not-a-session");
  await expect(page.getByTestId("session-search-empty")).toHaveText("没有匹配的会话", {
    timeout: 20_000,
  });
  await page.screenshot({ path: path.join(ARTIFACTS, "60-session-search-empty.png") });
});

test("the command palette returns sessions, not just commands and projects", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, USER);

  const stamp = Date.now();
  const token = `Pk4-${stamp}`;
  const projectId = await createProject(page, `ux60-palette-${stamp}`);
  const sessionId = await createSession(page, projectId, `${token} 调一下缓存淘汰`);

  // The project was created through the API, so the page's cached project list
  // does not know it yet; a reload is what an operator would do next.
  await page.goto("/");
  await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 20_000 });

  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible({ timeout: 10_000 });
  const input = dialog.getByPlaceholder("搜索命令、项目、会话…");
  await input.fill(token);

  const found = page.getByTestId("palette-session");
  await expect(found.first()).toBeVisible({ timeout: 20_000 });
  await expect(found.first()).toContainText("调一下缓存淘汰");
  await expect(found.first()).toContainText(`ux60-palette-${stamp}`);
  await page.screenshot({ path: path.join(ARTIFACTS, "60-palette-session-search.png") });

  // No ArrowDown: Ctrl+K → type → Enter is the whole user path, and the palette
  // has to have a row selected by the time the list settles. This assertion is
  // also the async one — the session row first renders before the projects query
  // has answered, so its label (and, in the old cmdk implementation, its
  // selection key) changes while it is already on screen.
  await input.press("Enter");
  await expect(page).toHaveURL(new RegExp(`/p/${projectId}/s/${sessionId}$`), {
    timeout: 20_000,
  });
});

test("Enter while the result list is still loading is ignored, then works", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, USER);

  const stamp = Date.now();
  const token = `Pld-${stamp}`;
  const projectId = await createProject(page, `ux60-loading-${stamp}`);
  const sessionId = await createSession(page, projectId, `${token} 加载中按回车`);

  // Hold the search response so "the list is still loading when Enter is
  // pressed" is a real state and not a race with a fast local server. The
  // empty-query request (the recent list) is not delayed.
  let searchStarted = false;
  await page.route("**/v1/sessions*", async (route) => {
    const url = route.request().url();
    if (!url.includes(token)) {
      await route.continue();
      return;
    }
    searchStarted = true;
    await new Promise((resolve) => setTimeout(resolve, 1_500));
    await route.continue();
  });

  await page.goto("/");
  await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 20_000 });

  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible({ timeout: 10_000 });
  const input = dialog.getByPlaceholder("搜索命令、项目、会话…");
  await input.fill(token);

  // The request is in flight and no row exists yet: Enter has no target.
  await expect.poll(() => searchStarted, { timeout: 10_000 }).toBe(true);
  await expect(page.getByTestId("palette-loading")).toBeVisible();
  await input.press("Enter");

  // Ignored, not queued: the palette stays open, nothing was opened, and the
  // query is untouched. A queued Enter would run a row the user never saw.
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId("palette-session")).toHaveCount(0);
  expect(page.url()).toMatch(/\/$/);

  // Once the row exists the same Enter opens it.
  const found = page.getByTestId("palette-session");
  await expect(found.first()).toBeVisible({ timeout: 20_000 });
  await expect(found.first()).toContainText(`ux60-loading-${stamp}`);
  await input.press("Enter");
  await expect(page).toHaveURL(new RegExp(`/p/${projectId}/s/${sessionId}$`), {
    timeout: 20_000,
  });
});
