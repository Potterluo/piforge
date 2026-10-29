// Reproduction probe for the three qa13 findings, run against the deployed app.
//
//   node e2e/scripts/qa13-repro.mjs            # BASE defaults to :7722
//   BASE=http://127.0.0.1:3100 node e2e/scripts/qa13-repro.mjs
//
// It writes screenshots and a JSON record under e2e/.artifacts/fix-qa13/.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-qa13");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const stamp = Date.now();
const record = { base: BASE, stamp, findings: {} };

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
const shot = async (page, name) => {
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false });
  console.log("[shot]", `${name}.png`);
};

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const save = () => writeFileSync(path.join(out, "repro-log.json"), JSON.stringify(record, null, 2));

const treeRequests = [];
page.on("response", (res) => {
  const url = res.url();
  if (url.includes("/files/tree")) treeRequests.push({ at: Date.now(), status: res.status(), url });
});

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
console.log("[login] ok");

async function newProject(name) {
  const res = await page.request.post(`${BASE}/v1/projects`, {
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    data: { name, repo_url: "", default_branch: "main" },
  });
  if (!res.ok()) throw new Error(`project create ${res.status()} ${await res.text()}`);
  return (await res.json()).project.id;
}

async function newSession(projectId, title) {
  const res = await page.request.post(`${BASE}/v1/projects/${projectId}/sessions`, {
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    data: { title },
  });
  if (!res.ok()) throw new Error(`session create ${res.status()} ${await res.text()}`);
  return (await res.json()).session.id;
}

