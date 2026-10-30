// Verification probe for the three product fixes, run against the deployed app
// (default :7722, the pf-local container).
//
//   node e2e/scripts/fix-orphans-probe.mjs
//
// It drives the user's own paths for each fix and prints [ok]/[BROKEN] per
// assertion:
//
//   FIX 1  create a project + session, delete it from the UI, then ask the
//          container whether sandboxes/<user>/<project> is really gone.
//   FIX 2  create a private skill, disable it, import the same name, and read
//          back that the import re-enabled it and a new session mounts it.
//   FIX 3  an admin creates a shared skill from the UI; a normal user sees it in
//          the list and the /skill: palette and cannot edit it.
//
// Screenshots and a JSON log land in e2e/.artifacts/fix-orphans/.
import { chromium } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { crc32 } from "node:zlib";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-orphans");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const CONTAINER = process.env.PIFORGE_CONTAINER ?? "pf-local";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const ADMIN = { email: "admin@piforge.local", password: "piforge-admin-123" };
const stamp = Date.now();

const record = { base: BASE, container: CONTAINER, stamp, checks: {}, findings: [] };
const save = () =>
  writeFileSync(path.join(out, "probe-log.json"), JSON.stringify(record, null, 2));
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
  const me = await page.request.get(`${BASE}/v1/auth/me`);
  return (await me.json()).user;
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

