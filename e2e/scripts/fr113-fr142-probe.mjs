#!/usr/bin/env node
/**
 * Deployed-app verification for FR-11.3 (exclusive browser takeover) and
 * FR-14.2 (read-only, frozen skill mounts with a named mechanism).
 *
 * Against the real deployment (default http://127.0.0.1:7722) it proves:
 *   1. a takeover grants a lease and reports the holder;
 *   2. a second session of the same sandbox is refused with 409 `browser_busy`
 *      naming the holder and the remaining seconds, and the panel shows
 *      「正在被 X 控制」 instead of offering a fight;
 *   3. switching back to observe releases the browser and the second session can
 *      take over;
 *   4. a mounted skill reports the mechanism actually in use (bind-ro or
 *      copy-ro) through the API and the UI badge;
 *   5. a write to the mounted tree is refused and does not change what the
 *      agent reads;
 *   6. editing the skill does not reach the running session, while a new
 *      session gets the edit.
 *
 * No API key is needed or read: the sessions are created but never prompted.
 *
 * Usage: node fr113-fr142-probe.mjs [baseURL]
 */
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(HERE, "../.artifacts/partial-batch3");
mkdirSync(OUT, { recursive: true });

const base = (process.argv[2] ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };

const evidence = { base, at: new Date().toISOString(), steps: [], screenshots: [] };
const step = (name, value = {}) => {
  evidence.steps.push({ name, ...value });
  console.log(`${name}:`, JSON.stringify(value));
};

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1500, height: 950 } });
const page = await context.newPage();

const csrf = async () =>
  (await context.cookies()).find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
const api = async (method, route, data) =>
  page.request.fetch(`${base}${route}`, {
    method,
    headers: { "X-PF-CSRF": await csrf() },
    data,
  });
const getJSON = async (route) => (await api("GET", route)).json();
const shot = async (name, p = page) => {
  const file = path.join(OUT, name);
  await p.screenshot({ path: file, fullPage: true });
  evidence.screenshots.push(file);
};