async function writeFile(sessionId, file, body) {
  const res = await page.request.put(
    `${BASE}/v1/sessions/${sessionId}/files?path=${encodeURIComponent(file)}`,
    {
      headers: {
        "X-PF-CSRF": csrfOf(await page.context().cookies()),
        "Content-Type": "text/plain; charset=utf-8",
      },
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
// FINDING 3 — git review state (do it first: it wants untracked files).
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(`fixqa-git-${stamp}`);
  const sessionId = await newSession(projectId, "fixqa git");

  // The files exist before the panel ever loads: this finding is about how the
  // panel classifies changes, not about when it refetches.
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

  const status = await page.request.get(`${BASE}/v1/sessions/${sessionId}/git/status`);
  const statusBody = await status.json();
  console.log("[f3] status", status.status(), JSON.stringify(statusBody));
  record.findings.finding3 = { sessionId, status: statusBody };
  save();

  await page.waitForTimeout(1_500);
  await shot(page, "repro-3-panel-before");
  record.findings.finding3.panelTextBefore = (
    await page.locator('[data-testid="git-panel"]').innerText()
  ).split("\n");
  save();

  // Click an untracked file: what does the diff pane say?
  const untrackedRow = page.locator('[data-testid="git-change"][data-path="fresh.txt"]').first();
  await untrackedRow.waitFor({ timeout: 20_000 });
  await untrackedRow.click();
  await page.waitForTimeout(1_500);
  await shot(page, "repro-3-untracked-diff");
  record.findings.finding3.diffText = (
    await page.locator('[data-testid="git-panel"]').innerText()
  )
    .split("\n")
    .filter((line) => line.includes("差异") || line.includes("@@") || line.includes("fresh"))
    .slice(0, 12);

  const diffResponse = await page.request.get(
    `${BASE}/v1/sessions/${sessionId}/git/diff?scope=unstaged&path=fresh.txt`,
  );
  record.findings.finding3.untrackedDiffApi = {
    status: diffResponse.status(),
    body: await diffResponse.json(),
  };

  // 全部暂存, then count what the panel claims.
  await page.getByTestId("git-stage-all").click();
  await page.waitForTimeout(2_500);
  await shot(page, "repro-3-staged");

  const rows = await page
    .locator('[data-testid="git-change"]')
    .evaluateAll((els) => els.map((el) => el.getAttribute("data-path")));
  record.findings.finding3.rowsAfterStageAll = rows;
  record.findings.finding3.distinctAfterStageAll = [...new Set(rows)];
  record.findings.finding3.partialBadges = await page.getByTestId("git-partial").count();
  const groupTitles = await page
    .locator('[data-testid="git-panel"] button:has-text("已暂存"), [data-testid="git-panel"] button:has-text("未暂存"), [data-testid="git-panel"] button:has-text("未跟踪")')
    .allInnerTexts();
  record.findings.finding3.groupTitles = groupTitles;
  const statusAfter = await page.request.get(`${BASE}/v1/sessions/${sessionId}/git/status`);
  record.findings.finding3.statusAfterStageAll = await statusAfter.json();
  console.log("[f3]", JSON.stringify(record.findings.finding3, null, 2));
}

// ---------------------------------------------------------------------------
// FINDING 1 — a file created by the terminal, seen by the file tree.
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(`fixqa-tree-${stamp}`);
  const sessionId = await newSession(projectId, "fixqa tree");
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await page.getByTestId("panel-tab-files").click();
  await page.getByTestId("file-tree").waitFor({ timeout: 20_000 });
  await page.waitForTimeout(2_000);
  await shot(page, "repro-1-tree-before");
  const before = {
    paths: await treePaths(),
    text: await page.getByTestId("file-tree").innerText(),
  };
  console.log("[f1] before", JSON.stringify(before));

  // Terminal: a real shell in the worktree, writing a file the app never sees.
  await page.getByTestId("panel-tab-terminal").click();
  await page
    .getByTestId("terminal-pane")
    .waitFor({ timeout: 30_000 });
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="terminal-pane"]')?.getAttribute("data-pty-status") ===
      "open",
    undefined,
    { timeout: 40_000 },
  );
  await page.waitForTimeout(1_500);
  await page.locator(".xterm-helper-textarea").first().click({ force: true });
  await page.keyboard.type("echo terminal-was-here > terminal-created.txt");
  await page.keyboard.press("Enter");
  await page.waitForTimeout(3_000);
  await shot(page, "repro-1-terminal");

  await page.getByTestId("panel-tab-files").click();
  const seenAt = Date.now();
  const waitFor = Number(process.env.WAIT_MS ?? 13_000);
  await page.waitForTimeout(waitFor);
  await shot(page, "repro-1-tree-after-wait");
  const after = {
    waitedMs: Date.now() - seenAt,
    paths: await treePaths(),
    text: await page.getByTestId("file-tree").innerText(),
    treeRequests: treeRequests.length,
  };
  console.log("[f1] after", JSON.stringify(after));
  record.findings.finding1 = { before, after, waitFor };

  // Does the manual refresh see it? Proves the file exists and the API is fine.
  await page.getByRole("button", { name: "刷新文件树" }).click();
  await page.waitForTimeout(2_000);
  await shot(page, "repro-1-tree-manual-refresh");
  record.findings.finding1.afterManualRefresh = await treePaths();
  console.log("[f1] manual", JSON.stringify(record.findings.finding1.afterManualRefresh));
}

// ---------------------------------------------------------------------------
// FINDING 2 — an unknown session id.
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(`fixqa-bad-${stamp}`);
  const missing = `missing-${stamp}`;
  const eventRequests = [];
  const onResponse = (res) => {
    if (res.url().includes(`/v1/sessions/${missing}`)) {
      eventRequests.push({ status: res.status(), url: res.url().replace(BASE, "") });
    }
  };
  page.on("response", onResponse);

  await page.goto(`${BASE}/p/${projectId}/s/${missing}`);
  await page.waitForTimeout(8_000);
  await shot(page, "repro-2-unknown-session");
  record.findings.finding2 = {
    missingSessionId: missing,
    url: page.url(),
    composerCount: await page.getByTestId("composer-input").count(),
    sessionTitleText: await page.getByTestId("session-title").count().then(async (n) =>
      n ? await page.getByTestId("session-title").innerText() : null,
    ),
    bodyText: (await page.locator("body").innerText()).split("\n").slice(0, 40),
    requests: eventRequests,
    notFoundVisible: await page.getByText("会话不存在").count(),
  };
  console.log("[f2]", JSON.stringify(record.findings.finding2, null, 2));
  page.off("response", onResponse);
}

writeFileSync(path.join(out, "repro-log.json"), JSON.stringify(record, null, 2));
console.log("[out]", path.join(out, "repro-log.json"));
await browser.close();
