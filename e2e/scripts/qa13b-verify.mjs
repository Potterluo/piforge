// Verification probe for the four remaining QA-13 MAJOR findings on the
// deployed app (default :7722). It asserts the fixed behaviour and writes the
// screenshots the report refers to under e2e/.artifacts/fix-qa13b/.
//
//   node e2e/scripts/qa13b-verify.mjs
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-qa13b");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const stamp = Date.now();
const record = { base: BASE, stamp, checks: {} };
const save = () => writeFileSync(path.join(out, "verify-log.json"), JSON.stringify(record, null, 2));

const shot = async (page, name) => {
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false });
  console.log("[shot]", `${name}.png`);
};

const ok = (condition, message) => {
  if (!condition) throw new Error(`FAILED: ${message}`);
  console.log("[ok]", message);
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";

async function login(page, email = "dev@piforge.local", password = "piforge-dev-123") {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(email);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
}

async function api(page, method, url, data) {
  return page.request.fetch(`${BASE}${url}`, {
    method,
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    data,
  });
}

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

// ---------------------------------------------------------------------------
// A — settings navigation and the visible palette trigger.
// ---------------------------------------------------------------------------
{
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill("dev@piforge.local");
  await page.getByTestId("login-password").fill("piforge-dev-123");
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });

  await page.getByRole("link", { name: "设置", exact: true }).first().click();
  await page.waitForURL(/\/settings\/providers$/, { timeout: 20_000 });
  await page.getByTestId("settings-nav").waitFor({ timeout: 20_000 });

  const states = {};
  for (const route of ["providers", "skills", "mcp", "api-keys", "general"]) {
    await page.goto(`${BASE}/settings/${route}`);
    await page.getByTestId(`settings-nav-${route}`).waitFor({ timeout: 20_000 });
    const active = await page.getByTestId(`settings-nav-${route}`).getAttribute("aria-current");
    const navVisible = await page.getByTestId("settings-nav").first().isVisible();
    ok(navVisible, `settings navigation is visible on /settings/${route}`);
    ok(active === "page", `current settings item is marked on /settings/${route}`);
    states[route] = { active, heading: (await page.locator("h1").first().innerText()).trim() };
  }
  record.checks.A = {
    states,
    navLinks: await page.getByTestId("settings-nav").first().getByRole("link").allInnerTexts(),
  };
  await shot(page, "A-01-settings-nav");

  // Every settings page keeps the same navigation (the screenshot above is 通用).
  await page.getByTestId("settings-nav-providers").click();
  await page.waitForURL(/\/settings\/providers$/, { timeout: 20_000 });
  await shot(page, "A-02-settings-providers-nav");

  const trigger = page.locator('[data-testid="command-palette-trigger"]:visible');
  await trigger.waitFor({ timeout: 20_000 });
  ok((await trigger.getAttribute("aria-label")) === "打开命令面板", "palette trigger has an accessible name");
  await shot(page, "A-03-palette-trigger");
  await trigger.click();
  const search = page.locator('input[placeholder="搜索命令、项目…"]:visible');
  await search.waitFor({ timeout: 10_000 });
  await shot(page, "A-04-palette-open");
  await page.keyboard.press("Escape");
  record.checks.A.paletteTrigger = (await trigger.getAttribute("aria-label")) ?? "";
  save();
}

