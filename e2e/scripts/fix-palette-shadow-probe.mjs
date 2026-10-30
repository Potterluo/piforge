// Deployed-app verification for the two fixes in this batch.
//
//   node e2e/scripts/fix-palette-shadow-probe.mjs
//
// FIX 1 (palette): Ctrl+K → type a session title → Enter with no arrow key,
// while the projects query is deliberately slower than the session search (the
// exact ordering that used to clear `aria-selected`). Prints the selected id
// count before Enter and whether Enter opened the session.
//
// FIX 2 (shadowing): an admin creates a shared skill, a fresh normal user
// creates a private skill with the same name. Checks the API's `effective`
// flag, the settings rows (which one is labelled in effect / shadowed), the `/`
// palette (one row, not two indistinguishable ones), what a new session mounts
// and records, then disables the private copy and checks the fallback.
//
// Screenshots and a JSON log land in e2e/.artifacts/fix-palette-shadow/.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-palette-shadow");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const ADMIN = { email: "admin@piforge.local", password: "piforge-admin-123" };
const stamp = Date.now();

const record = { base: BASE, stamp, checks: {}, findings: [] };
const save = () => writeFileSync(path.join(out, "probe-log.json"), JSON.stringify(record, null, 2));
const note = (key, value) => {
  record.checks[key] = value;
  console.log(`[${key}]`, JSON.stringify(value));
};
const ok = (condition, message) => {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  record.findings.push({ ok: Boolean(condition), message });
  save();
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

const browser = await chromium.launch({ channel: "chrome" });

try {
  // =====================================================================
  // FIX 1 — palette selection with an async label change
  // =====================================================================
  const palettePage = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  try {
    await login(palettePage, USER);
    const token = `Psel-${stamp}`;
    const project = await api(palettePage, "POST", "/v1/projects", {
      name: `palette-probe-${stamp}`,
      repo_url: "",
      default_branch: "main",
    });
    const projectId = project.body.project.id;
    const session = await api(palettePage, "POST", `/v1/projects/${projectId}/sessions`, {
      title: `${token} 检查缓存淘汰`,
    });
    const sessionId = session.body.session.id;

    // Hold the projects query so the session row first renders with the generic
    // "项目" label and gains its real project name after it is already selected.
    await palettePage.route("**/v1/projects*", async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 900));
      await route.continue();
    });

    await palettePage.goto(`${BASE}/`);
    await palettePage.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
    await palettePage.keyboard.press("Control+k");
    const dialog = palettePage.getByRole("dialog");
    await dialog.waitFor({ timeout: 10_000 });
    const input = dialog.getByPlaceholder("搜索命令、项目、会话…");
    await input.fill(token);
    const row = palettePage.getByTestId("palette-session").first();
    await row.waitFor({ timeout: 20_000 });
    // Forces the projects query to have answered: the label change that used to
    // invalidate the selection happens before Enter.
    await palettePage.getByText(`palette-probe-${stamp}`).first().waitFor({ timeout: 20_000 });
    await palettePage.waitForTimeout(300);

    const state = await palettePage.evaluate(() => {
      const options = [...document.querySelectorAll('[role="option"]')];
      const visible = options.filter((node) => node.getBoundingClientRect().height > 0);
      const selected = visible.filter((node) => node.getAttribute("aria-selected") === "true");
      const input = document.querySelector('[role="combobox"]');
      return {
        visible: visible.length,
        selected: selected.length,
        selectedLabels: selected.map((node) => (node.textContent ?? "").trim().slice(0, 80)),
        activeDescendant: input?.getAttribute("aria-activedescendant") ?? null,
      };
    });
    note("palette.state", state);
    await palettePage.screenshot({ path: path.join(out, "palette-before-enter.png") });

    ok(state.visible >= 1, "the typed query leaves at least one visible row");
    ok(state.selected === 1, "exactly one visible row is aria-selected after the list settles");
    ok(
      Boolean(state.activeDescendant),
      "the combobox points aria-activedescendant at the selected row",
    );

    await input.press("Enter");
    await palettePage
      .waitForURL(new RegExp(`/p/${projectId}/s/${sessionId}$`), { timeout: 10_000 })
      .catch(() => {});
    const opened = palettePage.url().endsWith(`/p/${projectId}/s/${sessionId}`);
    note("palette.urlAfterEnter", palettePage.url());
    ok(opened, "Ctrl+K → type → Enter opens the session without pressing an arrow key");
    if (opened) await palettePage.screenshot({ path: path.join(out, "palette-opened.png") });
  } finally {
    await palettePage.close();
  }

  // =====================================================================
  // FIX 2 — private skill shadows the shared one
  // =====================================================================
  const adminPage = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const userContext = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const userPage = await userContext.newPage();
  const skillName = `review-${stamp}`;
  let sharedId = null;
  try {
    await login(adminPage, ADMIN);
    const shared = await api(adminPage, "POST", "/v1/skills", {
      scope: "admin",
      name: skillName,
      version: "1.0.0",
      description: "共享审查口径",
      body: `---\nname: ${skillName}\ndescription: 共享审查口径\n---\n\n# 共享版本\n`,
    });
    ok(shared.status === 201, "an admin creates the shared skill");
    sharedId = shared.body.skill.id;

    const email = `probe-shadow-${stamp}@example.test`;
    const created = await api(adminPage, "POST", "/v1/admin/users", {
      email,
      password: "probe-shadow-123",
      display_name: "probe shadow",
      role: "user",
    });
    ok(created.status === 201, "the drill creates a throwaway normal user");

    // The user already lives in a separate browser context, so the admin cookie
    // cannot leak into their page and their skill list is exactly their own.
    await login(userPage, { email, password: "probe-shadow-123" });

    const mine = await api(userPage, "POST", "/v1/skills", {
      name: skillName,
      version: "2.0.0",
      description: "我的审查口径",
      body: `---\nname: ${skillName}\ndescription: 我的审查口径\n---\n\n# 私有版本\n`,
    });
    ok(mine.status === 201, "the user creates a private skill with the same name");
    const privateId = mine.body.skill.id;

    const listed = await api(userPage, "GET", "/v1/skills");
    const rows = listed.body.skills.filter((skill) => skill.name === skillName);
    note(
      "skills.rows",
      rows.map((skill) => ({
        id: skill.id,
        scope: skill.scope,
        enabled: skill.enabled,
        effective: skill.effective,
      })),
    );
    ok(rows.length === 2, "both rows are listed (the shared one is not hidden from the UI)");
    const privateRow = rows.find((skill) => skill.id === privateId);
    const sharedRow = rows.find((skill) => skill.id === sharedId);
    ok(
      privateRow?.effective === true && sharedRow?.effective === false,
      "the API marks the private row effective and the shared row shadowed",
    );

    await userPage.goto(`${BASE}/settings/skills`);
    // Select by the visible scope badge rather than data-scope: the old build
    // has no such attribute, and the probe must still be able to describe it.
    const settingsRows = userPage.locator(
      `[data-testid="skill-item"][data-name="${skillName}"]`,
    );
    const settingsPrivate = settingsRows.filter({
      has: userPage.getByText("私有", { exact: true }),
    });
    const settingsShared = settingsRows.filter({
      has: userPage.getByText("共享", { exact: true }),
    });
    await settingsPrivate.waitFor({ timeout: 20_000 });
    const privateText = (await settingsPrivate.textContent()) ?? "";
    const sharedText = (await settingsShared.textContent()) ?? "";
    note("settings.privateRow", privateText.replace(/\s+/g, " ").trim().slice(0, 200));
    note("settings.sharedRow", sharedText.replace(/\s+/g, " ").trim().slice(0, 200));
    await userPage.screenshot({ path: path.join(out, "shadow-settings.png") });

    ok(
      privateText.includes("生效中") && sharedText.includes("覆盖"),
      "the list says which copy is in effect and that the shared one is shadowed",
    );

    // The palette must offer one row per name, with the version that mounts.
    await userPage.goto(`${BASE}/`);
    await userPage.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
    const newSession = await api(userPage, "POST", "/v1/projects", {
      name: `shadow-probe-${stamp}`,
      repo_url: "",
      default_branch: "main",
    });
    const projectId = newSession.body.project.id;
    const session = await api(userPage, "POST", `/v1/projects/${projectId}/sessions`, {
      title: "shadow probe",
    });
    const sessionId = session.body.session.id;
    await userPage.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
    await userPage.getByTestId("composer-input").waitFor({ timeout: 40_000 });
    await userPage.getByTestId("composer-input").click();
    await userPage.keyboard.type(`/${skillName}`);
    await userPage.waitForTimeout(800);
    const options = await userPage
      .getByTestId("skill-palette-item")
      .evaluateAll((nodes) =>
        nodes.map((node) => ({
          name: node.getAttribute("data-name"),
          version: node.getAttribute("data-version"),
          overrides: node.getAttribute("data-overrides"),
          text: (node.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 120),
        })),
      );
    note("palette.options", options);
    await userPage.screenshot({ path: path.join(out, "shadow-palette.png") });
    ok(
      options.length === 1 && options[0].version === "2.0.0",
      "the / palette offers exactly the effective (private) row, at its version",
    );
    ok(
      options.some((option) => (option.text ?? "").includes("覆盖同名共享")),
      "the palette says the private row overrides the shared one",
    );
    await userPage.keyboard.press("Escape");

    const mounted = await userPage.request.get(
      `${BASE}/v1/sessions/${sessionId}/files?path=${encodeURIComponent(`.pi/skills/${skillName}/SKILL.md`)}`,
    );
    const mountedBody = mounted.status() === 200 ? ((await mounted.json()).body ?? "") : "";
    note("mounted.private", mountedBody.replace(/\s+/g, " ").trim());
    ok(mountedBody.includes("# 私有版本"), "a new session mounts the private bundle");

    const records = await userPage.request.get(`${BASE}/v1/sessions/${sessionId}/skills`);
    const recorded = (await records.json()).skills.find((row) => row.name === skillName);
    note("sessionSkill.private", recorded);
    ok(recorded?.skill_id === privateId, "session_skills records the private row (FR-14.5)");

    // Disable the private copy: the shared one must take over, and the list must
    // say so instead of making the skill look deleted.
    const disabled = await api(userPage, "PATCH", `/v1/skills/${privateId}`, { enabled: false });
    ok(disabled.status === 200, "the user disables their private copy");

    const second = await api(userPage, "POST", "/v1/projects", {
      name: `shadow-fallback-${stamp}`,
      repo_url: "",
      default_branch: "main",
    });
    const secondSession = await api(userPage, "POST", `/v1/projects/${second.body.project.id}/sessions`, {
      title: "shadow fallback",
    });
    const secondId = secondSession.body.session.id;
    const fallback = await userPage.request.get(
      `${BASE}/v1/sessions/${secondId}/files?path=${encodeURIComponent(`.pi/skills/${skillName}/SKILL.md`)}`,
    );
    const fallbackBody = fallback.status() === 200 ? ((await fallback.json()).body ?? "") : "";
    note("mounted.fallback", fallbackBody.replace(/\s+/g, " ").trim());
    ok(fallbackBody.includes("# 共享版本"), "the next session mounts the shared bundle again");

    const secondRecords = await userPage.request.get(`${BASE}/v1/sessions/${secondId}/skills`);
    const secondRecorded = (await secondRecords.json()).skills.find((row) => row.name === skillName);
    note("sessionSkill.fallback", secondRecorded);
    ok(secondRecorded?.skill_id === sharedId, "session_skills records the shared fallback row");

    await userPage.goto(`${BASE}/settings/skills`);
    await settingsPrivate.waitFor({ timeout: 20_000 });
    const fallbackText = (await settingsPrivate.textContent()) ?? "";
    note("settings.privateRowAfterDisable", fallbackText.replace(/\s+/g, " ").trim().slice(0, 200));
    ok(
      fallbackText.includes("已停用") && fallbackText.includes("共享版本"),
      "the disabled private row says the shared version is in use",
    );
    await userPage.screenshot({ path: path.join(out, "shadow-fallback.png") });

  } finally {
    if (sharedId) {
      const cleanup = await api(adminPage, "DELETE", `/v1/skills/${sharedId}`);
      note("cleanup.sharedSkill", cleanup.status);
    }
    await adminPage.close();
    await userContext.close();
  }
} finally {
  await browser.close();
}

const broken = record.findings.filter((finding) => !finding.ok);
console.log(
  broken.length === 0
    ? `[result] all ${record.findings.length} checks passed`
    : `[result] ${broken.length}/${record.findings.length} checks FAILED`,
);
save();
process.exitCode = broken.length === 0 ? 0 : 1;
