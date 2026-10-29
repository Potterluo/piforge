// Verification probe for the three qa13 fixes, run against the deployed app.
//
//   node e2e/scripts/qa13-verify.mjs                 # BASE defaults to :7722
//   BASE=http://127.0.0.1:3100 node e2e/scripts/qa13-verify.mjs
//
// Writes screenshots + a JSON record (with PASS/FAIL per check) under
// e2e/.artifacts/fix-qa13/.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-qa13");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const stamp = Date.now();
const record = { base: BASE, stamp, checks: {}, findings: {} };
const save = () => writeFileSync(path.join(out, "verify-log.json"), JSON.stringify(record, null, 2));

const check = (name, ok, detail) => {
  record.checks[name] = { ok: Boolean(ok), detail };
  console.log(`[${ok ? "PASS" : "FAIL"}] ${name}${detail === undefined ? "" : ` — ${JSON.stringify(detail)}`}`);
  save();
  return ok;
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
const shot = async (page, name) => {
  await page.screenshot({ path: path.join(out, `${name}.png`) });
  console.log("[shot]", `${name}.png`);
};

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

const treeRequests = [];
page.on("response", (res) => {
  if (res.url().includes("/files/tree")) treeRequests.push({ at: Date.now(), path: res.url() });
});

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
console.log("[login] ok");

const csrf = async () => csrfOf(await page.context().cookies());

async function newProject(name) {
  const res = await page.request.post(`${BASE}/v1/projects`, {
    headers: { "X-PF-CSRF": await csrf() },
    data: { name, repo_url: "", default_branch: "main" },
  });
  if (!res.ok()) throw new Error(`project ${res.status()} ${await res.text()}`);
  return (await res.json()).project.id;
}

async function newSession(projectId, title) {
  const res = await page.request.post(`${BASE}/v1/projects/${projectId}/sessions`, {
    headers: { "X-PF-CSRF": await csrf() },
    data: { title },
  });
  if (!res.ok()) throw new Error(`session ${res.status()} ${await res.text()}`);
  return (await res.json()).session.id;
}

async function writeFile(sessionId, file, body) {
  const res = await page.request.put(
    `${BASE}/v1/sessions/${sessionId}/files?path=${encodeURIComponent(file)}`,
    {
      headers: { "X-PF-CSRF": await csrf(), "Content-Type": "text/plain; charset=utf-8" },
      data: body,
    },
  );
  if (!res.ok()) throw new Error(`write ${file}: ${res.status()} ${await res.text()}`);
}

const treePaths = async () =>
  page
    .locator('[data-testid="file-node"]')
    .evaluateAll((els) => els.map((el) => el.getAttribute("data-path")));

// ---------------------------------------------------------------------------
// FINDING 1 — the tree refreshes itself; the empty state has an action.
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(`fixv-tree-${stamp}`);
  const sessionId = await newSession(projectId, "fixv tree");
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await page.getByTestId("panel-tab-files").click();
  await page.getByTestId("file-tree").waitFor({ timeout: 20_000 });
  await page.waitForTimeout(1_500);

  check(
    "f1: empty state offers 新建文件",
    (await page.getByTestId("file-create").count()) === 1,
    await page.getByTestId("file-tree").innerText(),
  );
  await shot(page, "fix-1-empty-state");

  // The empty-state action really creates the file and opens it.
  await page.getByTestId("file-create").click();
  await page.getByTestId("file-create-dialog").waitFor({ timeout: 10_000 });
  await shot(page, "fix-1-create-dialog");
  await page.getByTestId("file-create-path").fill("from-empty-state.txt");
  await page.getByTestId("file-create-submit").click();
  await page
    .locator('[data-testid="file-node"][data-path="from-empty-state.txt"]')
    .waitFor({ timeout: 15_000 });
  check("f1: created file appears in the tree and previews", await page.getByTestId("file-preview").isVisible(), await treePaths());
  await shot(page, "fix-1-created-from-empty-state");

  // Terminal: a real shell command writes a file the app never hears about
  // through the files API. The tree must pick it up on its own.
  await page.getByTestId("panel-tab-terminal").click();
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="terminal-pane"]')?.getAttribute("data-pty-status") ===
      "open",
    undefined,
    { timeout: 40_000 },
  );
  await page.waitForTimeout(1_000);
  await page.locator(".xterm-helper-textarea").first().click({ force: true });
  await page.keyboard.type("echo terminal-was-here > terminal-created.txt");
  await page.keyboard.press("Enter");

  await page.getByTestId("panel-tab-files").click();
  const before = treeRequests.length;
  const seenAt = Date.now();
  await page
    .locator('[data-testid="file-node"][data-path="terminal-created.txt"]')
    .waitFor({ timeout: 15_000 });
  const elapsed = Date.now() - seenAt;
  const during = treeRequests.length - before;

  check("f1: terminal-created file appears without refresh", true, { elapsedMs: elapsed });
  check("f1: refresh is debounced, not a poll", during <= 8, { treeRequestsSinceTabSwitch: during, elapsedMs: elapsed });
  await shot(page, "fix-1-tree-auto-refresh");
  record.findings.finding1 = { elapsedMs: elapsed, treeRequests: during, paths: await treePaths() };

  // Window focus is the signal the event stream cannot give (a command typed in
  // another window, a colleague pushing to the checkout). Synthetic focus is
  // enough here: the listener is the thing under test.
  await writeFile(sessionId, "by-someone-else.txt", "outside the editor\n");
  await page.waitForTimeout(500);
  const beforeFocus = treeRequests.length;
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await page
    .locator('[data-testid="file-node"][data-path="by-someone-else.txt"]')
    .waitFor({ timeout: 10_000 });
  check("f1: window focus refreshes the tree", true, { treeRequestsSinceFocus: treeRequests.length - beforeFocus });
  await shot(page, "fix-1-focus-refresh");
}

