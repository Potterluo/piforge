// Capture the curated, *real* screenshot set for the public README and the
// GitHub Pages site: docs/screenshots/live/NN-slug.png.
//
// What makes these shots different from docs/screenshots/: that set was
// produced against the fake Pi peer on a scratch control plane. This script
// drives a running deployment with a real provider, so every transcript, tool
// call, test result, diff, audit row and container hostname below was actually
// produced by an agent run.
//
// What it does, in order:
//   1. signs in as the seeded dev account and creates its own throwaway project
//      (`pubshots-<epoch-ms>`) and one session in it;
//   2. sends ONE real prompt (a Node script that prints the first ten primes,
//      with a test that is run, then committed) and photographs the run while it
//      streams and after it settles;
//   3. photographs the 测试 panel, the 变更 panel, the built-in terminal
//      (hostname/pwd inside the sandbox), and the fork dialog;
//   4. sends ONE short follow-up, stops it with the composer's 停止 button as
//      soon as its first file edit lands, and photographs the resulting 工作区
//      diff;
//   5. sends the inline review comment on a line of that diff (ONE more short
//      turn) and photographs the transcript with the quote and the 位置 line;
//   6. signs in to a second context as the seeded admin and photographs
//      /admin/status and /admin/audit;
//   7. deletes the throwaway project and verifies it is gone, then re-checks the
//      deployment's /healthz.
//
// Re-runnable: every run makes its own project (the name carries a timestamp),
// so it can be run again against the same deployment without collisions. If a
// run dies half-way the project is still deleted (unless KEEP_PROJECT=1, which
// is there for debugging the script itself).
//
// Before every screenshot the script scans the page's visible text for
// credential-shaped strings (sk-…, AKIA…, JWT, Bearer …, password=…) and aborts
// rather than save a pixel with a key in it. Transient toasts are hidden: they
// are confirmations of an action, not the subject of any shot.
//
// Known deployment skew: 09 and 10 document what the *running* build actually
// has. A build older than the checkout has no 租户配额与网络 card on
// /admin/status and no /admin/audit route at all; in that case 09 is captured
// without the tenant section and 10 is skipped with a log line rather than
// failing the run. On a deployment built from this checkout both are captured.
//
// Prerequisites: the deployment is up (default http://127.0.0.1:7722), the two
// seeded accounts exist, a working provider/model is configured (this script
// pins nothing — it uses whatever the deployment's default model is), and
// Playwright + a local Chrome are installed (`pnpm -C e2e install`,
// `channel: "chrome"`).
//
// Run it with:
//   cd e2e && node scripts/public-shots.mjs
//
// Env:
//   PIFORGE_BASE            deployment origin           (default http://127.0.0.1:7722)
//   OUT                     screenshot directory        (default ../docs/screenshots/live)
//   PIFORGE_DEV_EMAIL/PASSWORD, PIFORGE_ADMIN_EMAIL/PASSWORD
//   KEEP_PROJECT=1          do not delete the project (debugging only)
//   ONLY=02,03              capture only the named steps (debugging only)
//
// Viewport 1440x900 at deviceScaleFactor 2 throughout; /admin/status is captured
// as a full page because the runtime-directory, quota and tenant-limit cards do
// not fit in one viewport.

import { chromium } from "@playwright/test";
import { mkdirSync, readdirSync, statSync } from "node:fs";
import os from "node:os";
import path from "node:path";

const BASE = (process.env.PIFORGE_BASE ?? "http://127.0.0.1:7722").replace(/\/+$/, "");
const OUT = path.resolve(process.env.OUT ?? path.resolve(process.cwd(), "../docs/screenshots/live"));
const KEEP_PROJECT = process.env.KEEP_PROJECT === "1";
const ONLY = new Set(
  (process.env.ONLY ?? "")
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean),
);

const DEV = {
  email: process.env.PIFORGE_DEV_EMAIL ?? "dev@piforge.local",
  password: process.env.PIFORGE_DEV_PASSWORD ?? "piforge-dev-123",
};
const ADMIN = {
  email: process.env.PIFORGE_ADMIN_EMAIL ?? "admin@piforge.local",
  password: process.env.PIFORGE_ADMIN_PASSWORD ?? "piforge-admin-123",
};

const VIEWPORT = { width: 1440, height: 900 };
const SCALE = 2;

