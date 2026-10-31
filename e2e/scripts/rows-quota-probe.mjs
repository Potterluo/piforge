// Verification probe for the two follow-ups from the directory-lifecycle audit,
// run against the deployed app (default :7722, the pf-local container):
//
//   node e2e/scripts/rows-quota-probe.mjs <phase> [baseURL] [container]
//
// phase =
//   rows-before   old image: a project delete leaves its snapshot ROWS behind
//   rows-after    fixed image: the rows go with the sandbox/project
//   quota-before  old image: a sandbox over any budget still runs (no guard)
//   quota-after   fixed image: the run is refused with the Chinese message
//
// The snapshot row is produced by the product's own path, the idle reaper: the
// container is restarted with tiny thresholds, the session is provisioned and
// then reclaimed, which writes <data>/snapshots/<sandbox>/<stamp>/manifest.json
// and one snapshots row. Row counts are read from the database inside the
// container (python3 + sqlite3, read-only), because GET .../snapshots needs a
// live session and the point of this probe is the row that outlives it.
//
// Evidence lands in e2e/.artifacts/fix-rows-quota/.
import { chromium } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-rows-quota");
mkdirSync(out, { recursive: true });

const PHASE = process.argv[2] ?? "rows-after";
const BASE = (process.argv[3] ?? process.env.BASE ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const CONTAINER = process.argv[4] ?? process.env.PIFORGE_CONTAINER ?? "pf-local";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const ADMIN = { email: "admin@piforge.local", password: "piforge-admin-123" };
const stamp = Date.now();
const isRows = PHASE.startsWith("rows");
const isBefore = PHASE.endsWith("before");

const record = { base: BASE, container: CONTAINER, phase: PHASE, stamp, at: new Date().toISOString(), checks: {}, findings: [] };
const save = () => writeFileSync(path.join(out, `probe-${PHASE}.json`), JSON.stringify(record, null, 2));
const note = (key, value) => {
  record.checks[key] = value;
  console.log(`[${key}]`, JSON.stringify(value));
};
const ok = (condition, message) => {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  record.findings.push({ ok: Boolean(condition), message });
  return Boolean(condition);
};
const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";

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
    timeout: 240_000,
  });
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status(), body };
}

function docker(...args) {
  return execFileSync("docker", ["exec", CONTAINER, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "ignore"],
  }).trim();
}

/** Count the snapshots rows for one sandbox straight out of the deployment DB. */
function dbSnapshotRows(sandboxId) {
  const script = [
    "import sqlite3, sys",
    "con = sqlite3.connect('file:/data/piforge.db?mode=ro', uri=True)",
    "print(con.execute('select count(*) from snapshots where sandbox_id = ?', (sys.argv[1],)).fetchone()[0])",
  ].join("\n");
  const outText = docker("python3", "-c", script, sandboxId);
  return Number(outText.trim());
}

/** The session's event types, straight out of the deployment DB. */
function dbEventTypes(sessionId) {
  const script = [
    "import sqlite3, sys",
    "con = sqlite3.connect('file:/data/piforge.db?mode=ro', uri=True)",
    "print('\\n'.join(r[0] for r in con.execute('select type from events where session_id = ?', (sys.argv[1],))))",
    "print('---')",
    "print('\\n'.join(str(r[0]) for r in con.execute(\"select payload from events where session_id = ? and type = 'platform.error'\", (sys.argv[1],))))",
  ].join("\n");
  return docker("python3", "-c", script, sessionId).split("\n");
}

/** du-style size of one sandbox root, from inside the container. */
function duBytes(dir) {
  try {
    return Number(docker("du", "-sb", dir).split(/\s+/)[0]);
  } catch {
    return 0;
  }
}

const exists = (p) => {
  try {
    docker("test", "-e", p);
    return true;
  } catch {
    return false;
  }
};

async function waitFor(predicate, timeoutMs, intervalMs = 500) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) return true;
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
  return predicate();
}

