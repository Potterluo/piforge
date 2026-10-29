// Live verification for the first three UX fixes against the deployed app.
//
// Run with the container already rebuilt and restarted:
//   node e2e/scripts/ux-fix2-live.mjs
//
// It drives the real UI (only project/session setup that is not the subject of a
// screenshot goes through the API) and keeps model usage to one short turn.
import { chromium } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const BASE = process.env.PIFORGE_BASE ?? "http://127.0.0.1:7722";
const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/ux-fix2");
mkdirSync(ARTIFACTS, { recursive: true });

const log = (...args) => console.log("[ux-fix2-live]", ...args);

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
page.on("console", (message) => {
  if (message.type() === "error") errors.push(message.text());
});

async function csrf() {
  const cookies = await page.context().cookies();
  return cookies.find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
}

async function api(method, pathname, data) {
  const headers = { "X-PF-CSRF": await csrf() };
  if (data !== undefined) headers["Content-Type"] = "application/json";
  const response = await page.request.fetch(`${BASE}${pathname}`, {
    method,
    headers,
    data: data === undefined ? undefined : JSON.stringify(data),
  });
  const body = await response.json().catch(() => null);
  return { status: response.status(), body };
}

async function projects() {
  return (await (await page.request.get(`${BASE}/v1/projects`)).json()).projects;
}

async function removeEmptyProjectNamed(name) {
  for (const project of (await projects()).filter((item) => item.name === name)) {
    if (project.session_count > 0) {
      log(`keeping ${name}: it has ${project.session_count} sessions`);
      continue;
    }
    const gone = await api("DELETE", `/v1/projects/${project.id}`);
    log(`deleted stale empty project ${name}: ${gone.status}`);
  }
}

async function removeProjectsMatching(prefix) {
  for (const project of (await projects()).filter((item) => item.name.startsWith(prefix))) {
    const gone = await api("DELETE", `/v1/projects/${project.id}`);
    log(`deleted previous verification project ${project.name}: ${gone.status}`);
  }
}