/** Asks the container, not the API: the fix is about bytes on disk. */
function sandboxDirExists(userId, projectId) {
  const target = `/data/sandboxes/${userId}/${projectId}`;
  try {
    execFileSync("docker", ["exec", CONTAINER, "test", "-d", target], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

const zipBytes = (files) => {
  // Minimal stored-entry zip, same shape as the e2e helper writes.
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const file of files) {
    const name = Buffer.from(file.path, "utf8");
    const data = Buffer.from(file.body, "utf8");
    const sum = crc32(data);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0x21, 12);
    local.writeUInt32LE(sum, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    locals.push(local, name, data);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt16LE(0, 12);
    central.writeUInt16LE(0x21, 14);
    central.writeUInt32LE(sum, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt16LE(0, 30);
    central.writeUInt16LE(0, 32);
    central.writeUInt16LE(0, 34);
    central.writeUInt16LE(0, 36);
    central.writeUInt32LE(0, 38);
    central.writeUInt32LE(offset, 42);
    centrals.push(central, name);
    offset += local.length + name.length + data.length;
  }
  const directory = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  end.writeUInt16LE(0, 20);
  return Buffer.concat([...locals, directory, end]);
};

const browser = await chromium.launch({ channel: "chrome" });

try {
  // ---------------------------------------------------------------- FIX 1
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const me = await login(page, USER);
  note("user_id", me.id);

  const projectName = `probe-orphan-${stamp}`;
  const createdProject = await api(page, "POST", "/v1/projects", { name: projectName });
  if (createdProject.status !== 201) throw new Error(`create project: ${JSON.stringify(createdProject.body)}`);
  const projectId = createdProject.body.project.id;
  const createdSession = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "probe" });
  if (createdSession.status !== 201) throw new Error(`create session: ${JSON.stringify(createdSession.body)}`);

  let existed = false;
  for (let i = 0; i < 100; i++) {
    if (sandboxDirExists(me.id, projectId)) {
      existed = true;
      break;
    }
    await page.waitForTimeout(150);
  }
  note("sandbox_before_delete", { projectId, dirExists: existed });
  ok(existed, "the session materialised sandboxes/<user>/<project> on the container before the delete");

  await page.goto(`${BASE}/p/${projectId}`);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-delete").click();
  await page.getByTestId("project-delete-dialog").waitFor();
  await page.screenshot({ path: path.join(out, "fix1-delete-confirm.png") });
  await page.getByTestId("project-delete-confirm").click();
  await page.waitForURL(/\/$/, { timeout: 30_000 });
  await page.locator("[data-sonner-toast]").filter({ hasText: "项目已删除" }).waitFor({ timeout: 20_000 });
  await page.screenshot({ path: path.join(out, "fix1-deleted-home.png") });

  const projectGone = (await page.request.get(`${BASE}/v1/projects/${projectId}`)).status() === 404;
  let dirGone = false;
  const goneBy = Date.now() + 10_000;
  while (Date.now() < goneBy) {
    if (!sandboxDirExists(me.id, projectId)) {
      dirGone = true;
      break;
    }
    await page.waitForTimeout(200);
  }
  note("fix1", { projectGone, dirGone });
  ok(projectGone, "FIX 1: the project row is gone after the UI delete");
  ok(dirGone, "FIX 1: sandboxes/<user>/<project> is gone too — no orphan left on the container");

  // ---------------------------------------------------------------- FIX 2
  const skillName = `probe-skill-${stamp}`;
  const createdSkill = await api(page, "POST", "/v1/skills", {
    name: skillName,
    description: "import re-enable probe",
    body: "first version\n",
  });
  const skillId = createdSkill.body?.skill?.id;
  ok(createdSkill.status === 201 && Boolean(skillId), "FIX 2: a private skill was created");
  const disabled = await api(page, "PATCH", `/v1/skills/${skillId}`, { enabled: false });
  ok(disabled.status === 200 && disabled.body.skill.enabled === false, "FIX 2: it starts disabled");

  const zip = zipBytes([
    { path: `collection/${skillName}/SKILL.md`, body: "second version from the import\n" },
  ]);
  const importRes = await page.request.post(`${BASE}/v1/skills/import/zip`, {
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    multipart: { file: { name: "probe.zip", mimeType: "application/zip", buffer: zip } },
  });
  const importBody = await importRes.json().catch(() => null);
  note("fix2", { status: importRes.status(), skills: importBody?.skills });
  const imported = importBody?.skills?.find((skill) => skill.name === skillName);
  ok(importRes.status() === 200 && imported?.skipped === false, "FIX 2: the import replaced the bundle");
  ok(imported?.enabled === true && imported?.reenabled === true, "FIX 2: the response says it re-enabled the skill");

  const listed = await api(page, "GET", "/v1/skills");
  const row = listed.body.skills.find((skill) => skill.id === skillId);
  ok(row?.enabled === true, "FIX 2: the skill row is enabled, so a new session will mount it");

  await page.goto(`${BASE}/settings/skills`);
  const item = page.locator(`[data-testid="skill-item"][data-name="${skillName}"]`);
  await item.waitFor({ timeout: 20_000 });
  await page.screenshot({ path: path.join(out, "fix2-skill-enabled-after-import.png") });
  ok((await item.getByText("已停用").count()) === 0, "FIX 2: the list no longer shows the disabled badge");

  const session = await api(page, "POST", `/v1/projects`, { name: `probe-mount-${stamp}` });
  const mountProject = session.body.project.id;
  const sess = await api(page, "POST", `/v1/projects/${mountProject}/sessions`, { title: "mount" });
  const tooling = await api(page, "GET", `/v1/sessions/${sess.body.session.id}/tooling`);
  note("fix2_tooling", tooling.body?.mounted);
  ok(
    (tooling.body?.mounted?.skills ?? []).includes(skillName),
    "FIX 2: a new session really mounts the re-enabled skill",
  );
  await api(page, "DELETE", `/v1/projects/${mountProject}`);

  // ---------------------------------------------------------------- FIX 3
  const admin = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await login(admin, ADMIN);
  await admin.goto(`${BASE}/settings/skills`);
  const sharedName = `probe-shared-${stamp}`;
  await admin.getByTestId("skill-add").click();
  await admin.getByTestId("skill-shared").waitFor({ timeout: 20_000 });
  ok((await admin.getByTestId("skill-shared").count()) === 1, "FIX 3: an admin sees the share-with-everyone switch");
  await admin.getByTestId("skill-name").fill(sharedName);
  await admin.getByTestId("skill-description").fill("probe shared skill");
  await admin.getByTestId("skill-body").fill("# shared\n\nprobe\n");
  await admin.getByTestId("skill-shared").click();
  // The switch paints after the state update, and its thumb has a transition:
  // waiting for aria-checked plus one animation frame is what makes the
  // screenshot evidence rather than a picture of the previous frame.
  await admin
    .locator('[data-testid="skill-shared"][aria-checked="true"]')
    .waitFor({ timeout: 10_000 });
  await admin.waitForTimeout(400);
  await admin.screenshot({ path: path.join(out, "fix3-admin-create-shared-dialog.png") });
  await admin.getByTestId("skill-submit").click();
  const sharedRow = admin.locator(`[data-testid="skill-item"][data-name="${sharedName}"]`);
  await sharedRow.waitFor({ timeout: 20_000 });
  // Let the dialog finish closing so the screenshot shows the list, not a
  // half-faded form.
  await admin.getByTestId("skill-shared").waitFor({ state: "detached", timeout: 10_000 });
  await admin.screenshot({ path: path.join(out, "fix3-admin-shared-created.png") });
  const adminList = await api(admin, "GET", "/v1/skills");
  const shared = adminList.body.skills.find((skill) => skill.name === sharedName);
  note("fix3_created", shared);
  ok(shared?.scope === "admin", "FIX 3: the created skill has scope=admin");

  // A normal user, in its own context, sees it and cannot edit it.
  const normal = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await login(normal, USER);
  await normal.goto(`${BASE}/settings/skills`);
  const seen = normal.locator(`[data-testid="skill-item"][data-name="${sharedName}"]`);
  await seen.waitFor({ timeout: 20_000 });
  await normal.screenshot({ path: path.join(out, "fix3-normal-user-sees-shared.png") });
  ok((await seen.textContent()).includes("共享"), "FIX 3: a normal user sees it, marked 共享");
  ok(await seen.getByRole("switch").isDisabled(), "FIX 3: the normal user's enable switch is disabled");

  const palette = await api(normal, "GET", "/v1/skills");
  ok(
    palette.body.skills.some((skill) => skill.name === sharedName && skill.scope === "admin"),
    "FIX 3: it is in the skill list the /skill: palette reads",
  );
  const refusedPatch = await api(normal, "PATCH", `/v1/skills/${shared.id}`, { enabled: false });
  const refusedCreate = await api(normal, "POST", "/v1/skills", {
    scope: "admin",
    name: `probe-nope-${stamp}`,
    description: "x",
    body: "# x\n",
  });
  note("fix3_refusals", { patch: refusedPatch.status, create: refusedCreate.status });
  ok(refusedPatch.status === 403, "FIX 3: the API refuses a normal user's edit of the shared skill");
  ok(refusedCreate.status === 403, "FIX 3: the API refuses a normal user creating a shared skill");

  // Cleanup: deployment-wide state and this probe's private skill.
  const cleanupShared = await api(admin, "DELETE", `/v1/skills/${shared.id}`);
  const cleanupSkill = await api(page, "DELETE", `/v1/skills/${skillId}`);
  note("cleanup", { shared: cleanupShared.status, private: cleanupSkill.status });

  const failures = record.findings.filter((finding) => !finding.ok).length;
  note("result", { checks: record.findings.length, broken: failures });
  console.log(failures === 0 ? "[RESULT] PASS" : `[RESULT] ${failures} BROKEN`);
  process.exitCode = failures === 0 ? 0 : 1;
} finally {
  save();
  await browser.close();
}