const PROMPT = "写一个 Node 脚本输出前 10 个素数，配一个测试并运行它，然后 git commit";
// The follow-up has to *add* something the first run cannot already contain
// (the first run already writes a 0-case and passes it), or the agent answers
// "nothing to change" and there is no diff to review.
const FOLLOWUP =
  "在 primes.js 里加一个 isPrime(n) 导出，并在测试文件里补两个断言：isPrime(9) 为 false、isPrime(97) 为 true；" +
  "再加一个耗时 3 秒的用例（await new Promise((r) => setTimeout(r, 3000))）。然后运行 node --test 跑一次，改完先不要提交。";
// A review comment is composed per line below (the sentence has to be about the
// code it quotes), so there is no fixed REVIEW string here.

const PROJECT = `pubshots-${Date.now()}`;
const SESSION_TITLE = `素数脚本：测试与提交`;

const started = Date.now();
let page;
const shots = [];
const log = (...parts) => {
  const at = ((Date.now() - started) / 1000).toFixed(1).padStart(5);
  console.log(`[${at}s]`, ...parts);
};

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

/** Credential shapes that must never end up in a screenshot. */
const SECRET_PATTERNS = [
  { name: "openai-style key", re: /\bsk-[A-Za-z0-9_-]{16,}/ },
  { name: "aws access key", re: /\bAKIA[0-9A-Z]{16}\b/ },
  { name: "jwt", re: /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\./ },
  { name: "bearer header", re: /Bearer\s+[A-Za-z0-9._-]{16,}/i },
  { name: "assignment to a secret-ish name", re: /(api[_-]?key|secret|passwd|password|access[_-]?token)\s*[:=]\s*\S{8,}/i },
  { name: "pf_ cookie", re: /\bpf_(session|csrf|gateway)=/ },
];

/**
 * Reads the page's visible text and refuses to continue if it looks like it
 * carries a credential. A false positive is a loud failure in the run, which is
 * the right trade for a screenshot set that is published.
 */
async function assertNoSecrets(page, where) {
  const text = await page.evaluate(() => document.body?.innerText ?? "");
  for (const { name, re } of SECRET_PATTERNS) {
    const hit = re.exec(text);
    if (hit) {
      throw new Error(
        `refusing to save ${where}: visible text matches ${name} (${JSON.stringify(hit[0].slice(0, 24))}…)`,
      );
    }
  }
}

