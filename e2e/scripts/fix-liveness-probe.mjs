// Verification probe for the liveness fix, run against the deployed app:
//
//   node e2e/scripts/fix-liveness-probe.mjs live-local
//
// live-local  the deployment is -sandbox local, so the live run is a child of
//             the control plane and the in-process registry must keep it. The
//             probe starts a real run, holds it across two watchdog intervals
//             (default 30s each), screenshots it mid-run, and then proves the
//             watchdog wrote nothing: session still running, run still started,
//             no recovery events, and no "interrupted an orphaned run" line in
//             the container log.
//
// The remote half (a restart with -sandbox runner against a fake runner that
// reports a live session) is e2e/scripts/fix-liveness-restart.mjs, which runs
// the real old and new binaries; the durable Go version is
// backend/cmd/piforged/session_liveness_test.go.
//
// Everything is read-only except the project/session this probe creates for
// itself, the interrupt that ends its own run, and the delete that removes it.
import { chromium, request } from "@playwright/test";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-liveness");
mkdirSync(out, { recursive: true });

const PHASE = process.argv[2] ?? "live-local";
const BASE = (process.argv[3] ?? process.env.BASE ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const CONTAINER = process.env.PIFORGE_CONTAINER ?? "pf-local";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const WATCHDOG_MS = 30_000;
const HOLD_MS = Number(process.env.LIVENESS_HOLD_MS ?? 75_000);
const record = { base: BASE, container: CONTAINER, phase: PHASE, at: new Date().toISOString(), checks: {}, findings: [] };
const save = () => writeFileSync(path.join(out, `live-${PHASE}.json`), JSON.stringify(record, null, 2));
const note = (key, value) => {
  record.checks[key] = value;
  console.log(`[${key}]`, JSON.stringify(value));
};
const ok = (condition, message) => {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  record.findings.push({ ok: Boolean(condition), message });
  return Boolean(condition);
};

const DB_SCRIPT = `
import json, sqlite3, sys
con = sqlite3.connect('file:/data/piforge.db?mode=ro', uri=True)
sid = sys.argv[1]
out = {}
session = con.execute('select id, status from sessions where id = ?', (sid,)).fetchone()
out['session'] = {'id': session[0], 'status': session[1]} if session else None
out['runs'] = [
    {'id': r[0], 'status': r[1], 'interrupted_by': r[2], 'completion_note': r[3]}
    for r in con.execute('select id, status, interrupted_by, completion_note from runs where session_id = ? order by started_at', (sid,))
]
out['events'] = [
    {'type': r[0], 'payload': r[1]}
    for r in con.execute("select type, payload from events where session_id = ? and type in ('run.settled','platform.error') order by seq", (sid,))
]
print(json.dumps(out, ensure_ascii=False))
`;

function dbState(sessionId) {
  const raw = execFileSync("docker", ["exec", "-i", CONTAINER, "python3", "-", sessionId], {
    input: DB_SCRIPT,
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
  }).trim();
  return JSON.parse(raw);
}

function containerLogSince(windowSeconds) {
  const result = spawnSync("docker", ["logs", "--since", `${windowSeconds}s`, CONTAINER], { encoding: "utf8" });
  return `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function main() {
  if (PHASE !== "live-local") throw new Error(`unknown phase ${PHASE}`);
  const ctx = await request.newContext({ baseURL: BASE });
  const res = await ctx.post("/v1/auth/login", { data: USER });
  if (!res.ok()) throw new Error(`login: HTTP ${res.status()}`);
  const state = await ctx.storageState();
  const csrf = state.cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
  const headers = { "X-PF-CSRF": csrf };
  note("csrf_present", Boolean(csrf));

  // A project and a session of the probe's own: the deployment's existing rows
  // are not touched.
  const stamp = Date.now();
  const projectRes = await ctx.post("/v1/projects", {
    headers,
    data: { name: `probe-liveness-${stamp}`, repo_url: "", default_branch: "main" },
  });
  if (!projectRes.ok()) throw new Error(`create project: HTTP ${projectRes.status()} ${await projectRes.text()}`);
  const projectId = (await projectRes.json()).project.id;
  const sessionRes = await ctx.post(`/v1/projects/${projectId}/sessions`, {
    headers,
    data: { title: "watchdog keeps a live run" },
  });
  if (!sessionRes.ok()) throw new Error(`create session: HTTP ${sessionRes.status()}`);
  const sessionId = (await sessionRes.json()).session.id;
  note("project", { id: projectId });
  note("session", { id: sessionId });

  // A run long enough to span two watchdog intervals. The agent is asked to
  // block in a foreground bash call, so the run is genuinely in flight; a
  // model that settles early makes this probe fail rather than pass quietly.
  const send = await ctx.post(`/v1/sessions/${sessionId}/messages`, {
    headers,
    data: {
      client_msg_id: `cm_${stamp}`,
      text: "Use the bash tool to run this exact foreground command, blocking until it finishes: sleep 120\nThen reply with just: DONE. Do not run it in the background.",
    },
  });
  note("send", { status: send.status(), body: (await send.text()).slice(0, 200) });
  if (!send.ok()) throw new Error("the prompt was refused");

  const started = Date.now();
  let running = false;
  for (let i = 0; i < 60; i++) {
    const statusRes = await ctx.get(`/v1/sessions/${sessionId}`);
    const body = await statusRes.json();
    running = body.session?.status === "running" || body.status === "running";
    if (running) break;
    await sleep(500);
  }
  ok(running, "the run started (session status is running)");
  if (!running) throw new Error("the run never started");

  // Two watchdog intervals plus a margin, with the page open so the screenshot
  // is taken while the stop control is still on screen.
  const samples = [];
  const deadline = Date.now() + HOLD_MS;
  let shot = false;
  let midRunText = "";
  const browser = await chromium.launch({ channel: "chrome" });
  const page = await (await browser.newContext({ viewport: { width: 1440, height: 1000 } })).newPage();
  try {
    await page.goto(`${BASE}/login`);
    await page.getByTestId("login-email").fill(USER.email);
    await page.getByTestId("login-password").fill(USER.password);
    await page.getByTestId("login-submit").click();
    await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
    await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
    await page.getByTestId("session-stream").waitFor({ timeout: 30_000 });
    while (Date.now() < deadline) {
      await sleep(5_000);
      const statusRes = await ctx.get(`/v1/sessions/${sessionId}`);
      const body = await statusRes.json();
      samples.push({ at: Date.now() - started, status: body.session?.status ?? body.status });
      if (!shot && Date.now() - started >= 40_000) {
        shot = true;
        await page.screenshot({ path: path.join(out, "live-run-mid-watchdog.png"), fullPage: true });
        midRunText = (await page.locator("body").innerText()).replace(/\s+/g, " ");
      }
    }
  } finally {
    await browser.close();
  }
  note("samples", samples);
  note("hold_ms", HOLD_MS);
  note("watchdog_intervals_covered", Math.floor((Date.now() - started) / WATCHDOG_MS));
  note("ui_mid_run_has_stop", /停止|运行中/.test(midRunText));
  ok(shot, "a screenshot of the run was taken while it was in flight");
  ok(/停止|运行中/.test(midRunText), "the page showed the run as running mid-watchdog");

  const stillRunning = samples.at(-1)?.status === "running";
  ok(stillRunning, "the live run is still running after two watchdog intervals");

  const before = dbState(sessionId);
  note("db", before);
  ok(before.session?.status === "running", "the database row is still running (the watchdog wrote nothing)");
  ok(
    before.runs.every((r) => r.status === "started" && r.interrupted_by === ""),
    "no started run was settled by the watchdog",
  );
  ok(before.events.length === 0, "no run.settled / platform.error recovery event was appended");

  const logs = containerLogSince(Math.ceil((Date.now() - started) / 1000) + 10);
  const orphanLines = logs.split("\n").filter((line) => /interrupted an orphaned run/.test(line));
  note("orphan_lines", orphanLines);
  ok(orphanLines.length === 0, "the container log has no 'interrupted an orphaned run' line for this window");
  const reconcileLines = logs.split("\n").filter((line) => /session reconciliation/.test(line));
  note("reconcile_lines", reconcileLines);
  ok(
    reconcileLines.every((line) => !/interrupted=[1-9]/.test(line)),
    "no reconciliation summary reports an interruption",
  );

  // End the probe's own run, then remove the project it created. The evidence
  // above is already on disk.
  const interrupt = await ctx.post(`/v1/sessions/${sessionId}/interrupt`, { headers, data: { clear_queue: true } });
  note("interrupt", { status: interrupt.status() });
  for (let i = 0; i < 40; i++) {
    const statusRes = await ctx.get(`/v1/sessions/${sessionId}`);
    const body = await statusRes.json();
    if ((body.session?.status ?? body.status) !== "running") break;
    await sleep(500);
  }
  const after = dbState(sessionId);
  note("db_after_interrupt", after);
  ok(after.session?.status !== "running", "the probe's own run was interrupted afterwards (cleanup)");

  const removed = await ctx.delete(`/v1/projects/${projectId}`, { headers });
  note("project_delete", { status: removed.status() });
  ok(removed.ok(), "the probe removed the project it created");

  save();
  process.exitCode = record.findings.every((f) => f.ok) ? 0 : 1;
}

try {
  await main();
} catch (error) {
  record.error = String(error?.stack ?? error);
  console.error("[probe] failed:", error);
  save();
  process.exitCode = 1;
}