// ---------------------------------------------------------------------------
// FINDING 2 — an unknown session id is a not-found page, not a live shell.
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(`fixv-bad-${stamp}`);
  const missing = `missing-${stamp}`;
  const requests = [];
  const onResponse = (res) => {
    if (res.url().includes(`/v1/sessions/${missing}`)) {
      requests.push({ status: res.status(), url: res.url().replace(BASE, "") });
    }
  };
  page.on("response", onResponse);

  await page.goto(`${BASE}/p/${projectId}/s/${missing}`);
  await page.getByTestId("session-not-found").waitFor({ timeout: 20_000 });
  await shot(page, "fix-2-session-not-found");

  const composer = await page.getByTestId("composer-input").count();
  const panels = await page.getByTestId("workspace-panels").count();
  const events = requests.filter((r) => r.url.includes("/events"));
  const back = await page.getByTestId("session-not-found-project").getAttribute("href");

  check("f2: not-found state renders", true, await page.getByTestId("session-not-found").innerText());
  check("f2: composer is gone", composer === 0, { composer });
  check("f2: workspace panels are not mounted", panels === 0, { panels });
  check("f2: event stream is not opened at all", events.length === 0, { events });
  check("f2: there is a way back", back === `/p/${projectId}`, { href: back, requests: requests.length });

  // And the link works.
  await page.getByTestId("session-not-found-project").click();
  await page.waitForURL(`**/p/${projectId}`, { timeout: 20_000 });
  check("f2: 返回项目 navigates", page.url().endsWith(`/p/${projectId}`), page.url());
  page.off("response", onResponse);
  record.findings.finding2 = { requests, composer, panels, events: events.length };
}

// ---------------------------------------------------------------------------
// FINDING 3 — one row per path; an untracked file has a reviewable diff.
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(`fixv-git-${stamp}`);
  const sessionId = await newSession(projectId, "fixv git");
  for (const [file, body] of [
    ["fresh.txt", "fresh\n"],
    ["hello.txt", "hello\n"],
    ["notes.md", "# notes\n"],
    ["src/a.ts", "export const a = 1;\n"],
  ]) {
    await writeFile(sessionId, file, body);
  }

  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await page.getByTestId("panel-tab-changes").click();
  await page.getByTestId("git-panel").waitFor({ timeout: 20_000 });
  await page.locator('[data-testid="git-change"]').first().waitFor({ timeout: 20_000 });

  // A new file must be reviewable before it is staged.
  await page.locator('[data-testid="git-change"][data-path="fresh.txt"]').first().click();
  await page.waitForTimeout(2_500);
  const diffPane = page.getByTestId("git-diff-patch").or(page.getByTestId("git-diff-view")).first();
  const untrackedDiffText = (await diffPane.count()) ? await diffPane.innerText() : "";
  const panelText = await page.getByTestId("git-panel").innerText();
  check(
    "f3: an untracked file shows its diff",
    /\+fresh/.test(untrackedDiffText) && !panelText.includes("这个文件在当前范围内没有差异"),
    untrackedDiffText.split("\n").slice(-8),
  );
  await shot(page, "fix-3-untracked-diff");

  await page.getByTestId("git-stage-all").click();
  await page.waitForTimeout(2_500);
  await shot(page, "fix-3-staged");

  const rows = await page.locator('[data-testid="git-change"]').evaluateAll((els) =>
    els.map((el) => ({ path: el.getAttribute("data-path"), state: el.getAttribute("data-state") })),
  );
  const states = await page.getByTestId("git-state").allInnerTexts();
  const statusAfter = await (
    await page.request.get(`${BASE}/v1/sessions/${sessionId}/git/status`)
  ).json();

  check("f3: one row per path after 全部暂存", rows.length === 4 && new Set(rows.map((r) => r.path)).size === 4, rows);
  check("f3: no row says 部分暂存 for a fully staged file", !states.includes("部分暂存"), states);
  check("f3: every row is 已暂存", states.filter((s) => s === "已暂存").length === 4, states);
  record.findings.finding3 = { rows, states, statusAfter, untrackedDiffText: untrackedDiffText.split("\n").slice(-12) };
}

save();
console.log("[out]", path.join(out, "verify-log.json"));
await browser.close();
