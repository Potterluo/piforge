// Verification probe for the runtime-directory lifecycle, run against the
// deployed app (default :7722, the pf-local container).
//
//   node e2e/scripts/dirlifecycle-probe.mjs [baseURL] [container] [phase]
//
// phase = "before" on the old image (expects leftovers), "after" on the fixed
// one (expects every directory to lose its entry within a few seconds). It
// drives the real HTTP API and asks the container for bytes on disk, because
// the bug is exactly "the row is gone but the directory is not".
//
// Evidence (screenshots, JSON, log) lands in e2e/.artifacts/fix-dirlifecycle/.
import { chromium } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-dirlifecycle");
mkdirSync(out, { recursive: true });

const BASE = (process.argv[2] ?? process.env.BASE ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const CONTAINER = process.argv[3] ?? process.env.PIFORGE_CONTAINER ?? "pf-local";
const PHASE = process.argv[4] ?? process.env.PHASE ?? "after";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const ADMIN = { email: "admin@piforge.local", password: "piforge-admin-123" };
const stamp = Date.now();
const DIRS = ["sandboxes", "pi-agents", "skill-snapshots", "snapshots", "artifacts", "skills"];

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
    timeout: 180_000,
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

/** One line per inventory directory: entries, recursive files, recursive bytes. */
function usage() {
  const script = DIRS.map(
    (d) =>
      `if [ -d /data/${d} ]; then echo "${d} $(ls -1 /data/${d} | wc -l) $(find /data/${d} -type f | wc -l) $(du -sb /data/${d} | cut -f1)"; else echo "${d} 0 0 0"; fi`,
  ).join("; ");
  const rows = {};
  for (const line of docker("sh", "-lc", script).split(/\r?\n/)) {
    const [name, entries, files, bytes] = line.trim().split(/\s+/);
    if (name) rows[name] = { entries: Number(entries), files: Number(files), bytes: Number(bytes) };
  }
  return rows;
}

const exists = (p) => {
  try {
    docker("test", "-e", p);
    return true;
  } catch {
    return false;
  }
};

async function waitFor(predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return true;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  return predicate();
}

const browser = await chromium.launch({ channel: "chrome" });
let page = null;
try {
  page = await browser.newPage({ viewport: { width: 1440, height: 950 } });
  const me = await login(page, USER);
  note("user_id", me.id);
  const before = usage();
  note("usage_before", before);

  const project = await api(page, "POST", "/v1/projects", {
    name: `probe-dirlifecycle-${PHASE}-${stamp}`,
  });
  if (project.status >= 300) throw new Error(`create project: ${project.status} ${JSON.stringify(project.body)}`);
  const projectId = project.body.project.id;
  note("project_id", projectId);

  const created = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: `dirs ${PHASE}` });
  if (created.status >= 300) throw new Error(`create session: ${created.status} ${JSON.stringify(created.body)}`);
  const sessionId = created.body.session.id;
  note("session_id", sessionId);

  // A real agent launch writes pi-agents/<session>/models.json + mcp.json. The
  // model call itself may fail on a key-less deployment; the directory is
  // created before the process starts, so that is enough for this probe.
  const sent = await api(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `probe-${stamp}`,
    text: "hello",
  });
  note("send_status", sent.status);
  const agentDir = `/data/pi-agents/${sessionId}`;
  const agentSeen = await waitFor(() => exists(agentDir), 30_000);
  ok(agentSeen, `the session created its agent directory (${agentDir})`);
  note("agent_dir_files", agentSeen ? docker("sh", "-lc", `find ${agentDir} -type f -printf '%f ' 2>/dev/null || true`) : "");

  // A screenshot writes artifacts/<session>/screenshots/<time>.png. Navigate the
  // sandbox browser to the app's own login page first, so the capture is not a
  // blank about:blank (which the server refuses on purpose).
  const tab = await api(page, "POST", `/v1/sessions/${sessionId}/browser/tabs`, {
    url: `${BASE}/login`,
  });
  note("tab_status", tab.status);
  const shot = await api(page, "POST", `/v1/sessions/${sessionId}/browser/screenshot`);
  note("screenshot_status", shot.status);
  const artifactDir = `/data/artifacts/${sessionId}`;
  const artifactSeen = await waitFor(() => exists(artifactDir), 30_000);
  ok(artifactSeen, `the screenshot created its artifacts directory (${artifactDir})`);
  note("artifact_files", artifactSeen ? docker("sh", "-lc", `find ${artifactDir} -type f`) : "");

  const withSession = usage();
  note("usage_with_session", withSession);

  // Admin status: after the fix this includes the storage block. A separate
  // browser context so signing in as the admin does not replace the dev cookie
  // jar the delete below needs.
  const adminContext = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
  const adminPage = await adminContext.newPage();
  await login(adminPage, ADMIN);
  const adminStatus = await adminPage.request.get(`${BASE}/v1/admin/status`);
  const adminBody = await adminStatus.json();
  note("admin_storage", adminBody.storage ?? null);
  await adminPage.goto(`${BASE}/admin/status`);
  await adminPage.getByTestId("admin-status").waitFor({ timeout: 30_000 });
  await adminPage.screenshot({ path: path.join(out, `${PHASE}-admin-status.png`), fullPage: true });
  const storageVisible = await adminPage.getByTestId("stat-storage").isVisible().catch(() => false);
  note("admin_storage_block_visible", storageVisible);
  await adminContext.close();

  // Delete the session and watch the directories it owns.
  const deleted = await api(page, "DELETE", `/v1/sessions/${sessionId}`);
  note("delete_session_status", deleted.status);
  const deadline = 10_000;
  const settled = await waitFor(
    () => !exists(agentDir) && !exists(artifactDir),
    deadline,
  );
  note("session_dirs_gone_within_10s", {
    agent: !exists(agentDir),
    artifacts: !exists(artifactDir),
    settled,
  });
  const afterSession = usage();
  note("usage_after_session_delete", afterSession);
  await page.screenshot({ path: path.join(out, `${PHASE}-session-deleted.png`) });

  // Project delete exists too; the session is already gone, so this is mostly
  // cleanup plus a check that the cascade is idempotent.
  const projectDeleted = await api(page, "DELETE", `/v1/projects/${projectId}`);
  note("delete_project_status", projectDeleted.status);
  const final = usage();
  note("usage_final", final);
  save();

  if (PHASE === "before") {
    // Fail-first: the old image leaves both directories behind.
    ok(!exists(agentDir) && !exists(artifactDir), "old image: directories still on disk after the session delete");
  } else {
    ok(!exists(agentDir) && !exists(artifactDir), "fixed image: every session directory was reclaimed");
  }
} catch (error) {
  record.error = String(error?.stack ?? error);
  console.error("[probe] failed:", error);
  save();
  process.exitCode = 1;
} finally {
  await browser.close();
}
