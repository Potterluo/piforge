// Verification probe for restart reconciliation, run against the deployed app
// (default :7722, the pf-local container):
//
//   node e2e/scripts/fix-reconcile-probe.mjs before
//   # rebuild the image, restart pf-local on the same volume
//   node e2e/scripts/fix-reconcile-probe.mjs after
//
// before  old image: the project that was running when its process died refuses
//         to delete (409) and its session row still says running.
// after   new image: boot reconciliation already flipped the row to interrupted,
//         the transcript carries the reason, and the same delete succeeds.
//
// The probe reads the database through `docker exec ... python3` with
// `mode=ro`; it never writes it. Evidence lands in
// e2e/.artifacts/fix-reconcile/.
import { chromium } from "@playwright/test";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-reconcile");
mkdirSync(out, { recursive: true });

const PHASE = process.argv[2] ?? "before";
const BASE = (process.argv[3] ?? process.env.BASE ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const CONTAINER = process.argv[4] ?? process.env.PIFORGE_CONTAINER ?? "pf-local";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const NAME_PREFIX = "probe-quota-before-";
const record = { base: BASE, container: CONTAINER, phase: PHASE, at: new Date().toISOString(), checks: {}, findings: [] };
const save = () => writeFileSync(path.join(out, `stuck-${PHASE}.json`), JSON.stringify(record, null, 2));
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
pid = sys.argv[1]
out = {}
project = con.execute('select id, name from projects where id = ?', (pid,)).fetchone()
out['project'] = {'id': project[0], 'name': project[1]} if project else None
out['sessions'] = [
    {'id': r[0], 'status': r[1], 'title': r[2]}
    for r in con.execute('select id, status, title from sessions where project_id = ?', (pid,))
]
out['runs'] = [
    {'id': r[0], 'session_id': r[1], 'status': r[2], 'interrupted_by': r[3], 'completion_note': r[4]}
    for r in con.execute(
        'select id, session_id, status, interrupted_by, completion_note from runs '
        'where session_id in (select id from sessions where project_id = ?) order by started_at', (pid,))
]
out['events'] = [
    {'type': r[0], 'payload': r[1]}
    for r in con.execute(
        'select type, payload from events where session_id in '
        '(select id from sessions where project_id = ?) and type in '
        "('run.settled', 'platform.error') order by seq desc limit 6", (pid,))
]
out['sandbox'] = [
    {'id': r[0], 'state': r[1]}
    for r in con.execute('select id, state from sandboxes where project_id = ?', (pid,))
]
print(json.dumps(out, ensure_ascii=False))
`;

function dbState(projectId) {
  const raw = execFileSync(
    "docker",
    ["exec", CONTAINER, "python3", "-c", DB_SCRIPT, projectId],
    { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
  ).trim();
  return JSON.parse(raw);
}

function saveStartupLog() {
  try {
    // slog writes to stderr, and `docker logs` keeps the two streams apart.
    const result = spawnSync("docker", ["logs", CONTAINER], { encoding: "utf8" });
    const logs = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
    const lines = logs
      .split("\n")
      .filter((line) => /session reconciliation|interrupted an orphaned run/.test(line));
    writeFileSync(path.join(out, "startup-reconcile.log"), lines.join("\n") + "\n");
    record.checks.startup_log_lines = lines;
    console.log("[startup]", lines.join("\n"));
  } catch (error) {
    record.checks.startup_log_error = String(error);
  }
}

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";

/**
 * The product facts the `after` stage must show. It is a function so `recheck`
 * can re-evaluate the captured record (the project is deleted by then and
 * cannot be re-read), without inventing a second database.
 */
function assertReconciled(state) {
  ok(
    state.sessions.every((s) => s.status === "interrupted"),
    "the stuck session was flipped to interrupted",
  );
  const stillStarted = state.runs.filter((r) => r.status === "started");
  ok(stillStarted.length === 0, "no run is left in the started state");
  const recon = state.runs.find((r) => r.interrupted_by === "reconcile");
  ok(
    Boolean(recon) &&
      recon.status === "interrupted" &&
      /服务重启/.test(recon.completion_note ?? ""),
    "the started run was settled as interrupted by the reconciler, with the reason",
  );
  // The older run in this project had settled normally before the crash. The
  // reconciler must not rewrite history it did not repair.
  const settledBefore = state.runs.filter((r) => r.status === "settled");
  ok(
    settledBefore.every((r) => r.interrupted_by === ""),
    "a run that had already settled was left untouched",
  );
  const settled = state.events.find((e) => e.type === "run.settled")?.payload ?? "";
  const platformError = state.events.find((e) => e.type === "platform.error")?.payload ?? "";
  note("settled_payload", settled);
  note("platform_error_payload", platformError);
  ok(/服务重启/.test(settled), "the run.settled event carries the restart reason");
  ok(/run_interrupted/.test(platformError), "the platform.error event names the interruption");
}

async function login(page, who) {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(who.email);
  await page.getByTestId("login-password").fill(who.password);
  const submit = page.getByTestId("login-submit");
  await submit.waitFor({ state: "visible", timeout: 30_000 });
  for (let i = 0; i < 40; i++) {
    if (await submit.isEnabled()) break;
    await page.waitForTimeout(250);
  }
  await submit.click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
  return (await (await page.request.get(`${BASE}/v1/auth/me`)).json()).user;
}

async function api(page, method, url, data) {
  const res = await page.request.fetch(`${BASE}${url}`, {
    method,
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    data,
    timeout: 120_000,
  });
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status(), body };
}

// The project was deleted by the `after` run, so a corrected assertion can only
// be re-evaluated over the facts that run recorded. It is deliberately a
// separate phase and writes its own file; it does not pretend to be a new run.
if (PHASE === "recheck") {
  const captured = JSON.parse(readFileSync(path.join(out, "stuck-after.json"), "utf8"));
  record.rechecked_at = captured.at;
  record.rechecked_phase = captured.phase;
  assertReconciled(captured.checks.db_before);
  writeFileSync(path.join(out, "stuck-after-recheck.json"), JSON.stringify(record, null, 2));
  process.exit(record.findings.every((f) => f.ok) ? 0 : 1);
}

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
try {
  await login(page, USER);
  const projects = (await (await page.request.get(`${BASE}/v1/projects`)).json()).projects ?? [];
  const target = projects.find((p) => String(p.name).startsWith(NAME_PREFIX));
  if (!target) throw new Error(`no project named ${NAME_PREFIX}… exists`);
  note("project", { id: target.id, name: target.name });

  const before = dbState(target.id);
  note("db_before", before);

  if (PHASE === "before") {
    ok(
      before.sessions.some((s) => s.status === "running"),
      "the pre-existing session still claims to be running",
    );
    ok(
      before.runs.some((r) => r.status === "started"),
      "its run row is still started",
    );
    ok(!before.events.some((e) => e.payload?.includes("服务重启")), "no reconciliation event exists yet");

    // The user path: open the project, confirm delete, read the refusal on screen.
    await page.goto(`${BASE}/p/${target.id}`);
    await page.getByTestId("project-actions").click();
    await page.getByTestId("project-delete").click();
    await page.getByTestId("project-delete-dialog").waitFor({ timeout: 20_000 });
    await page.screenshot({ path: path.join(out, "stuck-before-confirm.png") });
    await page.getByTestId("project-delete-confirm").click();
    const toast = page.locator("[data-sonner-toast]").filter({ hasText: "删除失败" });
    await toast.waitFor({ timeout: 20_000 });
    const toastText = (await toast.innerText()).replace(/\s+/g, " ").trim();
    note("ui_refusal", toastText);
    ok(/正在运行/.test(toastText), "the UI refuses with the running-session guard");
    await page.screenshot({ path: path.join(out, "stuck-before-delete-refused.png") });

    const direct = await api(page, "DELETE", `/v1/projects/${target.id}`);
    note("api_delete", direct);
    ok(direct.status === 409, `DELETE /v1/projects/{id} answers 409 (got ${direct.status})`);

    const after = dbState(target.id);
    note("db_after_refusal", after);
    ok(
      after.sessions.some((s) => s.status === "running"),
      "the database row is still running after the refusal: the lie is permanent on the old image",
    );
    ok(
      (await page.request.get(`${BASE}/v1/projects/${target.id}`)).status() === 200,
      "the project still exists",
    );
  } else {
    // The boot pass runs before the port opens, so by the time this probe can
    // log in the row must already have been repaired.
    saveStartupLog();
    assertReconciled(before);

    // The session UI shows the interrupted state rather than a run that never ends.
    const session = before.sessions[0];
    await page.goto(`${BASE}/p/${target.id}/s/${session.id}`);
    await page.getByTestId("session-stream").waitFor({ timeout: 30_000 }).catch(() => {});
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(out, "stuck-after-session-interrupted.png"), fullPage: true });

    // The user path that used to be impossible.
    await page.goto(`${BASE}/p/${target.id}`);
    await page.getByTestId("project-actions").click();
    await page.getByTestId("project-delete").click();
    await page.getByTestId("project-delete-dialog").waitFor({ timeout: 20_000 });
    await page.getByTestId("project-delete-confirm").click();
    await page.waitForURL(/\/$/, { timeout: 30_000 });
    await page.screenshot({ path: path.join(out, "stuck-after-deleted-home.png") });

    const gone = await page.request.get(`${BASE}/v1/projects/${target.id}`);
    note("project_status_after_delete", gone.status());
    ok(gone.status() === 404, "the project is gone through the UI delete");
    ok(
      (await (await page.request.get(`${BASE}/v1/projects`)).json()).projects.every(
        (p) => p.id !== target.id,
      ),
      "the project no longer appears in the list",
    );
    const dbAfter = dbState(target.id);
    note("db_after_delete", dbAfter);
    ok(dbAfter.project === null && dbAfter.sessions.length === 0, "the database rows are gone too");
  }
  save();
} catch (error) {
  record.error = String(error?.stack ?? error);
  console.error("[probe] failed:", error);
  save();
  process.exitCode = 1;
} finally {
  await browser.close();
}
