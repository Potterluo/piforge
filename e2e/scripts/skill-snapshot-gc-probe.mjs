// Verification probe for FR-14.2's snapshot lifecycle, run against the deployed
// app (default :7722, the pf-local container).
//
//   node e2e/scripts/skill-snapshot-gc-probe.mjs [baseURL] [container]
//
// A session's frozen skill snapshots live in the container at
// /data/skill-snapshots/<session>. This probe asks the container rather than the
// API, because the bug was bytes on disk outliving the row:
//
//   1. a session that mounts a skill leaves its snapshot directory behind;
//   2. deleting that session removes it within a few seconds;
//   3. deleting a project removes every child session's snapshot, including a
//      branch session's own snapshot.
//
// On the pre-fix code (1) passes and (2)/(3) fail: the directories accumulate.
// On the fixed code all three pass. It requires bind-ro to be in effect (only
// that mechanism creates a snapshot); run it on a deployment that has
// CAP_SYS_ADMIN, or on a throw-away container started with --cap-add SYS_ADMIN.
//
// Screenshots and a JSON log land in e2e/.artifacts/fix-gc/.
import { chromium } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-gc");
mkdirSync(out, { recursive: true });

const BASE = (process.argv[2] ?? process.env.BASE ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const CONTAINER = process.argv[3] ?? process.env.PIFORGE_CONTAINER ?? "pf-local";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const stamp = Date.now();

const record = { base: BASE, container: CONTAINER, stamp, at: new Date().toISOString(), checks: {}, findings: [] };
const save = () => writeFileSync(path.join(out, "snapshot-gc-probe.json"), JSON.stringify(record, null, 2));
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

async function login(page) {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill(USER.password);
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

/** Asks the container for the snapshot directory names: the fix is about bytes. */
function snapshotDirs() {
  try {
    const raw = execFileSync("docker", ["exec", CONTAINER, "ls", "-1", "/data/skill-snapshots"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    });
    return raw
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean);
  } catch {
    return [];
  }
}

const dirOf = (sessionID) => {
  // The directory is named after the session id (older builds used its slug, so
  // match case- and separator-insensitively).
  const norm = (s) => s.toLowerCase().replace(/[^a-z0-9]/g, "");
  return snapshotDirs().find((name) => norm(name) === norm(sessionID)) ?? null;
};

async function waitFor(predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return true;
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  return predicate();
}

const browser = await chromium.launch({ channel: "chrome" });
let page = null;

try {
  page = await browser.newPage({ viewport: { width: 1440, height: 950 } });
  const me = await login(page);
  note("user_id", me.id);

  // A skill with content, so provisioning actually mounts something.
  const skillName = `gc-probe-${stamp}`;
  const createdSkill = await api(page, "POST", "/v1/skills", {
    name: skillName,
    version: "1.0.0",
    description: "snapshot gc probe",
    body: `---\nname: ${skillName}\ndescription: snapshot gc probe\n---\n\n# Snapshot GC probe\nAnswer with GC-ONE.\n`,
  });
  if (createdSkill.status >= 300) throw new Error(`create skill: ${createdSkill.status}`);
  const skillId = createdSkill.body.skill.id;

  const before = snapshotDirs();
  const project = await api(page, "POST", "/v1/projects", { name: `probe-gc-${stamp}` });
  if (project.status >= 300) throw new Error(`create project: ${project.status}`);
  const projectId = project.body.project.id;
  const created = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "gc one" });
  if (created.status >= 300) throw new Error(`create session: ${created.status}`);
  const sessionId = created.body.session.id;

  // Wait for the mount, and record the mechanism. A snapshot only exists when
  // the mount is a read-only bind of one.
  let mechanism = null;
  let tooling = null;
  for (let i = 0; i < 120; i++) {
    tooling = (await api(page, "GET", `/v1/sessions/${sessionId}/tooling`)).body;
    const skills = tooling?.mounted?.skills ?? [];
    if (skills.includes(skillName)) {
      mechanism = tooling.mounted.mechanisms?.[skillName] ?? null;
      break;
    }
    await page.waitForTimeout(250);
  }
  note("mechanism", mechanism);
  note("snapshot_dirs_before", before.length);

  const appeared = await waitFor(() => snapshotDirs().length > before.length, 10_000);
  const sessionDir = dirOf(sessionId);
  note("snapshot_after_create", { appeared, dir: sessionDir, count: snapshotDirs().length });
  ok(appeared, "a mounted session created a snapshot directory on the container");
  ok(sessionDir !== null, "the snapshot directory is named after the session");
  if (!appeared || sessionDir === null) {
    throw new Error(
      mechanism === "bind-ro"
        ? "bind-ro is in effect but no snapshot directory appeared"
        : `no snapshot directory: the mechanism is ${mechanism}; this probe needs bind-ro (CAP_SYS_ADMIN)`,
    );
  }

  // The UI names the mechanism; kept as evidence of the pairing this fix belongs
  // to.
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("session-logs-open").click();
  const item = page.getByTestId("session-skill-item").filter({ hasText: skillName });
  await item.waitFor({ timeout: 30_000 });
  await page.screenshot({ path: path.join(out, "gc-before-session-delete.png"), fullPage: true });

  // --- 1. delete the session ------------------------------------------------
  const deleted = await api(page, "DELETE", `/v1/sessions/${sessionId}`);
  const dirGone = await waitFor(() => snapshotDirs().every((name) => name !== sessionDir), 10_000);
  note("session_delete", {
    status: deleted.status,
    dir: sessionDir,
    snapshot_dir_gone: dirGone,
    remaining: snapshotDirs().length,
  });
  ok(dirGone, "deleting a session removed its snapshot directory within 10s");

  await page.screenshot({ path: path.join(out, "gc-after-session-delete.png"), fullPage: true });

  // --- 2. delete a project with a main and a branch session -----------------
  const beforeProject = snapshotDirs();
  const project2 = await api(page, "POST", "/v1/projects", { name: `probe-gc2-${stamp}` });
  if (project2.status >= 300) throw new Error(`create project 2: ${project2.status}`);
  const project2Id = project2.body.project.id;
  const mainSession = (
    await api(page, "POST", `/v1/projects/${project2Id}/sessions`, { title: "gc main" })
  ).body.session;

  const branchSession = (
    await api(page, "POST", `/v1/projects/${project2Id}/sessions`, {
      title: "gc branch",
      kind: "branch",
    })
  ).body.session;

  // Both sessions are provisioned by creation; wait for two new snapshot dirs.
  const twoAppeared = await waitFor(() => snapshotDirs().length >= beforeProject.length + 2, 30_000);
  const mainDir = dirOf(mainSession.id);
  const branchDir = dirOf(branchSession.id);
  note("project_before_delete", {
    appeared: twoAppeared,
    main_dir: mainDir,
    branch_dir: branchDir,
    count: snapshotDirs().length,
  });
  ok(twoAppeared && mainDir !== null && branchDir !== null,
    "both sessions of the project have their own snapshot directory");

  const deletedProject = await api(page, "DELETE", `/v1/projects/${project2Id}`);
  const projectDirGone = await waitFor(
    () => snapshotDirs().every((name) => name !== mainDir && name !== branchDir),
    15_000,
  );
  note("project_delete", {
    status: deletedProject.status,
    dirs_gone: projectDirGone,
    remaining_for_project: [dirOf(mainSession.id), dirOf(branchSession.id)],
  });
  ok(projectDirGone, "deleting a project removed every child session's snapshot, branch included");

  await page.goto(`${BASE}/`);
  await page.screenshot({ path: path.join(out, "gc-after-project-delete.png"), fullPage: true });

  // Cleanup what is left of this probe.
  await api(page, "DELETE", `/v1/projects/${projectId}`);
  await api(page, "DELETE", `/v1/skills/${skillId}`);

  record.ok = record.findings.every((finding) => finding.ok);
} catch (error) {
  record.ok = false;
  record.error = String(error?.stack ?? error);
  console.error(record.error);
  if (page) {
    await page.screenshot({ path: path.join(out, "gc-probe-failure.png"), fullPage: true }).catch(() => {});
  }
} finally {
  save();
  await browser.close();
  console.log(`evidence: ${path.join(out, "snapshot-gc-probe.json")}`);
  process.exit(record.ok ? 0 : 1);
}