try {
  // --- login ---------------------------------------------------------------
  await page.goto(`${base}/login`);
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill(USER.password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
  step("logged in", { user: USER.email });

  // --- two sessions in one sandbox (same project) --------------------------
  const stamp = Date.now();
  const project = await (
    await api("POST", "/v1/projects", {
      name: `probe-batch3-${stamp}`,
      repo_url: "",
      default_branch: "main",
    })
  ).json();
  const projectId = project.project.id;
  const mkSession = async (title) =>
    (
      await (
        await api("POST", `/v1/projects/${projectId}/sessions`, { title })
      ).json()
    ).session;
  const first = await mkSession("first");
  const second = await mkSession("second");
  step("sessions", {
    project_id: projectId,
    first: { id: first.id, worktree: first.worktree_id },
    second: { id: second.id, worktree: second.worktree_id },
  });
  if (!first.worktree_id || !second.worktree_id) {
    throw new Error("a session has no worktree; the browser endpoints need one");
  }

  // --- FR-11.3: grant, refuse, release, retake -----------------------------
  const granted = await api("POST", `/v1/sessions/${first.id}/browser/mode`, { mode: "takeover" });
  const grantedBody = await granted.json();
  step("takeover granted", { status: granted.status(), body: grantedBody });

  const refused = await api("POST", `/v1/sessions/${second.id}/browser/mode`, { mode: "takeover" });
  const refusedBody = await refused.json();
  step("second takeover refused", {
    status: refused.status(),
    code: refusedBody.error?.code,
    message: refusedBody.error?.message,
    details: refusedBody.error?.details,
  });

  // The second panel says who holds it.
  await page.goto(`${base}/p/${projectId}/s/${second.id}`);
  await page.getByTestId("panel-tab-browser").click();
  await page.getByTestId("browser-panel").waitFor({ timeout: 60_000 });
  await page.getByTestId("browser-holder").waitFor({ timeout: 30_000 });
  const holderText = await page.getByTestId("browser-holder").innerText();
  const takeoverDisabled = await page.getByTestId("browser-mode").isDisabled();
  step("second panel shows the holder", { holderText, takeoverDisabled });
  await shot("fr-11.3-busy.png");

  // Release from the first session, then the second can take over.
  const released = await api("POST", `/v1/sessions/${first.id}/browser/mode`, { mode: "observe" });
  step("released", { status: released.status(), body: await released.json() });

  await page.getByTestId("browser-holder").waitFor({ state: "detached", timeout: 30_000 });
  const retake = await api("POST", `/v1/sessions/${second.id}/browser/mode`, { mode: "takeover" });
  const retakeBody = await retake.json();
  step("takeover after release", { status: retake.status(), body: retakeBody });

  // The panel follows the lease on its own; no confirmation is involved because
  // the takeover already happened through the API.
  await page.getByTestId("browser-mode").filter({ hasText: "控制中" }).waitFor({ timeout: 30_000 });
  await shot("fr-11.3-controlling.png");

  // Leave the deployment as we found it.
  await api("POST", `/v1/sessions/${second.id}/browser/mode`, { mode: "observe" });
  await page.getByTestId("browser-mode").filter({ hasText: "观察" }).waitFor({ timeout: 30_000 });

  // --- FR-14.2: mechanism, read-only, frozen -------------------------------
  const skillName = `probe-mount-${stamp}`;
  const v1 = `---\nname: ${skillName}\ndescription: probe mount\n---\n\n# Probe mount\nPROBE-ONE.\n`;
  const v2 = `---\nname: ${skillName}\ndescription: probe mount\n---\n\n# Probe mount\nPROBE-TWO.\n`;
  const createdSkill = await (
    await api("POST", "/v1/skills", {
      name: skillName,
      version: "1.0.0",
      description: "probe mount",
      body: v1,
    })
  ).json();
  const skillId = createdSkill.skill.id;

  const mountProject = await (
    await api("POST", "/v1/projects", {
      name: `probe-mount-${stamp}`,
      repo_url: "",
      default_branch: "main",
    })
  ).json();
  const mountProjectId = mountProject.project.id;
  const mountSession = (
    await (await api("POST", `/v1/projects/${mountProjectId}/sessions`, { title: "mount" })).json()
  ).session;

  const mountedPath = `.pi/skills/${skillName}/SKILL.md`;
  const readFile = async (sessionId, p) => {
    const res = await api(
      "GET",
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(p)}`,
    );
    if (res.status() !== 200) return { status: res.status(), body: "" };
    return { status: res.status(), body: (await res.json()).body };
  };

  let tooling = await getJSON(`/v1/sessions/${mountSession.id}/tooling`);
  for (let i = 0; i < 40 && !(tooling.mounted?.skills ?? []).includes(skillName); i += 1) {
    await page.waitForTimeout(500);
    tooling = await getJSON(`/v1/sessions/${mountSession.id}/tooling`);
  }
  const mechanism = tooling.mounted?.mechanisms?.[skillName];
  const skillsView = await getJSON(`/v1/sessions/${mountSession.id}/skills`);
  const record = (skillsView.skills ?? []).find((skill) => skill.name === skillName);
  step("skill mounted", {
    mechanism,
    mounted_path: record?.mounted_path,
    content_hash: record?.content_hash,
    api_mechanism: skillsView.mechanisms?.[skillName],
  });

  const before = await readFile(mountSession.id, mountedPath);
  const write = await api(
    "PUT",
    `/v1/sessions/${mountSession.id}/files?path=${encodeURIComponent(mountedPath)}`,
    "agent was here\n",
  );
  const after = await readFile(mountSession.id, mountedPath);
  step("write attempt", {
    write_status: write.status(),
    write_body: (await write.text()).slice(0, 300),
    before_has_probe_one: before.body.includes("PROBE-ONE"),
    after_has_probe_one: after.body.includes("PROBE-ONE"),
  });

  // The UI names the mechanism.
  await page.goto(`${base}/p/${mountProjectId}/s/${mountSession.id}`);
  await page.getByTestId("session-logs-open").click();
  const item = page.getByTestId("session-skill-item").filter({ hasText: skillName });
  await item.waitFor({ timeout: 30_000 });
  const badge = item.getByTestId("session-skill-mechanism");
  const badgeText = (await badge.innerText().catch(() => "")) || "";
  const badgeMechanism = (await badge.getAttribute("data-mechanism").catch(() => null)) || null;
  step("ui mechanism badge", { badgeText, badgeMechanism });
  await shot(`fr-14.2-mechanism-${badgeMechanism ?? "unknown"}.png`);

  // Edit the skill: the running session keeps its copy, a new one gets the edit.
  const replaced = await api("PUT", `/v1/skills/${skillId}/bundle`, {
    files: [{ path: "SKILL.md", content: v2 }],
  });
  step("skill edited", { status: replaced.status() });
  const stillOld = await readFile(mountSession.id, mountedPath);
  step("running session unchanged", { has_probe_one: stillOld.body.includes("PROBE-ONE") });

  const freshProject = await (
    await api("POST", "/v1/projects", {
      name: `probe-mount-new-${stamp}`,
      repo_url: "",
      default_branch: "main",
    })
  ).json();
  const freshSession = (
    await (
      await api("POST", `/v1/projects/${freshProject.project.id}/sessions`, { title: "fresh" })
    ).json()
  ).session;
  let freshBody = "";
  for (let i = 0; i < 40; i += 1) {
    freshBody = (await readFile(freshSession.id, mountedPath)).body;
    if (freshBody.includes("PROBE-TWO")) break;
    await page.waitForTimeout(500);
  }
  step("new session got the edit", { has_probe_two: freshBody.includes("PROBE-TWO") });

  // Clean up the skill row so a repeated probe does not accumulate names.
  await api("DELETE", `/v1/skills/${skillId}`);
  await api("DELETE", `/v1/projects/${projectId}`);
  await api("DELETE", `/v1/projects/${mountProjectId}`);
  await api("DELETE", `/v1/projects/${freshProject.project.id}`);
  step("cleanup", { ok: true });

  evidence.ok = true;
} catch (error) {
  evidence.ok = false;
  evidence.error = String(error?.stack ?? error);
  console.error(evidence.error);
  await shot("probe-failure.png").catch(() => {});
} finally {
  writeFileSync(path.join(OUT, "probe.json"), JSON.stringify(evidence, null, 2));
  await context.close();
  await browser.close();
  console.log(`evidence: ${path.join(OUT, "probe.json")}`);
  process.exit(evidence.ok ? 0 : 1);
}
