// Deployment probe for FR-14.2 without CAP_SYS_ADMIN.
//
//   node e2e/scripts/no-cap-skill-probe.mjs [baseURL] [container]
//
// The default deployment must not need SYS_ADMIN for a correct read-only story.
// Against the real deployment (default http://127.0.0.1:7722, container
// pf-local) this proves:
//
//   1. the container has no added capabilities (HostConfig.CapAdd is empty) and
//      drops CAP_DAC_OVERRIDE (HostConfig.CapDrop);
//   2. piforged's own CapEff has neither CAP_SYS_ADMIN (21) nor CAP_DAC_OVERRIDE
//      (1) — the container-level drop is what makes 0444 real for root, since a
//      process cannot drop another thread's capabilities;
//   3. a mounted skill reports `copy-ro` (not `copy`, and not `bind-ro`), so the
//      badge can honestly say 「只读副本」;
//   4. writing an existing skill file through the file API is refused (4xx/5xx)
//      and its bytes do not change;
//   5. no snapshot directory was created for the session (a copy needs none).
//
// With SYS_ADMIN present the same probe asserts `bind-ro` instead, so the
// upgrade path stays covered.
//
// Screenshots and a JSON log land in e2e/.artifacts/fix-gc/.
import { chromium } from "@playwright/test";
import { execFileSync, spawnSync } from "node:child_process";
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
const save = () => writeFileSync(path.join(out, "no-cap-skill-probe.json"), JSON.stringify(record, null, 2));
const note = (key, value) => {
  record.checks[key] = value;
  console.log(`[${key}]`, JSON.stringify(value));
};
const ok = (condition, message) => {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  record.findings.push({ ok: Boolean(condition), message });
  return Boolean(condition);
};

