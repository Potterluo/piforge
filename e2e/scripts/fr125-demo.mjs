// Clean end-to-end FR-12.5 demo on the deployed app (real provider).
//
// Two short agent turns produce the failing and the passing run; the terminal
// supplies a passing run, a running run, and an unreadable command. Every state
// the panel claims to support is captured from this one session, then reloaded.
import { chromium } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const BASE = "http://127.0.0.1:7722";
const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fr125");
mkdirSync(ARTIFACTS, { recursive: true });

const browser = await chromium.launch({ channel: "chrome", headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
page.setDefaultTimeout(60_000);
page.on("pageerror", (e) => console.log("[pageerror]", e.message));
const shot = (name) => page.screenshot({ path: path.join(ARTIFACTS, name) });

let sessionId = "";
let projectId = "";

async function api(method, route, data) {
  const cookies = await context.cookies();
  const csrf = cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
  const res = await context.request.fetch(`${BASE}${route}`, {
    method,
    headers: { "X-PF-CSRF": csrf },
    data,
  });
  if (!res.ok()) throw new Error(`${method} ${route} -> ${res.status()} ${await res.text()}`);
  return res.json();
}
const listRuns = async () => (await api("GET", `/v1/sessions/${sessionId}/test-runs`)).runs;

async function waitForRun(predicate, label, timeout = 240_000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    const hit = (await listRuns()).find(predicate);
    if (hit) return hit;
    await page.waitForTimeout(800);
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function waitForNewRun(ids, predicate, label, timeout = 120_000) {
  return waitForRun(
    (run) => !ids.has(run.id) && predicate(run),
    label,
    timeout,
  );
}

async function ask(text, label) {
  const before = await page.getByTestId("turn-user").count();
  await page.getByTestId("composer-input").fill(text);
  await page.getByTestId("composer-send").click();
  await page.waitForFunction(
    (count) => document.querySelectorAll('[data-testid="turn-user"]').length > count,
    before,
    { timeout: 30_000 },
  );
  try {
    await page.getByTestId("composer-stop").waitFor({ timeout: 20_000 });
    await page.getByTestId("composer-stop").waitFor({ state: "detached", timeout: 300_000 });
  } catch {
    console.log(`[${label}] running state not observed`);
  }
  await page.waitForFunction(
    () => document.querySelector('[data-testid="run-state"]')?.dataset.state === "settled",
    null,
    { timeout: 300_000 },
  );
  console.log(`[${label}] settled`);
}

async function typeInTerminal(command) {
  await page.getByTestId("panel-tab-terminal").click();
  await page.getByTestId("terminal-pane").waitFor();
  await page.waitForFunction(
    () => document.querySelector('[data-testid="terminal-pane"]')?.dataset.ptyStatus === "open",
    null,
    { timeout: 30_000 },
  );
  await page.locator('[data-testid="terminal-pane"] .xterm-helper-textarea').click();
  await page.keyboard.type(command, { delay: 20 });
  await page.keyboard.press("Enter");
}

async function openTests() {
  await page.getByTestId("panel-tab-tests").click();
  await page.getByTestId("test-run").first().waitFor();
}

// ---- setup ---------------------------------------------------------------
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor();

projectId = (
  await api("POST", "/v1/projects", {
    name: `fr125-final-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  })
).project.id;
sessionId = (await api("POST", `/v1/projects/${projectId}/sessions`, { title: "FR-12.5 demo" })).session.id;
console.log("[ids]", { projectId, sessionId });

await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
await page.locator(`[data-session-id="${sessionId}"]`).first().waitFor({ timeout: 60_000 });

// ---- empty ---------------------------------------------------------------
await page.getByTestId("panel-tab-tests").click();
await page.getByTestId("test-empty").waitFor();
await shot("deployed-empty.png");
console.log("[empty] captured");

// ---- turn 1: the agent writes a failing test and runs it ----------------
await ask(
  "In the current workspace, create a Go module: go.mod (module calc), calc.go with func Add(a, b int) int, " +
    "and calc_test.go with TestAddPass expecting Add(1,2)==3 and TestAddFail expecting Add(1,2)==4. " +
    "Then run `go test ./...` and show the failing output. Do not fix the failing test yet.",
  "turn-fail",
);
const failed = await waitForRun((run) => run.status === "failed", "first-red", 60_000);
console.log("[failed]", failed.command, failed.failures);

await openTests();
await page.waitForFunction(
  () => document.querySelector('[data-testid="test-tab-badge"]')?.dataset.state === "fail",
  null,
  { timeout: 15_000 },
);
const failedRow = page.locator('[data-testid="test-run"][data-status="failed"]').first();
await failedRow.getByRole("button").first().click();
await page.getByTestId("test-failure").first().waitFor();
await shot("deployed-failure.png");
console.log("[failure]", await page.getByTestId("test-failure").first().innerText());

const location = page.getByTestId("test-failure-location").first();
const locationText = await location.innerText();
await location.click();
await page.getByTestId("file-preview").waitFor();
await page.waitForTimeout(800);
console.log("[location]", locationText, "→ active line", await page.getByTestId("code-preview").getAttribute("data-active-line"));
await shot("deployed-file-line.png");

// ---- a terminal passing run ---------------------------------------------
let ids = new Set((await listRuns()).map((run) => run.id));
await typeInTerminal("go test -run TestAddPass -v ./...");
const terminalPass = await waitForNewRun(ids, (run) => run.status === "passed", "terminal-pass");
console.log("[terminal pass]", terminalPass.passed, "passed");
await openTests();
await page.waitForFunction(
  () => document.querySelector('[data-testid="test-tab-badge"]')?.dataset.state === "pass",
  null,
  { timeout: 15_000 },
);
await shot("deployed-terminal.png");

// ---- a running terminal run ---------------------------------------------
ids = new Set((await listRuns()).map((run) => run.id));
await typeInTerminal("sleep 6 && go test ./...");
const running = await waitForNewRun(ids, (run) => run.status === "running", "running");
await openTests();
await page.locator('[data-testid="test-run"][data-status="running"]').first().waitFor();
await shot("deployed-running.png");
console.log("[running]", running.command);
const settled = await waitForRun(
  (run) => run.id === running.id && run.status !== "running",
  "running-settled",
);
console.log("[running settled]", settled.status);

// ---- turn 2: the agent fixes it and the suite passes --------------------
const idsBeforeFix = new Set((await listRuns()).map((run) => run.id));
await ask(
  "Fix the failing test so the suite passes (assert Add(1,2)==3 there too), then run `go test ./...` again and show the result.",
  "turn-pass",
);
const latestPass = await waitForNewRun(idsBeforeFix, (run) => run.status === "passed", "agent-pass", 60_000);
console.log("[agent pass]", latestPass.command);
await openTests();
await page.waitForFunction(
  () => document.querySelector('[data-testid="test-tab-badge"]')?.dataset.state === "pass",
  null,
  { timeout: 15_000 },
);
await shot("deployed-pass.png");
console.log("[pass] captured");

// ---- a terminal command whose output cannot be read ---------------------
ids = new Set((await listRuns()).map((run) => run.id));
await typeInTerminal("go test ./does-not-exist");
const bad = await waitForNewRun(ids, (run) => run.status === "unrecognized", "unrecognized", 60_000);
console.log("[unrecognized]", bad.command, bad.output_tail?.slice(0, 100));
await openTests();
const badRow = page.locator('[data-testid="test-run"][data-status="unrecognized"]').first();
await badRow.getByRole("button").first().click();
await page.getByTestId("test-raw-tail").waitFor();
await shot("deployed-unrecognized.png");

// ---- reload --------------------------------------------------------------
const beforeReload = await listRuns();
await page.reload();
await page.locator(`[data-session-id="${sessionId}"]`).first().waitFor({ timeout: 60_000 });
await openTests();
await page.waitForTimeout(2000);
const rows = await page.getByTestId("test-run").count();
const afterReload = await listRuns();
await shot("deployed-after-reload.png");
console.log("[reload]", { rows, api: afterReload.length, before: beforeReload.length });
if (rows !== afterReload.length) throw new Error("reload lost rows");
console.log("[done]", afterReload.map((r) => `${r.status}/${r.source}/${r.command}`));
await browser.close();
