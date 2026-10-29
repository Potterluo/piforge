// FR-14.5 + FR-14.6 on the deployed app (real Pi, real provider).
//
// It creates a skill, starts a session, opens the palette, filters it, inserts
// the invocation, sends one short turn, records the mounted version, edits the
// skill, and shows that the old session still reports the old version while a
// new session reports the new one.
//
//   node scripts/fr145-146-demo.mjs
//
// Artifacts: .artifacts/fr145-146/*.png plus summary.json.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const BASE = "http://127.0.0.1:7722";
const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fr145-146");
mkdirSync(ARTIFACTS, { recursive: true });

const EMAIL = "dev@piforge.local";
const PASSWORD = "piforge-dev-123";
const stamp = Date.now();
const NAME = `fr145-skill-${stamp}`;
const V1 = `---\nname: ${NAME}\ndescription: FR-14.5 demo skill (one)\n---\n\n# Demo skill\nWhen asked, answer with VERSION-ONE and nothing else.\n`;
const V2 = `---\nname: ${NAME}\ndescription: FR-14.5 demo skill (two)\n---\n\n# Demo skill\nWhen asked, answer with VERSION-TWO and nothing else.\n`;

const browser = await chromium.launch({ channel: "chrome", headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
page.setDefaultTimeout(90_000);
page.on("pageerror", (error) => console.log("[pageerror]", error.message));

const shot = async (name) => {
  await page.screenshot({ path: path.join(ARTIFACTS, name) });
  console.log("[shot]", name);
};

async function api(method, route, data) {
  const cookies = await context.cookies();
  const csrf = cookies.find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
  const res = await context.request.fetch(`${BASE}${route}`, {
    method,
    headers: { "X-PF-CSRF": csrf },
    data,
  });
  if (!res.ok()) throw new Error(`${method} ${route} -> ${res.status()} ${await res.text()}`);
  return res.json();
}

const sessionSkills = async (sessionId) =>
  (await api("GET", `/v1/sessions/${sessionId}/skills`)).skills;

async function waitForSkill(sessionId, timeout = 120_000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    const record = (await sessionSkills(sessionId)).find((skill) => skill.name === NAME);
    if (record) return record;
    await page.waitForTimeout(1000);
  }
  throw new Error(`session ${sessionId} never recorded ${NAME}`);
}

async function newSession(title) {
  const project = await api("POST", "/v1/projects", {
    name: `fr145-${stamp}-${title}`,
    repo_url: "",
    default_branch: "main",
  });
  const session = await api("POST", `/v1/projects/${project.project.id}/sessions`, { title });
  return { projectId: project.project.id, sessionId: session.session.id };
}

async function openSession(projectId, sessionId) {
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.locator(`[data-session-id="${sessionId}"]`).waitFor({ timeout: 60_000 });
}

// --- login ---------------------------------------------------------------
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill(EMAIL);
await page.getByTestId("login-password").fill(PASSWORD);
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
console.log("[login] ok");

// --- a skill with an explicit version -------------------------------------
// A previous demonstration run may have left skills behind; removing them keeps
// the Skill 中心 screenshot readable and the name unique.
const existing = await api("GET", "/v1/skills");
for (const skill of existing.skills) {
  if (skill.name.startsWith("fr145-skill-")) {
    await api("DELETE", `/v1/skills/${skill.id}`);
  }
}

const created = await api("POST", "/v1/skills", {
  name: NAME,
  version: "1.0.0",
  description: "FR-14.5 demo skill",
  body: V1,
});
const skillId = created.skill.id;
console.log("[skill] created", skillId, "version", created.skill.version, "hash", created.skill.content_hash.slice(0, 12));

// Show it in Skill 中心 too, with its version and derived hash.
await page.goto(`${BASE}/settings/skills`);
const skillRow = page.locator(`[data-testid="skill-item"][data-name="${NAME}"]`);
await skillRow.waitFor({ timeout: 30_000 });
await skillRow.scrollIntoViewIfNeeded();
await shot("01-skill-in-center.png");

// --- session A ------------------------------------------------------------
const a = await newSession("session-a");
await openSession(a.projectId, a.sessionId);
const recordA = await waitForSkill(a.sessionId);
console.log("[session A] record", recordA.version, recordA.content_hash.slice(0, 12), recordA.mounted_path);

const input = page.getByTestId("composer-input");
await input.click();
await page.keyboard.type("/");
const palette = page.getByTestId("skill-palette");
await palette.waitFor();
await page.waitForTimeout(300);
await shot("02-palette-open.png");

// Filter by name; the palette keeps version/hash on screen while filtering.
await page.keyboard.type(NAME);
await page.waitForTimeout(300);
await shot("03-palette-filter.png");
const filtered = page.getByTestId("skill-palette-item");
console.log("[palette] filtered rows", await filtered.count());
if ((await filtered.count()) !== 1) throw new Error("filter did not narrow to one skill");
if ((await filtered.first().getAttribute("data-name")) !== NAME) throw new Error("wrong skill filtered");

await input.press("Enter");
await page.getByTestId("composer-skill-chip").waitFor();
await page.waitForTimeout(200);
await shot("04-invocation-chip.png");
console.log("[chip]", await page.getByTestId("composer-skill-chip").innerText());

// One short provider turn: the invocation is an ordinary prompt.
await page.keyboard.type("只回复 ok");
await page.getByTestId("composer-send").click();
await page.getByTestId("turn-user").first().waitFor({ timeout: 30_000 });
await page.waitForFunction(
  () => document.querySelector('[data-testid="run-state"]')?.dataset.state === "settled",
  null,
  { timeout: 240_000 },
);
console.log("[run] settled");
await shot("05-session-a-transcript.png");

// The transcript kept the invocation, and Pi expanded it into the skill itself:
// the user turn carries `<skill name=... location=...>` plus the SKILL.md body,
// which is the agent being told about the skill, not just a mount on disk.
const turnText = await page.getByTestId("turn-user").first().innerText();
if (!turnText.includes(`<skill name="${NAME}"`)) {
  throw new Error("the agent was not given the skill block for the invocation");
}
if (!turnText.includes("VERSION-ONE")) throw new Error("the skill body did not reach the agent");
const reply = await page.getByTestId("turn-assistant").first().innerText();
if (!reply.includes("VERSION-ONE")) throw new Error("the run did not use the mounted skill");
console.log("[agent] turn contains the skill block and answered VERSION-ONE");

// --- session A's recorded version (read-only section) ---------------------
await page.getByTestId("session-logs-open").click();
const recordedA = page.locator(`[data-testid="session-skill-item"][data-name="${NAME}"]`);
await recordedA.waitFor();
await recordedA.scrollIntoViewIfNeeded();
await page.waitForTimeout(300);
await shot("06-session-a-version.png");
console.log("[session A] sheet shows", await recordedA.innerText());
await page.keyboard.press("Escape");

// --- edit the skill after session A exists --------------------------------
const replaced = await api("PUT", `/v1/skills/${skillId}/bundle`, {
  files: [{ path: "SKILL.md", content: V2 }],
});
console.log("[skill] edited, stored hash now", replaced.skill.content_hash.slice(0, 12));

const afterEdit = await waitForSkill(a.sessionId);
if (afterEdit.content_hash !== recordA.content_hash) {
  throw new Error("session A's record changed after the edit");
}
const rereadA = (await sessionSkills(a.sessionId)).find((skill) => skill.name === NAME);
if (!rereadA || rereadA.content_hash !== recordA.content_hash) {
  throw new Error("session A reports the new version");
}

// Reload A so the sheet is not reading a cached query, then prove it again.
await openSession(a.projectId, a.sessionId);
await page.getByTestId("session-logs-open").click();
await page.waitForTimeout(500);
await shot("07-session-a-after-edit.png");
await page.keyboard.press("Escape");

// --- session B, created after the edit ------------------------------------
const b = await newSession("session-b");
await openSession(b.projectId, b.sessionId);
const recordB = await waitForSkill(b.sessionId);
console.log("[session B] record", recordB.version, recordB.content_hash.slice(0, 12));
if (recordB.content_hash === recordA.content_hash) throw new Error("new session got the old version");

await page.getByTestId("session-logs-open").click();
const recordedB = page.locator(`[data-testid="session-skill-item"][data-name="${NAME}"]`);
await recordedB.waitFor();
await recordedB.scrollIntoViewIfNeeded();
await page.waitForTimeout(300);
await shot("08-session-b-version.png");
console.log("[session B] sheet shows", await recordedB.innerText());

const summary = {
  skill: { id: skillId, name: NAME, version: created.skill.version },
  sessionA: { ...a, ...recordA, mountedBody: "VERSION-ONE", afterEdit: afterEdit.content_hash },
  sessionB: { ...b, ...recordB, mountedBody: "VERSION-TWO" },
  agent: { turnCarriedSkillBlock: true, reply },
  versionsDiffer: recordA.content_hash !== recordB.content_hash,
  uploadHashMatchesB: replaced.skill.content_hash === recordB.content_hash,
};
writeFileSync(path.join(ARTIFACTS, "summary.json"), JSON.stringify(summary, null, 2));
console.log("[summary]", JSON.stringify(summary, null, 2));

await browser.close();