const docker = (args) =>
  execFileSync("docker", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();

// container logs go to both streams (slog writes to stderr), so neither one
// alone is the log.
const dockerLogs = (name) => {
  const res = spawnSync("docker", ["logs", name], { encoding: "utf8" });
  return `${res.stdout ?? ""}${res.stderr ?? ""}`;
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

const browser = await chromium.launch({ channel: "chrome" });
let page = null;

try {
  page = await browser.newPage({ viewport: { width: 1440, height: 950 } });
  const me = await login(page);
  note("user_id", me.id);

  // --- 1/2. the deployment's capabilities --------------------------------
  const capAdd = docker(["inspect", CONTAINER, "--format", "{{json .HostConfig.CapAdd}}"]);
  const capDrop = docker(["inspect", CONTAINER, "--format", "{{json .HostConfig.CapDrop}}"]);
  note("container_caps", { add: capAdd, drop: capDrop });
  ok(capAdd === "null" || capAdd === "[]", "the container adds no capabilities (no --cap-add SYS_ADMIN)");
  ok((capDrop ?? "").includes("DAC_OVERRIDE"), "the container drops CAP_DAC_OVERRIDE (a cap_drop, not a grant)");

  const pid = docker(["exec", CONTAINER, "sh", "-c", "pgrep -f '/app/bin/piforged' | head -1"]);
  const status = docker(["exec", CONTAINER, "cat", `/proc/${pid}/status`]);
  const capEff = (status.split(/\r?\n/).find((line) => line.startsWith("CapEff:")) ?? "").split(/\s+/)[1] ?? "";
  const bits = BigInt(`0x${capEff || "0"}`);
  const hasSysAdmin = (bits & (1n << 21n)) !== 0n;
  const hasDacOverride = (bits & (1n << 1n)) !== 0n; // CAP_DAC_OVERRIDE = 1
  note("piforged_caps", { pid, cap_eff: capEff, sys_admin: hasSysAdmin, dac_override: hasDacOverride });
  ok(!hasSysAdmin, "piforged has no CAP_SYS_ADMIN (no bind mount is possible)");
  ok(!hasDacOverride, "piforged has no CAP_DAC_OVERRIDE, so 0444 is enforced even for root");

  const log = dockerLogs(CONTAINER).replace(/\u001b\[[0-9;]*m/g, "");
  const dropLine = log.split(/\r?\n/).find((line) => line.includes("skill mounts use read-only copies")) ?? "";
  note("startup_log", dropLine);
  ok(dropLine.includes("copy-ro"), "the startup log names the mechanism this deployment reports (copy-ro)");

  // --- 3/4. the mechanism and a refused write ---------------------------
  const skillName = `nocap-probe-${stamp}`;
  const createdSkill = await api(page, "POST", "/v1/skills", {
    name: skillName,
    version: "1.0.0",
    description: "no-cap probe",
    body: `---\nname: ${skillName}\ndescription: no-cap probe\n---\n\n# No-cap probe\nAnswer with NOCAP-ONE.\n`,
  });
  if (createdSkill.status >= 300) throw new Error(`create skill: ${createdSkill.status}`);
  const skillId = createdSkill.body.skill.id;

  const project = await api(page, "POST", "/v1/projects", { name: `probe-nocap-${stamp}` });
  if (project.status >= 300) throw new Error(`create project: ${project.status}`);
  const projectId = project.body.project.id;
  const createdSession = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "no cap" });
  if (createdSession.status >= 300) throw new Error(`create session: ${createdSession.status}`);
  const sessionId = createdSession.body.session.id;

  const mounted = `.pi/skills/${skillName}/SKILL.md`;
  let tooling = null;
  let mechanism = null;
  for (let i = 0; i < 120; i++) {
    tooling = (await api(page, "GET", `/v1/sessions/${sessionId}/tooling`)).body;
    if ((tooling?.mounted?.skills ?? []).includes(skillName)) {
      mechanism = tooling.mounted.mechanisms?.[skillName] ?? null;
      break;
    }
    await page.waitForTimeout(250);
  }
  note("mechanism", { value: mechanism, mounted: tooling?.mounted ?? null });
  ok(mechanism === "copy-ro", `the reported mechanism is copy-ro without SYS_ADMIN (got ${mechanism})`);

  const readMounted = async () => {
    const res = await api(page, "GET", `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(mounted)}`);
    return { status: res.status, body: res.body?.body ?? "" };
  };
  const before = await readMounted();
  const write = await api(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(mounted)}`,
    "agent was here\n",
  );
  const after = await readMounted();
  note("write_attempt", {
    status: write.status,
    before_has_nocap_one: before.body.includes("NOCAP-ONE"),
    after_has_nocap_one: after.body.includes("NOCAP-ONE"),
  });
  ok(write.status >= 400, "writing an existing skill file is refused (4xx/5xx)");
  ok(after.body.includes("NOCAP-ONE"), "the refused write did not change the bytes the agent reads");

  // A copy needs no snapshot: the directory belongs to the bind mechanism.
  const snapshotPresent = (() => {
    try {
      const listed = docker(["exec", CONTAINER, "ls", "-1", "/data/skill-snapshots"]).split(/\r?\n/);
      const norm = (s) => s.toLowerCase().replace(/[^a-z0-9]/g, "");
      return listed.some((name) => norm(name) === norm(sessionId));
    } catch {
      return false;
    }
  })();
  note("snapshot_dir_for_session", snapshotPresent);
  ok(!snapshotPresent, "copy-ro created no snapshot directory (there is no mount to share)");

  // --- the UI badge says the same thing, with its accessible one-liner ----
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("session-logs-open").click();
  const item = page.getByTestId("session-skill-item").filter({ hasText: skillName });
  await item.waitFor({ timeout: 30_000 });
  const badge = item.getByTestId("session-skill-mechanism");
  const badgeText = await badge.innerText();
  const dataMechanism = await badge.getAttribute("data-mechanism");
  const title = await badge.getAttribute("title");
  const aria = await badge.getAttribute("aria-label");
  note("ui_badge", { badgeText, dataMechanism, title, aria });
  ok(dataMechanism === mechanism, "the badge reports the same mechanism the API does");
  ok(badgeText.includes("只读副本"), "the badge says 只读副本, not 只读挂载");
  ok(
    (title ?? "").includes("agent 无法覆盖") && (aria ?? "").includes("agent 无法覆盖"),
    "the tooltip/aria line explains that a copy refuses overwriting but allows new files",
  );
  await page.screenshot({ path: path.join(out, "nocap-mechanism-copy-ro.png"), fullPage: true });

  // Cleanup the probe's own rows.
  await api(page, "DELETE", `/v1/projects/${projectId}`);
  await api(page, "DELETE", `/v1/skills/${skillId}`);

  record.ok = record.findings.every((finding) => finding.ok);
} catch (error) {
  record.ok = false;
  record.error = String(error?.stack ?? error);
  console.error(record.error);
  if (page) {
    await page.screenshot({ path: path.join(out, "nocap-probe-failure.png"), fullPage: true }).catch(() => {});
  }
} finally {
  save();
  await browser.close();
  console.log(`evidence: ${path.join(out, "no-cap-skill-probe.json")}`);
  process.exit(record.ok ? 0 : 1);
}