/** PNG dimensions from the IHDR chunk, so the report carries real pixel sizes. */
function pngSize(buffer) {
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

/**
 * Screenshot the current page. `settle` waits before the shutter so a
 * half-drawn panel cannot be caught mid-flight.
 */
async function shot(page, name, { fullPage = false, settle = 300 } = {}) {
  if (ONLY.size > 0 && !ONLY.has(name.slice(0, 2))) {
    log(`skip ${name} (ONLY=${[...ONLY].join(",")})`);
    return;
  }
  if (settle) await page.waitForTimeout(settle);
  await hideToasts(page);
  await assertNoSecrets(page, name);
  const file = path.join(OUT, `${name}.png`);
  const buffer = await page.screenshot({ path: file, fullPage });
  const { width, height } = pngSize(buffer);
  shots.push({ name, file, width, height });
  log(`shot ${name} (${width}x${height})`);
}

/**
 * Hides the toast stack.
 *
 * Toasts are transient confirmations ("已中断", "已发送给 agent") that sit over
 * the corner of the panel. They are not the subject of any of these shots and a
 * capture that catches one looks like a bug report. A style tag covers the
 * ordinary case; `hideToasts` below re-applies it at the shutter, because the
 * toaster is mounted lazily by sonner and survives nothing.
 */
async function hideTransientUI(target) {
  await target.addStyleTag({ content: "[data-sonner-toaster]{display:none!important}" });
}

/** Belt and braces at the shutter: an inline `display:none` on the live toaster. */
async function hideToasts(target) {
  await target
    .evaluate(() => {
      document.querySelectorAll("[data-sonner-toaster]").forEach((node) => {
        node.style.display = "none";
      });
    })
    .catch(() => {});
}

/**
 * Grows the frame until the page's own content fits.
 *
 * The shell is `h-svh overflow-hidden` with `<main>` inside it, and the admin
 * pages carry no scroll container of their own: whatever does not fit is
 * clipped, not scrollable. At 900 CSS px the operator page cuts off the
 * per-sandbox quota list and the tenant/egress card — the very cards 09 exists
 * to show. A taller window renders the same layout at the same 1440 width, and
 * the log prints the height it settled on.
 */
async function fitFrameTo(target, testId, { start = 1100, step = 200, max = 2100 } = {}) {
  for (let height = start; height <= max; height += step) {
    await target.setViewportSize({ width: VIEWPORT.width, height });
    await target.waitForTimeout(320);
    const fits = await target.evaluate((id) => {
      const element = document.querySelector(`[data-testid="${id}"]`);
      if (!element) return true;
      return element.scrollHeight + 8 <= window.innerHeight;
    }, testId);
    if (fits) return height;
  }
  return max;
}

async function csrf(page) {
  const cookies = await page.context().cookies();
  return cookies.find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
}

/** Signed-in API call through the browser context (cookies + double-submit CSRF). */
async function api(page, method, url, data) {
  const headers = { "X-PF-CSRF": await csrf(page) };
  if (typeof data === "string") headers["Content-Type"] = "text/plain; charset=utf-8";
  const res = await page.request.fetch(url.startsWith("http") ? url : BASE + url, {
    method,
    headers,
    data,
  });
  const text = await res.text();
  let body = null;
  try {
    body = JSON.parse(text);
  } catch {
    body = text;
  }
  return { status: res.status(), body };
}

async function login(page, who) {
  await page.goto(`${BASE}/login`, { waitUntil: "domcontentloaded" });
  const submit = page.getByTestId("login-submit");
  // The submit button stays disabled until React has hydrated; typing before
  // that lets the hydration pass wipe the fields, which reads as "wrong
  // password". Waiting on the button is the cheap way to avoid that.
  await submit.waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForFunction(
    () => {
      const button = document.querySelector('[data-testid="login-submit"]');
      return button instanceof HTMLButtonElement && !button.disabled;
    },
    undefined,
    { timeout: 30_000, polling: 100 },
  );
  await page.getByTestId("login-email").fill(who.email);
  await page.getByTestId("login-password").fill(who.password);
  await submit.click();
  await page.getByTestId("app-sidebar").waitFor({ state: "visible", timeout: 30_000 });
  await hideTransientUI(page);
}

const runState = () =>
  page.evaluate(
    () => document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") ?? "",
  );

async function waitRunState(want, timeout) {
  await page.waitForFunction(
    (state) => document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") === state,
    want,
    { timeout, polling: 150 },
  );
}

/** What the transcript currently looks like, for the mid-run capture loop. */
function transcriptShape() {
  return page.evaluate(() => {
    const assistant = Array.from(document.querySelectorAll('[data-testid="turn-assistant"]'))
      .map((turn) => turn.innerText)
      .join("\n");
    const groups = Array.from(document.querySelectorAll('[data-testid="tool-group"]'));
    const traces = Array.from(document.querySelectorAll('[data-testid="tool-trace"]'));
    // A call is in flight when its row says 运行中, when the trace is still
    // "等待输出…", or when the stream carries the word at all — the three shapes
    // this panel uses depending on whether the call is grouped yet.
    const streamText = document.querySelector('[data-testid="session-stream"]')?.innerText ?? "";
    const live = [...groups, ...traces].filter((node) => node.innerText.includes("运行中"));
    const waiting = /等待输出/.test(streamText) ? 1 : 0;
    const stop = document.querySelector('[data-testid="composer-stop"]');
    return {
      state: document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") ?? "",
      assistantChars: assistant.trim().length,
      groups: groups.length,
      traceRows: traces.length,
      liveHits: live.length + waiting + (streamText.includes("运行中") ? 1 : 0),
      liveText: [
        ...live.map((node) => node.innerText.replace(/\s+/g, " ").trim()),
        waiting ? "（工具输出占位：等待输出）" : "",
      ]
        .filter(Boolean)
        .join(" | "),
      groupText: groups.map((node) => node.innerText.replace(/\s+/g, " ").trim()).join(" | "),
      stopVisible: Boolean(stop && stop.getBoundingClientRect().width > 0),
      composerEnabled: Boolean(
        document.querySelector('[data-testid="composer-input"]')?.disabled === false,
      ),
      text: streamText.slice(-4000),
    };
  });
}

/** Expands the tool group that is currently running, so its live row is visible. */
async function expandLiveGroup() {
  const group = page
    .locator('[data-testid="tool-group"]')
    .filter({ hasText: "运行中" })
    .last();
  if ((await group.count()) === 0) return false;
  const expanded = await group.getAttribute("aria-expanded");
  if (expanded !== "true") await group.click({ timeout: 2_000 }).catch(() => {});
  return true;
}

/**
 * Captures 02 while a run is in flight.
 *
 * The subject is a run in motion: streamed assistant text, a tool call that is
 * still running, and the composer in its stop/queue shape. Every condition is
 * re-checked immediately before the shutter, because a run that ends between the
 * check and the screenshot would leave the settled composer in a file named
 * "running" — the first attempt at this set did exactly that.
 */
async function captureMidRun() {
  const deadline = Date.now() + 5 * 60_000;
  let frames = 0;
  let lastLog = 0;
  while (Date.now() < deadline) {
    const shape = await transcriptShape();
    if (shape.state !== "running") break;
    if (Date.now() - lastLog > 1500) {
      lastLog = Date.now();
      log(
        `  probe: chars=${shape.assistantChars} groups=${shape.groups} traces=${shape.traceRows} ` +
          `live=${shape.liveHits} stop=${shape.stopVisible}` +
          (shape.liveText ? ` liveText=${JSON.stringify(shape.liveText.slice(0, 110))}` : ""),
      );
    }
    if (shape.stopVisible && shape.liveHits > 0) {
      await expandLiveGroup().catch(() => {});
      const stillRunning = await page.evaluate(() => {
        const stop = document.querySelector('[data-testid="composer-stop"]');
        const state = document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state");
        return state === "running" && Boolean(stop && stop.getBoundingClientRect().width > 0);
      });
      if (stillRunning) {
        await shot(page, "02-workbench-running", { settle: 0 });
        frames += 1;
        const after = await runState();
        log(
          `  frame ${frames}: captured while running (state after shutter: ${after}), ` +
            `${shape.assistantChars} chars, live=${shape.liveHits} ${JSON.stringify(shape.liveText.slice(0, 90))}`,
        );
        if (after !== "running") break;
        if (frames >= 4) break; // the last one is on disk; more only churns the file
      }
    }
    await page.waitForTimeout(60);
  }
  if (frames === 0) {
    const shape = await transcriptShape();
    log(
      `warning: no in-flight frame was captured; last transcript tail: ${JSON.stringify(shape.text.slice(-240))}`,
    );
  }
  return frames;
}

/** Collapses every expanded tool group — the settled shot wants one line each. */
async function collapseAllGroups() {
  const groups = page.locator('[data-testid="tool-group"]');
  const total = await groups.count();
  for (let index = 0; index < total; index += 1) {
    const group = groups.nth(index);
    if ((await group.getAttribute("aria-expanded")) === "true") {
      await group.click({ timeout: 2_000 }).catch(() => {});
      await page.waitForTimeout(120);
    }
  }
}

async function poll(fn, { timeout, interval = 250, what }) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const value = await fn();
    if (value) return value;
    await page.waitForTimeout(interval);
  }
  throw new Error(`timed out after ${timeout}ms waiting for ${what}`);
}