try {
  // ---------------------------------------------------------------- sign in
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill("dev@piforge.local");
  await page.getByTestId("login-password").fill("piforge-dev-123");
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
  log("signed in");

  const stamp = Date.now();
  const now = new Date();
  const monthDay = `${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
  const visibleSidebar = page.locator('[data-testid="app-sidebar"]:visible');

  // ------------------------------------------------- item 1b: derived names
  await removeEmptyProjectNamed("Spoon-Knife");
  await removeEmptyProjectNamed(`空白工作区 · ${monthDay}`);
  await removeProjectsMatching("空白工作区 · 改名 ");

  await page.goto(`${BASE}/`);
  await page.getByTestId("new-project-button").first().click();
  const dialog = page.getByRole("dialog");
  await dialog.waitFor();
  const nameField = page.getByTestId("project-name");
  if ((await nameField.count()) === 0) throw new Error("project name field missing");
  log("project name placeholder:", await nameField.getAttribute("placeholder"));
  await page.getByTestId("project-repo").fill("https://github.com/octocat/Spoon-Knife.git");
  await page.getByTestId("project-name-derived").waitFor();
  log("derived preview:", (await page.getByTestId("project-name-derived").innerText()).trim());
  await page.screenshot({ path: path.join(ARTIFACTS, "live-01-project-derived-name.png") });
  await page.getByTestId("project-submit").click();
  await page.waitForURL(/\/p\/prj_/, { timeout: 40_000 });
  const spoonId = /\/p\/([^/]+)/.exec(page.url())[1];
  await page.getByTestId("project-title").waitFor();
  await page.waitForFunction(
    () => (document.querySelector('[data-testid="project-title"]')?.textContent ?? "").trim() !== "",
    undefined,
    { timeout: 20_000 },
  );
  log("Spoon-Knife project:", await page.getByTestId("project-title").innerText(), spoonId);
  await page.screenshot({ path: path.join(ARTIFACTS, "live-02-project-named-spoon-knife.png") });

  // Blank workspace name.
  await page.goto(`${BASE}/`);
  await page.getByTestId("new-project-button").first().click();
  await page.getByRole("dialog").waitFor();
  const blankPreview = (await page.getByTestId("project-name-derived").innerText()).trim();
  log("blank preview:", blankPreview);
  await page.getByTestId("project-submit").click();
  await page.waitForURL(/\/p\/prj_/, { timeout: 40_000 });
  const blankId = /\/p\/([^/]+)/.exec(page.url())[1];
  log("blank project:", await page.getByTestId("project-title").innerText(), blankId);
  await page.screenshot({ path: path.join(ARTIFACTS, "live-03-project-blank-workspace.png") });

  // --------------------------------------------- item 1c: rename from header
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-rename").click();
  const renamed = `空白工作区 · 改名 ${stamp}`;
  await page.getByTestId("project-rename-input").fill(renamed);
  await page.getByTestId("project-rename-submit").click();
  await page.getByTestId("project-title").waitFor();
  await page.waitForFunction(
    (value) => document.querySelector('[data-testid="project-title"]')?.textContent === value,
    renamed,
    { timeout: 20_000 },
  );
  log("renamed header:", await page.getByTestId("project-title").innerText());
  log(
    "renamed sidebar:",
    (await visibleSidebar.getByTestId("current-project-name").innerText()).trim(),
  );
  await page.screenshot({ path: path.join(ARTIFACTS, "live-04-project-renamed.png") });
  await visibleSidebar.getByTestId("project-switcher").click();
  await page.getByTestId("project-switcher-all").click();
  await page.waitForURL(`${BASE}/`);
  const homeCard = page.locator('[data-testid="project-card"]', { hasText: renamed }).first();
  await homeCard.waitFor({ timeout: 20_000 });
  log("renamed home card:", (await homeCard.innerText()).split("\n")[0]);
  await page.screenshot({ path: path.join(ARTIFACTS, "live-05-project-renamed-home.png") });

  // ----------------------------- item 1a: no title, named by first message
  await page.goto(`${BASE}/p/${blankId}`);
  await page.getByTestId("new-session-button").first().click();
  await page.getByRole("dialog").waitFor();
  const titleFieldCount = await page.getByTestId("session-title").count();
  log("title field in new-session dialog:", titleFieldCount);
  if (titleFieldCount !== 0) throw new Error("the new-session dialog still asks for a title");
  await page.screenshot({ path: path.join(ARTIFACTS, "live-06-new-session-no-title.png") });
  await page.getByTestId("session-submit").click();
  await page.waitForURL(/\/s\/ses_/, { timeout: 60_000 });
  const sessionId = /\/s\/([^/]+)/.exec(page.url())[1];
  const row = page.locator(`[data-testid="session-link"][href$="/s/${sessionId}"]`).first();
  await row.waitFor({ timeout: 30_000 });
  log("sidebar placeholder:", (await row.innerText()).trim());
  await page.screenshot({ path: path.join(ARTIFACTS, "live-07-session-placeholder.png") });

  // One real model turn. The answer is short so the auto-title is stable.
  const prompt = "只回复两个字：收到";
  await page.getByTestId("composer-input").waitFor({ timeout: 60_000 });
  await page.getByTestId("composer-input").fill(prompt);
  await page.getByTestId("composer-send").click();
  await page.getByTestId("turn-user").waitFor({ timeout: 60_000 });
  // The state indicator is a hidden live-region span, not a visible row.
  await page.getByTestId("run-state").waitFor({ state: "attached", timeout: 120_000 });
  await page.waitForFunction(
    () => document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") === "settled",
    undefined,
    { timeout: 180_000 },
  );
  await row.waitFor({ timeout: 30_000 });
  await page.waitForFunction(
    (id) => {
      const link = document.querySelector(`[data-testid="session-link"][href$="/s/${id}"]`);
      return link && !link.textContent?.includes("新会话");
    },
    sessionId,
    { timeout: 30_000 },
  );
  log("auto-named sidebar:", (await row.innerText()).trim());
  const detail = await (await page.request.get(`${BASE}/v1/sessions/${sessionId}`)).json();
  log("stored title:", detail.session.title);
  await page.screenshot({ path: path.join(ARTIFACTS, "live-08-session-auto-named.png") });

  // ------------------------------------------- item 2: cross-project search
  const allSessions = await (await page.request.get(`${BASE}/v1/sessions`)).json();
  log("sessions visible to dev:", allSessions.sessions.length);
  const searchTerm = "fix";
  await page.getByTestId("session-search-input").fill(searchTerm);
  const results = page.getByTestId("session-search-result");
  await results.first().waitFor({ timeout: 30_000 });
  const count = await results.count();
  const names = new Set();
  for (let index = 0; index < count; index += 1) {
    names.add((await results.nth(index).getByTestId("session-search-project").innerText()).trim());
  }
  log(`search "${searchTerm}": ${count} results across ${names.size} projects`, [...names]);
  await page.screenshot({ path: path.join(ARTIFACTS, "live-09-session-search.png") });

  const firstTarget = await results.first().getAttribute("data-session-id");
  await page.getByTestId("session-search-input").press("ArrowDown");
  const secondTarget = await results.nth(1).getAttribute("data-session-id");
  await page.getByTestId("session-search-input").press("Enter");
  await page.waitForURL(new RegExp(`/s/${secondTarget}$`), { timeout: 20_000 });
  log("Enter opened:", secondTarget, `(first result was ${firstTarget})`);

  // Empty state wording, then the palette.
  await page.getByTestId("session-search-input").fill("zzzz-no-such-session");
  await page.getByTestId("session-search-empty").waitFor({ timeout: 20_000 });
  log("empty state:", await page.getByTestId("session-search-empty").innerText());
  await page.screenshot({ path: path.join(ARTIFACTS, "live-10-session-search-empty.png") });

  await page.keyboard.press("Control+k");
  const palette = page.getByRole("dialog");
  await palette.waitFor();
  const paletteInput = palette.getByPlaceholder("搜索命令、项目、会话…");
  await paletteInput.fill("fix");
  const paletteSessions = page.getByTestId("palette-session");
  await paletteSessions.first().waitFor({ timeout: 20_000 });
  log("palette sessions:", await paletteSessions.count());
  await page.screenshot({ path: path.join(ARTIFACTS, "live-11-palette-sessions.png") });
  await paletteInput.press("Escape");
  await page.keyboard.press("Escape");

  // ------------------------------------ item 3: sessions stay above the fold
  const projectCount = (await projects()).length;
  const targetSession = allSessions.sessions[0];
  await page.goto(`${BASE}/p/${targetSession.project_id}/s/${targetSession.id}`);
  await page.getByTestId("workspace-panels").waitFor({ timeout: 60_000 });
  const box = async (testId) => {
    const rect = await page.getByTestId(testId).first().boundingBox();
    if (!rect) throw new Error(`${testId} has no box`);
    return rect;
  };
  const searchBox = await box("session-search-input");
  const firstRow = await box("session-link");
  log(
    `with ${projectCount} projects at 1440x900: search y=${searchBox.y.toFixed(0)}, ` +
      `first session y=${firstRow.y.toFixed(0)}`,
  );
  await page.screenshot({ path: path.join(ARTIFACTS, "live-12-sessions-above-fold.png") });

  log("console/page errors:", errors.length ? errors.slice(0, 5) : "none");
} finally {
  await browser.close();
}