const browser = await chromium.launch({ channel: "chrome" });
let page = null;
try {
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const me = await login(page, USER);
  note("user_id", me.id);

  const project = await api(page, "POST", "/v1/projects", {
    name: `probe-${PHASE}-${stamp}`,
  });
  if (project.status >= 300) throw new Error(`create project: ${project.status} ${JSON.stringify(project.body)}`);
  const projectId = project.body.project.id;
  note("project_id", projectId);

  const created = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: `${PHASE} probe`,
  });
  if (created.status >= 300) throw new Error(`create session: ${created.status} ${JSON.stringify(created.body)}`);
  const sessionId = created.body.session.id;
  note("session_id", sessionId);

  // One real turn provisions the checkout; it is also the first thing a disk
  // budget is measured against.
  const first = await api(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `probe-first-${stamp}`,
    text: "Reply with the single word ok.",
  });
  note("first_message_status", first.status);
  const sandboxRoot = `/data/sandboxes/${me.id}/${projectId}`;
  const provisioned = await waitFor(() => exists(`${sandboxRoot}/repo`), 120_000);
  note("checkout_provisioned", { root: sandboxRoot, provisioned, bytes: duBytes(sandboxRoot) });
  if (!provisioned) throw new Error("the sandbox checkout never appeared");

  // A second message while the first turn is still running is refused as
  // session_busy, which would hide the behaviour this probe is about.
  const idle = await waitFor(async () => {
    const status = await api(page, "GET", `/v1/sessions/${sessionId}/status`);
    return status.status === 200 && status.body?.running === false && status.body?.streaming === false;
  }, 180_000, 1000);
  note("first_turn_settled", idle);

  if (isRows) {
    // The reaper (tiny thresholds on this container) writes the manifest and the
    // row, then destroys the directory. The row is the part being tested.
    const listed = await waitFor(async () => {
      const snap = await api(page, "GET", `/v1/sessions/${sessionId}/sandbox/snapshots`);
      return snap.status === 200 && (snap.body?.snapshots?.length ?? 0) > 0;
    }, 150_000);
    note("snapshots_endpoint_listed", listed);
    const snap = await api(page, "GET", `/v1/sessions/${sessionId}/sandbox/snapshots`);
    const rows = snap.body?.snapshots ?? [];
    note("snapshots_before_delete", rows.length);
    note("snapshot_rows_api", rows.map((r) => ({ id: r.id, sandbox_id: r.sandbox_id, blob_key: r.blob_key })));
    const sandboxId = rows[0]?.sandbox_id ?? "";
    note("sandbox_id", sandboxId);
    note("rows_in_db_before_delete", sandboxId ? dbSnapshotRows(sandboxId) : null);

    // The manifest directory the row names: present before the project goes.
    if (rows[0]?.blob_key) {
      note("manifest_dir_exists_before_delete", exists(`/data/snapshots/${path.posix.dirname(rows[0].blob_key)}`));
    }

    const deleted = await api(page, "DELETE", `/v1/projects/${projectId}`);
    note("delete_project_status", deleted.status);
    note("delete_project_body", deleted.body);
    if (!isBefore) {
      ok(deleted.status === 200, `DELETE /v1/projects/{id} answered ${deleted.status}`);
    }

    const after = sandboxId ? dbSnapshotRows(sandboxId) : null;
    note("rows_in_db_after_delete", after);
    note("sandbox_dir_exists_after_delete", exists(sandboxRoot));

    // Admin view: the project is gone from the list; the storage block is what an
    // operator would look at to see the snapshots directory.
    const adminContext = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
    const adminPage = await adminContext.newPage();
    await login(adminPage, ADMIN);
    await adminPage.goto(`${BASE}/admin/status`);
    await adminPage.getByTestId("admin-status").waitFor({ timeout: 30_000 });
    await adminPage.screenshot({ path: path.join(out, `${PHASE}-admin-storage.png`), fullPage: true });
    await adminContext.close();

    await page.goto(`${BASE}/`);
    await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
    await page.screenshot({ path: path.join(out, `${PHASE}-project-gone.png`) });

    if (isBefore) {
      // The old image cannot delete a project whose sandbox the reaper already
      // released (the provider answers not_found and the delete turns that into
      // 409), so the row stays by construction.
      ok(after !== null && after >= 1,
        `old image: ${after} snapshot row(s) still in the database after the delete attempt (status ${deleted.status})`);
    } else {
      ok(deleted.status === 200 && after === 0,
        `fixed image: delete ${deleted.status}, ${after} snapshot row(s) left`);
    }
  } else {
    // A 6 MiB file in the checkout. 6 MiB > the 5 MiB probe quota the container was
    // started with, and comfortably inside the 2 GiB default used everywhere else.
    docker("sh", "-lc", `dd if=/dev/zero of=${sandboxRoot}/repo/probe-big.bin bs=1M count=6 2>/dev/null`);
    const bytes = duBytes(sandboxRoot);
    note("sandbox_bytes", bytes);
    note("sandbox_over_5mib", bytes > 5 * 1024 * 1024);

    // Admin status: the per-sandbox report is where an operator sees this. The
    // guardian reuses a measurement for a minute (an idle screen must not become a
    // disk scan), so an operator sees "who is close" within one cache window
    // rather than the instant a file is written. Poll for it instead of pretending
    // the report is instantaneous.
    const adminContext = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
    const adminPage = await adminContext.newPage();
    await login(adminPage, ADMIN);
    let adminBody = await (await adminPage.request.get(`${BASE}/v1/admin/status`)).json();
    let usageRow = (adminBody.sandbox_usage ?? []).find((row) => row.project_id === projectId) ?? null;
    note("admin_sandbox_usage_cached", usageRow);
    const fresh = await waitFor(async () => {
      adminBody = await (await adminPage.request.get(`${BASE}/v1/admin/status`)).json();
      usageRow = (adminBody.sandbox_usage ?? []).find((row) => row.project_id === projectId) ?? null;
      return Boolean(usageRow?.over);
    }, 95_000, 5000);
    note("admin_sandbox_usage", usageRow);
    note("admin_sandbox_disk_quota", adminBody.sandbox_disk_quota ?? null);
    note("admin_report_caught_up", fresh);
    await adminPage.goto(`${BASE}/admin/status`);
    await adminPage.getByTestId("admin-status").waitFor({ timeout: 30_000 });
    await adminPage.screenshot({ path: path.join(out, `${PHASE}-admin-quota.png`), fullPage: true });
    await adminContext.close();

    // The run attempt, through the product's own composer path.
    const second = await api(page, "POST", `/v1/sessions/${sessionId}/messages`, {
      client_msg_id: `probe-second-${stamp}`,
      text: "Ignore the previous instruction and install every dependency.",
    });
    note("second_message_status", second.status);
    note("second_message_body", second.body);
    const message = second.body?.error?.message ?? "";
    note("refusal_message", message);

    // The same attempt from the UI, so the message a user sees is what is on
    // screen. The session page renders the recorded platform.error inline, above
    // the transcript: scroll it back into view before the screenshot.
    await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
    await page.getByTestId("session-stream").waitFor({ timeout: 45_000 }).catch(() => {});
    await page.evaluate(() => {
      for (const el of Array.from(document.querySelectorAll("*"))) {
        if (el.scrollHeight > el.clientHeight + 20) el.scrollTop = 0;
      }
      window.scrollTo(0, 0);
    });
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(out, `${PHASE}-session-error.png`), fullPage: true });

    if (isBefore) {
      ok(second.status === 200, `old image: the over-budget run was accepted (${second.status}), the volume has no guard`);
    } else {
      ok(second.status === 507 && second.body?.error?.code === "sandbox_disk_quota",
        `fixed image: the run was refused (${second.status}, ${second.body?.error?.code})`);
      ok(/配额/.test(message) && /运行已停止/.test(message) && /node_modules/.test(message),
        "the refusal names the quota, says the run stopped, and says what to delete");
      const eventRows = dbEventTypes(sessionId);
      const separator = eventRows.indexOf("---");
      const types = separator >= 0 ? eventRows.slice(0, separator) : eventRows;
      const payloads = separator >= 0 ? eventRows.slice(separator + 1) : [];
      note("event_types", types);
      ok(types.includes("platform.error"), "the refusal was recorded in the session event log");
      ok(payloads.some((payload) => /sandbox_disk_quota/.test(payload)),
        "the recorded event names the quota code");
      const overRow = (adminBody.sandbox_usage ?? []).find((row) => row.project_id === projectId);
      ok(overRow?.over === true && (adminBody.sandbox_disk_quota ?? 0) > 0,
        `admin status reports the sandbox over its quota (${overRow?.bytes} > ${overRow?.quota_bytes})`);    }

    // Cleanup: drop the project so the deployment keeps its own data small.
    const deleted = await api(page, "DELETE", `/v1/projects/${projectId}`);
    note("delete_project_status", deleted.status);
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