// ---------------------------------------------------------------------------
// B — Chinese errors and the real menu name.
// ---------------------------------------------------------------------------
{
  const bad = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await bad.goto(`${BASE}/login`);
  await bad.getByTestId("login-email").fill("dev@piforge.local");
  await bad.getByTestId("login-password").fill("definitely-wrong");
  await bad.getByTestId("login-submit").click();
  await bad.getByTestId("login-error").waitFor({ timeout: 20_000 });
  const wrongPassword = await bad.getByTestId("login-error").innerText();
  ok(wrongPassword.includes("邮箱或密码不正确"), "wrong password renders Chinese");
  await shot(bad, "B-01-bad-login");
  await bad.close();

  // A provider with no key: create it, press 测试, read the toast.
  const providerName = `verify-nokey-${stamp}`;
  const created = await api(page, "POST", "/v1/providers", {
    name: providerName,
    api: "openai-compat",
    base_url: "https://example.invalid/v1",
    models: [{ id: "test-model" }],
  });
  const providerId = (await created.json())?.provider?.id;
  await page.goto(`${BASE}/settings/providers`);
  const row = page.locator(`[data-testid="provider-item"][data-name="${providerName}"]`);
  await row.waitFor({ timeout: 20_000 });
  await row.getByRole("button", { name: "测试" }).click();
  const probeToast = page.locator("[data-sonner-toast]").filter({ hasText: "连通性测试失败" });
  await probeToast.waitFor({ timeout: 20_000 });
  // Screenshot after the enter animation: sonner anchors the toast below the
  // viewport until it slides in, and a frame taken too early shows nothing.
  await page.waitForTimeout(700);
  await shot(page, "B-02-provider-no-key");
  const probeText = await probeToast.innerText();
  ok(probeText.includes("还没有 API Key"), "provider without a key renders Chinese");
  ok(!probeText.includes("this provider has no API key yet"), "raw provider English is not shown");
  if (providerId) await api(page, "DELETE", `/v1/providers/${providerId}`);

  // The send error must name 模型与凭据, not Settings → Models.
  const project = await api(page, "POST", "/v1/projects", {
    name: `verify-send-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = (await project.json()).project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "verify send" });
  const sessionId = (await session.json()).session.id;
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await page.getByTestId("composer-input").fill("hello");
  await page.getByTestId("composer-send").click();
  const sendToast = page.locator("[data-sonner-toast]").filter({ hasText: "发送失败" });
  await sendToast.waitFor({ timeout: 20_000 });
  await page.waitForTimeout(700);
  await shot(page, "B-03-send-error");
  const sendText = await sendToast.innerText();
  ok(sendText.includes("模型与凭据"), "send error names 模型与凭据");
  ok(!sendText.includes("Settings"), "send error does not say Settings");
  ok(!sendText.includes("Models"), "send error does not say Models");
  record.checks.B = { wrongPassword, probeText, sendText };
  await api(page, "DELETE", `/v1/projects/${projectId}`);
  save();
}

// ---------------------------------------------------------------------------
// C — repo validation and the no-worktree recovery state.
// ---------------------------------------------------------------------------
{
  await page.goto(`${BASE}/`);
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 20_000 });
  await page.getByRole("button", { name: "新建项目" }).first().click();
  const badName = `verify-bad-repo-${stamp}`;
  await page.getByTestId("project-name").fill(badName);
  await page.getByTestId("project-repo").fill("not-a-repo-url");
  await page.getByTestId("project-submit").click();
  const fieldError = page.getByTestId("project-repo-error");
  await fieldError.waitFor({ timeout: 10_000 });
  const fieldText = await fieldError.innerText();
  ok(fieldText.includes("仓库地址格式不支持"), "bad repo URL is refused in the form, in Chinese");
  const projectsNow = await (await page.request.get(`${BASE}/v1/projects`)).json();
  ok(!projectsNow.projects.some((p) => p.name === badName), "no project was created for the bad URL");
  await shot(page, "C-01-repo-field-error");
  await page.getByRole("button", { name: "取消" }).click();

  // A syntactically valid but unreachable repository produces the legacy row.
  const project = await api(page, "POST", "/v1/projects", {
    name: `verify-no-worktree-${stamp}`,
    repo_url: "https://127.0.0.1:1/acme/nope.git",
    default_branch: "main",
  });
  const projectId = (await project.json()).project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "no worktree" });
  const sessionId = (await session.json()).session.id;
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  const state = page.getByTestId("no-worktree-state");
  await state.waitFor({ timeout: 40_000 });
  await shot(page, "C-02-no-worktree");
  ok((await page.getByTestId("workspace-panels").count()) === 0, "workspace panels are not mounted");
  ok((await page.getByText("this session has no worktree yet").count()) === 0, "no English 422 text");
  ok((await state.getByRole("button", { name: "重试" }).count()) === 0, "no retry loop");

  await page.getByTestId("no-worktree-fix").click();
  await page.getByTestId("no-worktree-repo-input").fill("https://example.com/acme/fixed.git");
  await page.getByTestId("no-worktree-save").click();
  await page.locator("[data-sonner-toast]").filter({ hasText: "仓库地址已保存" }).waitFor({ timeout: 20_000 });
  await shot(page, "C-03-repo-fixed");
  const patched = await (await page.request.get(`${BASE}/v1/projects/${projectId}`)).json();
  ok(patched.project.repo_url === "https://example.com/acme/fixed.git", "repo URL was saved");
  record.checks.C = { fieldText, repoAfterFix: patched.project.repo_url };

  await page.getByTestId("no-worktree-delete").click();
  await page.getByTestId("no-worktree-delete-confirm").click();
  await page.waitForURL(`${BASE}/`, { timeout: 20_000 });
  const gone = await page.request.get(`${BASE}/v1/projects/${projectId}`);
  ok(gone.status() === 404, "deleted project is gone");
  save();
}

// ---------------------------------------------------------------------------
// D — compare chooser and duplicate rejection.
// ---------------------------------------------------------------------------
{
  const project = await api(page, "POST", "/v1/projects", {
    name: `verify-compare-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = (await project.json()).project.id;
  const first = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "尝试 A" });
  const firstId = (await first.json()).session.id;
  const second = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "尝试 B" });
  const secondId = (await second.json()).session.id;

  await page.goto(`${BASE}/p/${projectId}/s/${firstId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await page.locator(`[data-session-actions="${firstId}"]`).click();
  await page.getByTestId("session-compare").click();
  await page.getByTestId("compare-chooser").waitFor({ timeout: 20_000 });
  const selfCandidate = await page
    .locator(`[data-testid="compare-candidate"][data-session-id="${firstId}"]`)
    .count();
  ok(selfCandidate === 0, "the session is not offered as its own comparison");
  await shot(page, "D-01-compare-chooser");
  await page.locator(`[data-testid="compare-candidate"][data-session-id="${secondId}"]`).click();
  await page.getByTestId("compare-start").click();
  await page.getByTestId("compare-grid").waitFor({ timeout: 40_000 });
  const columns = await page.getByTestId("compare-column").count();
  ok(columns === 2, "two different sessions are compared");
  ok(page.url().includes(`ids=${firstId},${secondId}`), "the URL carries the two distinct ids");
  await shot(page, "D-02-compare-two");

  await page.goto(`${BASE}/p/${projectId}/compare?ids=${firstId},${firstId}`);
  const invalid = page.getByTestId("compare-invalid");
  await invalid.waitFor({ timeout: 20_000 });
  ok((await invalid.innerText()).includes("不能拿同一个会话和自己对比"), "duplicate ids are rejected");
  ok((await page.getByTestId("compare-column").count()) === 0, "duplicate ids render no columns");
  await shot(page, "D-03-compare-invalid");

  const refused = await page.request.get(`${BASE}/v1/sessions/compare?ids=${firstId},${firstId}`);
  ok(refused.status() === 400, "the API rejects duplicate ids");
  record.checks.D = { columns, duplicateStatus: refused.status(), url: page.url() };
  await api(page, "DELETE", `/v1/projects/${projectId}`);
  save();
}

await browser.close();
console.log("[done]", path.join(out, "verify-log.json"));