// --------------------------------------------------------------------------
// cleanup
// --------------------------------------------------------------------------

async function deleteProject(projectId) {
  if (!projectId) return;
  if (KEEP_PROJECT) {
    log(`KEEP_PROJECT=1: leaving ${projectId} in place`);
    return;
  }
  log(`deleting project ${projectId}`);
  const removed = await api(page, "DELETE", `/v1/projects/${projectId}`);
  log(`delete answered ${removed.status}`);
  const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
  const stillListed = (listed.projects ?? []).some((project) => project.id === projectId);
  const fetched = await page.request.get(`${BASE}/v1/projects/${projectId}`);
  const health = await page.request.get(`${BASE}/healthz`).catch(() => null);
  const healthy = health ? health.ok() : false;
  log(
    `cleanup: listed=${stillListed ? "STILL PRESENT" : "gone"} get=${fetched.status()} healthz=${healthy ? "ok" : "FAILED"}`,
  );
  if (stillListed || fetched.status() !== 404 || !healthy) {
    throw new Error("cleanup check failed: the throwaway project or the deployment is not healthy");
  }
}

// --------------------------------------------------------------------------
// main
// --------------------------------------------------------------------------

mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({ channel: "chrome" });
let projectId = null;
let sessionId = null;

try {
  const context = await browser.newContext({
    viewport: VIEWPORT,
    deviceScaleFactor: SCALE,
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
  });
  page = await context.newPage();
  page.on("pageerror", (error) => log("[pageerror]", error.message));

  // ---------------------------------------------------------------- sign in
  log(`signing in as ${DEV.email} on ${BASE}`);
  await login(page, DEV);
  await hideTransientUI(page);

  // ---------------------------------------------------------------- project
  const created = await api(page, "POST", "/v1/projects", {
    name: PROJECT,
    repo_url: "",
    default_branch: "main",
  });
  if (created.status >= 300) throw new Error(`create project: ${created.status} ${JSON.stringify(created.body)}`);
  projectId = created.body.project.id;
  log(`project ${PROJECT} = ${projectId}`);

  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: SESSION_TITLE,
  });
  if (session.status >= 300) throw new Error(`create session: ${session.status}`);
  sessionId = session.body.session.id;
  log(`session ${sessionId}`);

  // ---------------------------------------------------------------- workbench
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`, { waitUntil: "domcontentloaded" });
  const composer = page.getByTestId("composer-input");
  await composer.waitFor({ state: "visible", timeout: 60_000 });
  await hideTransientUI(page);
  await page.waitForFunction(
    () => {
      const input = document.querySelector('[data-testid="composer-input"]');
      return input instanceof HTMLTextAreaElement && !input.disabled;
    },
    undefined,
    { timeout: 60_000, polling: 150 },
  );
  await page.waitForTimeout(600);

  // ---------------------------------------------------------------- the run
  log("sending the real prompt");
  await composer.fill(PROMPT);
  await page.getByTestId("composer-send").click();
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="turn-user"]').length >= 1,
    undefined,
    { timeout: 30_000, polling: 100 },
  );
  await waitRunState("running", 60_000);
  log("waiting for the run to settle");
  await waitRunState("settled", 10 * 60_000);
  const settled = await transcriptShape();
  if (/模型调用失败|检查「设置/.test(settled.text)) {
    throw new Error("the run ended in a model failure, not a result — not publishing that");
  }
  log(`  settled transcript: ${settled.assistantChars} assistant chars, ${settled.groups} tool groups`);

  // ---------------------------------------------------------------- 01 home
  // Taken after the run so the project card carries its real session count and
  // last-active time. The filter narrows the grid to this run's project; the
  // deployment's other projects are its own leftovers and not part of the story.
  await page.goto(`${BASE}/`, { waitUntil: "domcontentloaded" });
  await page.getByTestId("new-project-button").waitFor({ state: "visible", timeout: 30_000 });
  await hideTransientUI(page);
  await page.getByTestId("project-filter").fill(PROJECT);
  await page.waitForFunction(
    (name) => {
      const cards = Array.from(document.querySelectorAll('[data-testid="project-card"]'));
      return cards.length === 1 && (cards[0].textContent ?? "").includes(name);
    },
    PROJECT,
    { timeout: 20_000, polling: 150 },
  );
  await shot(page, "01-home", { settle: 500 });

  // ---------------------------------------------------------------- back to the session
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`, { waitUntil: "domcontentloaded" });
  await page.getByTestId("composer-input").waitFor({ state: "visible", timeout: 60_000 });
  await hideTransientUI(page);

  // ---------------------------------------------------------------- 04 tests
  await page.getByTestId("panel-tab-tests").click();
  await page.locator('[data-testid="test-run"]').first().waitFor({ state: "visible", timeout: 30_000 });
  const testRuns = await page.locator('[data-testid="test-run"]').allInnerTexts();
  log(`  test runs: ${JSON.stringify(testRuns)}`);
  const parsed = testRuns.some((row) => /✅|❌|⚠️/.test(row));
  if (!parsed) log("warning: the test rows carry no parsed counts");
  await shot(page, "04-test-panel", { settle: 500 });

  // ---------------------------------------------------------------- 06 terminal
  // The built-in terminal runs inside the sandbox (the container is the pod
  // boundary in this deployment), so `hostname` here must be the container id,
  // not this machine's name. The screenshot is the evidence; the check below
  // only asserts the PTY is live.
  await page.getByTestId("panel-tab-terminal").click();
  await page
    .locator('[data-testid="terminal-pane"][data-pty-status="open"]')
    .waitFor({ state: "visible", timeout: 60_000 });
  await page.waitForTimeout(1200);
  await page.getByTestId("terminal-pane").click({ position: { x: 140, y: 90 } });
  await page.keyboard.type("hostname\r");
  await page.waitForTimeout(900);
  await page.keyboard.type("pwd\r");
  await page.waitForTimeout(1500);
  log(`  terminal pty status: ${await page.getByTestId("terminal-pane").getAttribute("data-pty-status")}`);
  await shot(page, "06-terminal", { settle: 400 });

  // ---------------------------------------------------------------- 08 fork dialog
  // The dialog, filled in but not submitted: creating a branch would add a
  // second session (and a second worktree) to a throwaway project.
  await page.getByTestId("panel-tab-changes").click();
  await page.waitForTimeout(400);
  const firstUserTurn = page.getByTestId("turn-user").first();
  await firstUserTurn.hover();
  await page.getByTestId("fork-button").first().click({ timeout: 15_000 });
  const forkLabel = page.getByTestId("fork-label");
  await forkLabel.waitFor({ state: "visible", timeout: 15_000 });
  await forkLabel.fill("primes-edge-cases");
  await page.waitForFunction(
    () => {
      const button = document.querySelector('[data-testid="fork-submit"]');
      return button instanceof HTMLButtonElement && !button.disabled;
    },
    undefined,
    { timeout: 10_000, polling: 100 },
  );
  await shot(page, "08-fork-dialog", { settle: 400 });
  await page.keyboard.press("Escape");
  await page.waitForTimeout(400);

  // ---------------------------------------------------------------- follow-up: the run 02/03 is about
  // This turn is the one the running/settled pair documents, because it is the
  // only run on this deployment that is slow enough to photograph mid-flight:
  // the agent edits two files and then runs a test that sleeps for 3 seconds, so
  // a tool call is genuinely in flight for several seconds instead of the ~40 ms
  // a `node --test` of this size takes. It also leaves the real, uncommitted
  // worktree change that 05 and 07 review, and it is asked not to commit.
  log("sending the follow-up that edits code and runs a slow test");
  const beforeTurnCount = await page.getByTestId("turn-user").count();
  await page.getByTestId("composer-input").fill(FOLLOWUP);
  await page.getByTestId("composer-send").click();
  await page.waitForFunction(
    (count) => document.querySelectorAll('[data-testid="turn-user"]').length > count,
    beforeTurnCount,
    { timeout: 30_000, polling: 100 },
  );
  await waitRunState("running", 60_000);

  // 02 — mid-run: streamed text, a tool call in flight, the stop/queue composer.
  const liveFrames = await captureMidRun();
  log(`  02: ${liveFrames} frame(s) captured while a tool call was in flight`);

  // 03 — the same session settled, the tool trace collapsed back to one line.
  await waitRunState("settled", 10 * 60_000);
  const settledAfter = await transcriptShape();
  if (/模型调用失败|检查「设置/.test(settledAfter.text)) {
    throw new Error("the follow-up ended in a model failure, not a result — not publishing that");
  }
  await collapseAllGroups();
  // A settled workbench reads better with the file the agent wrote in the panel
  // than with its "未打开文件" placeholder. Clicking the tree node is a real
  // preview load; if the node is not there, the placeholder stays.
  await page.getByTestId("panel-tab-files").click();
  await page
    .locator('[data-testid="file-node"][data-path="primes.js"]')
    .first()
    .click({ timeout: 5_000 })
    .catch(() => {});
  await page.waitForTimeout(900);
  await shot(page, "03-workbench-settled", { settle: 400 });
  log(
    `  settled: ${settledAfter.assistantChars} assistant chars, ${settledAfter.groups} tool group(s), ` +
      `${settledAfter.traceRows} expanded trace row(s)`,
  );

  // ---------------------------------------------------------------- 05 changes
  const diffStatus = await page.request.get(`${BASE}/v1/sessions/${sessionId}/git/status`);
  const diffBody = diffStatus.ok() ? await diffStatus.json() : {};
  const changedPaths = (diffBody.changes ?? []).map((change) => change.path);
  log(`  worktree at review time: ${JSON.stringify(changedPaths)}`);

  await page.getByTestId("panel-tab-changes").click();
  if (!changedPaths.length) {
    // Last resort: the run finished without touching a file. Write one boundary
    // test through the product's own file API so the panel still documents a
    // real worktree change, and say so in the log and the README.
    log("warning: the follow-up produced no file edit; writing one through the files API");
    const content = [
      'const test = require("node:test");',
      'const assert = require("node:assert");',
      'const { firstPrimes } = require("./primes.js");',
      "",
      'test("1 is not prime", () => {',
      "  assert.deepEqual(firstPrimes(1), []);",
      "});",
      "",
    ].join("\n");
    const wrote = await api(
      page,
      "PUT",
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("primes.edge.test.js")}`,
      content,
    );
    log(`  fallback write: ${wrote.status}`);
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByTestId("composer-input").waitFor({ state: "visible", timeout: 60_000 });
    await page.getByTestId("panel-tab-changes").click();
  }
  const changeRow = page.locator('[data-testid="git-change"]').first();
  await changeRow.waitFor({ state: "visible", timeout: 30_000 });
  log(`  change row: ${(await changeRow.innerText()).replace(/\s+/g, " ").trim()}`);
  await changeRow.click();
  await page.locator('[data-testid="git-diff-view"]').waitFor({ state: "visible", timeout: 30_000 });
  await page
    .locator('[data-testid="diff-line-row"][data-kind="add"]')
    .first()
    .waitFor({ state: "visible", timeout: 30_000 });
  const scope = await page.getByTestId("git-diff-scope").textContent().catch(() => "");
  log(`  diff scope: ${scope}`);
  await shot(page, "05-changes-panel", { settle: 400 });

  // ---------------------------------------------------------------- 07 inline comment
  // A comment on added line of that diff. Sending it is the third and last
  // provider turn, and it is also the "continue" half of stop/continue: the
  // agent picks the work back up with the review comment in its prompt.
  const pick = await page.evaluate(() => {
    const rows = Array.from(
      document.querySelectorAll('[data-testid="diff-line-row"][data-kind="add"][data-new-line]'),
    ).map((row) => {
      const raw = (row.innerText ?? "").replace(/\s+/g, " ").trim();
      return {
        line: Number(row.getAttribute("data-new-line")),
        raw,
        // The row renders "old new +code"; keep the code for matching.
        code: raw.replace(/^\d*\s*\d*\s*[+-]\s?/, ""),
      };
    });
    const useful = rows.filter((row) => row.code.length >= 8);
    // Prefer a line a reviewer would actually attach a sentence to, in the order
    // a real review tends to go: an assertion, a test name, the delay, the impl.
    const preferences = [
      (row) => /assert\./.test(row.code),
      (row) => /test\(|it\(/.test(row.code),
      (row) => /settimeout|await |promise|sleep/i.test(row.code),
      (row) => /function|return|=>/.test(row.code),
    ];
    for (const matches of preferences) {
      const hits = useful.filter(matches);
      if (hits.length > 0) return hits[hits.length - 1];
    }
    return useful[useful.length - 1] ?? rows[rows.length - 1] ?? null;
  });
  if (!pick) throw new Error("the diff has no added line to comment on");
  // The sentence has to be about the line it quotes, or the shot shows a review
  // comment that does not belong to the code next to it.
  let review = "这段先保留：等测试跑通再动它。";
  if (/assert\./.test(pick.code)) {
    review = "这条断言保留：用严格相等比较，别放宽成真值判断。";
  } else if (/settimeout|await |promise|sleep/i.test(pick.code)) {
    review = "3 秒太久了：这个等待会拖慢每次跑套件，缩到几十毫秒就够验证。";
  } else if (/function|return|=>/.test(pick.code)) {
    review = "保持布尔返回：这里不要抛错，false 就是答案。";
  }
  log(`  commenting on added line ${pick.line}: ${JSON.stringify(pick.code.slice(0, 60))}`);
  log(`  comment text: ${review}`);

  const target = page.locator(`[data-testid="diff-line-row"][data-new-line="${pick.line}"]`).first();
  await target.hover();
  await target.getByTestId("diff-line-comment").click();
  const editor = page.getByTestId("review-comment-editor");
  await editor.waitFor({ state: "visible", timeout: 15_000 });
  const quoted = await page.getByTestId("review-comment-quote").innerText().catch(() => "");
  log(`  quoted code: ${JSON.stringify(quoted.replace(/\s+/g, " ").trim().slice(0, 80))}`);
  await page.getByTestId("review-comment-input").fill(review);
  await page.getByTestId("review-comment-send").click();

  // The transcript is fed by the agent's own event stream, so this waits for the
  // composed message — header, quote, comment and the 位置 note — to arrive.
  await page.waitForFunction(
    () => {
      const turns = Array.from(document.querySelectorAll('[data-testid="turn-user"]'));
      const last = turns.at(-1);
      const text = last?.innerText ?? "";
      return text.includes("行内评论") && text.includes("评论：") && text.includes("位置：");
    },
    undefined,
    { timeout: 45_000, polling: 150 },
  );
  log("  review comment reached the transcript");
  await page.waitForTimeout(250);
  await shot(page, "07-inline-comment", { settle: 200 });

  // Stop/continue, exercised where it costs no shot: the run the comment started
  // is still in flight, so 停止 ends it. Nothing below depends on this transcript
  // — it is the same command a user presses, and the session returns to idle so
  // the project can be deleted under a quiet agent.
  if ((await runState()) === "running") {
    await page.getByTestId("composer-stop").click({ timeout: 5_000 }).catch(() => {});
    log("  pressed 停止 on the in-flight continuation");
    await waitRunState("settled", 120_000);
    log(
      `  stop recorded: ${await page.getByTestId("run-interrupted").count()} run-interrupted marker(s), ` +
        `composer idle = ${await page.getByTestId("composer-input").isEditable()}`,
    );
  } else {
    log("  the continuation had already settled; the stop control was shown in 02");
  }

  // Let anything still running finish so the delete is not under a live agent.
  try {
    await waitRunState("settled", 2 * 60_000);
  } catch {
    log("warning: the session did not settle before cleanup");
  }

  // ---------------------------------------------------------------- admin
  const adminContext = await browser.newContext({
    viewport: VIEWPORT,
    deviceScaleFactor: SCALE,
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
  });
  const adminPage = await adminContext.newPage();
  await login(adminPage, ADMIN);

  await adminPage.goto(`${BASE}/admin/status`, { waitUntil: "domcontentloaded" });
  await hideTransientUI(adminPage);
  await adminPage.getByTestId("admin-status").waitFor({ state: "visible", timeout: 30_000 });
  await adminPage.locator('[data-testid="stat-storage"]').waitFor({ state: "visible", timeout: 30_000 });
  await adminPage.locator('[data-testid^="stat-storage-"]').first().waitFor({ state: "visible", timeout: 30_000 });
  await adminPage.getByTestId("stat-storage-quotas").waitFor({ state: "visible", timeout: 30_000 });
  // The tenant/egress card is newer than this deployment's web build: wait for it
  // only when it exists, and say so rather than failing the capture.
  const tenantCard = adminPage.getByTestId("stat-tenant-limits");
  const claimsTenantLimits = (await tenantCard.count()) > 0;
  if (claimsTenantLimits) {
    await tenantCard.waitFor({ state: "visible", timeout: 30_000 });
    log("  admin status: runtime directories + per-sandbox quota + tenant/egress limits");
  } else {
    log(
      "warning: this build has no 租户配额与网络 card (GET /v1/admin/status carries no tenant_limits); " +
        "09 shows the runtime-directory card and the per-sandbox quota only",
    );
  }
  await adminPage.waitForTimeout(600);
  const frameHeight = await fitFrameTo(adminPage, "admin-status");
  log(`  admin status frame: 1440x${frameHeight} (content does not fit 900 on this build)`);
  await shot(adminPage, "09-admin-status", { settle: 400 });

  // /admin/audit is newer than this deployment's web build (the route landed
  // after the image was built). Probe it instead of failing: on a deployment
  // built from this checkout the same code captures the page.
  const auditProbe = await adminPage.request.get(`${BASE}/admin/audit`);
  if (auditProbe.status() === 404) {
    log("SKIPPED 10-audit: this deployment's build has no /admin/audit route (HTTP 404)");
  } else {
    await adminPage.goto(`${BASE}/admin/audit`, { waitUntil: "domcontentloaded" });
    await hideTransientUI(adminPage);
    await adminPage.getByTestId("admin-audit").waitFor({ state: "visible", timeout: 30_000 });
    await adminPage.locator('[data-testid^="audit-preview-"]').first().waitFor({ state: "visible", timeout: 30_000 });
    await adminPage.waitForFunction(
      () => {
        const rows = Array.from(document.querySelectorAll('[data-testid^="audit-row-"]'));
        const withCommit = rows.filter((row) => /提交\s+[0-9a-f]{7,}/.test(row.innerText ?? ""));
        return rows.length >= 3 && withCommit.length >= 1;
      },
      undefined,
      { timeout: 30_000, polling: 200 },
    );
    log(`  audit rows on the page: ${await adminPage.locator('[data-testid^="audit-row-"]').count()}`);
    await shot(adminPage, "10-audit", { settle: 500 });
  }

  await adminContext.close();
} catch (error) {
  log("FAILED:", error instanceof Error ? error.stack ?? error.message : String(error));
  process.exitCode = 1;
} finally {
  try {
    if (page) await deleteProject(projectId);
  } catch (error) {
    log("cleanup error:", error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
  await browser.close();

  // ---------------------------------------------------------------- report
  const files = readdirSync(OUT)
    .filter((name) => /^\d\d-.+\.png$/.test(name))
    .sort();
  console.log("\n--- docs/screenshots/live ---");
  for (const name of files) {
    const known = shots.find((entry) => entry.name === `${path.parse(name).name}`);
    const bytes = statSync(path.join(OUT, name)).size;
    const dims = known ? `${known.width}x${known.height}` : "";
    console.log(`${name}${dims ? `  ${dims}` : ""}  ${(bytes / 1024).toFixed(0)} KiB`);
  }
  const host = os.hostname();
  const unique = new Set(shots.map((entry) => entry.name)).size;
  console.log(
    `\n${unique} distinct screenshot(s) written this run; host name is ${host} ` +
      `(the terminal shot must NOT show it).`,
  );
  console.log(`elapsed ${((Date.now() - started) / 1000).toFixed(1)}s`);
}
