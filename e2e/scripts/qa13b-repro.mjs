// Reproduction probe for the four remaining QA-13 MAJOR findings.
//
// It runs against the deployed app (default :7722) and writes screenshots plus a
// JSON record under e2e/.artifacts/fix-qa13b/repro/. It only creates data with a
// unique stamp, so it can be run repeatedly against the same deployment.
//
//   node e2e/scripts/qa13b-repro.mjs
//   BASE=http://127.0.0.1:3100 node e2e/scripts/qa13b-repro.mjs
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-qa13b/repro");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const stamp = Date.now();
const record = { base: BASE, stamp, findings: {} };
const save = () => writeFileSync(path.join(out, "repro-log.json"), JSON.stringify(record, null, 2));

const shot = async (page, name) => {
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false });
  console.log("[shot]", `${name}.png`);
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
const csrf = async (page) =>
  csrfOf(await page.context().cookies()) || (await page.evaluate(() => null));

async function login(page, email = "dev@piforge.local", password = "piforge-dev-123") {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(email);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
}

async function api(page, method, url, data) {
  return page.request.fetch(`${BASE}${url}`, {
    method,
    headers: { "X-PF-CSRF": await csrf(page) },
    data,
  });
}

const browser = await chromium.launch({ channel: "chrome" });

// ---------------------------------------------------------------------------
// A — settings navigation + palette trigger.
// ---------------------------------------------------------------------------
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await login(page);
  await page.getByRole("link", { name: "设置", exact: true }).first().click();
  await page.waitForURL(/\/settings/, { timeout: 20_000 });
  await page.waitForTimeout(800);
  const landing = page.url();
  const navLinks = await page
    .locator('a[href^="/settings"]')
    .evaluateAll((els) => els.map((el) => ({ href: el.getAttribute("href"), text: el.innerText.trim() })));
  const settingsNavCount = await page.locator('[data-testid="settings-nav"]').count();
  const paletteTrigger = await page
    .locator('[data-testid="command-palette-trigger"]')
    .evaluateAll((els) => els.map((el) => ({ text: el.innerText.trim(), visible: !!(el.offsetWidth || el.offsetHeight) })));
  await shot(page, "A-01-settings-landing");

  // All four settings pages: is any nav visible there?
  const perPage = {};
  for (const route of ["providers", "skills", "mcp", "api-keys"]) {
    await page.goto(`${BASE}/settings/${route}`);
    await page.waitForTimeout(500);
    perPage[route] = {
      settingsLinks: await page.locator('a[href^="/settings"]').count(),
      navBoxes: await page.locator('[data-testid="settings-nav"]').count(),
      headings: (await page.locator("h1").allInnerTexts()).map((s) => s.trim()),
    };
    await shot(page, `A-02-settings-${route}`);
  }

  // The palette is the only way to the pages: prove it still opens with Ctrl+K.
  await page.keyboard.press("Control+k");
  const paletteVisible = await page.getByRole("dialog").isVisible().catch(() => false);
  await shot(page, "A-03-palette-open");

  record.findings.A = {
    landing,
    navLinks,
    settingsNavCount,
    paletteTrigger,
    perPage,
    paletteVisible,
  };
  save();
  console.log("[A]", JSON.stringify(record.findings.A, null, 2));
  await page.close();
}

// ---------------------------------------------------------------------------
// B — English errors in a Chinese UI.
// ---------------------------------------------------------------------------
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill("dev@piforge.local");
  await page.getByTestId("login-password").fill("definitely-wrong");
  await page.getByTestId("login-submit").click();
  await page.getByTestId("login-error").waitFor({ timeout: 20_000 });
  const wrongPassword = await page.getByTestId("login-error").innerText();
  await shot(page, "B-01-bad-login");
  record.findings.B = { wrongPassword };

  await login(page);

  // A provider with no key: create one through the API, probe it, then delete it.
  const providerName = `qa13b-nokey-${stamp}`;
  const created = await api(page, "POST", "/v1/providers", {
    name: providerName,
    api: "openai-compat",
    base_url: "https://example.invalid/v1",
    models: [{ id: "test-model" }],
  });
  const createdBody = await created.json();
  record.findings.B.providerCreateStatus = created.status();
  const providerId = createdBody?.provider?.id;
  if (providerId) {
    const probe = await api(page, "POST", `/v1/providers/${providerId}/test`, {});
    record.findings.B.providerProbe = { status: probe.status(), body: await probe.json() };
    await page.goto(`${BASE}/settings/providers`);
    await page.waitForTimeout(600);
    const row = page.locator(`[data-testid="provider-item"][data-name="${providerName}"]`);
    await row.waitFor({ timeout: 20_000 });
    await row.getByRole("button", { name: "测试" }).click();
    await page.waitForTimeout(1_200);
    record.findings.B.providerToast = await page.locator("[data-sonner-toast]").allInnerTexts();
    await shot(page, "B-02-provider-test");
    await api(page, "DELETE", `/v1/providers/${providerId}`);
  }

  // A session with no provider configured: the send error names the wrong menu.
  const project = await api(page, "POST", "/v1/projects", {
    name: `qa13b-send-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = (await project.json()).project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "qa13b send",
  });
  const sessionId = (await session.json()).session.id;
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await page.getByTestId("composer-input").fill("hello");
  await page.getByTestId("composer-send").click();
  await page.waitForTimeout(2_000);
  record.findings.B.sendToast = await page.locator("[data-sonner-toast]").allInnerTexts();
  await shot(page, "B-03-send-error");
  save();
  console.log("[B]", JSON.stringify(record.findings.B, null, 2));
  await page.close();
}

// ---------------------------------------------------------------------------
// C — a nonsense repo URL creates a project, then a worktree-less session.
// ---------------------------------------------------------------------------
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await login(page);
  const before = await (await page.request.get(`${BASE}/v1/projects`)).json();

  // Through the UI, exactly the QA flow.
  await page.goto(`${BASE}/`);
  await page.getByTestId("app-sidebar").waitFor({ timeout: 20_000 });
  await page.getByRole("button", { name: "新建项目" }).first().click();
  await page.getByTestId("project-name").fill(`qa13b-badrepo-${stamp}`);
  await page.getByTestId("project-repo").fill("not-a-repo-url");
  await page.getByTestId("project-submit").click();
  await page.waitForTimeout(1_500);
  const after = await (await page.request.get(`${BASE}/v1/projects`)).json();
  const created = after.projects.find((p) => p.name === `qa13b-badrepo-${stamp}`);
  record.findings.C = {
    projectsBefore: before.projects.length,
    projectsAfter: after.projects.length,
    created: created ? { id: created.id, repo_url: created.repo_url } : null,
    toast: await page.locator("[data-sonner-toast]").allInnerTexts(),
    urlAfterSubmit: page.url(),
  };
  await shot(page, "C-01-bad-repo-created");

  if (created) {
    const sessionRes = await api(page, "POST", `/v1/projects/${created.id}/sessions`, {
      title: "qa13b bad repo session",
    });
    const sessionBody = await sessionRes.json();
    record.findings.C.sessionCreate = { status: sessionRes.status(), body: sessionBody };
    const sessionId = sessionBody?.session?.id;
    if (sessionId) {
      // Count the workspace requests over five seconds to see the retry loop.
      const requests = [];
      const onResponse = (res) => {
        if (/\/v1\/sessions\/[^/]+\/(files|git|pty|browser)/.test(res.url())) {
          requests.push({ at: Date.now(), status: res.status(), url: res.url().replace(BASE, "") });
        }
      };
      page.on("response", onResponse);
      await page.goto(`${BASE}/p/${created.id}/s/${sessionId}`);
      await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
      await page.waitForTimeout(5_000);
      page.off("response", onResponse);
      record.findings.C.workspaceRequests = requests;
      record.findings.C.panelText = await page.locator('[data-testid="workspace-panels"]').innerText();
      record.findings.C.bodyText = (await page.locator("body").innerText()).slice(0, 2000);
      await shot(page, "C-02-no-worktree");
    }
  }
  save();
  console.log("[C]", JSON.stringify(record.findings.C, null, 2));
  await page.close();
}

// ---------------------------------------------------------------------------
// D — compare from one session.
// ---------------------------------------------------------------------------
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await login(page);
  const project = await api(page, "POST", "/v1/projects", {
    name: `qa13b-compare-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = (await project.json()).project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "qa13b compare solo",
  });
  const sessionId = (await session.json()).session.id;
  await page.goto(`${BASE}/p/${projectId}`);
  await page.getByTestId("session-link").first().waitFor({ timeout: 30_000 });
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });

  await page.getByTestId("session-actions").first().click();
  await page.getByTestId("session-compare").click();
  await page.waitForTimeout(1_500);
  record.findings.D = {
    url: page.url(),
    heading: await page.locator("h1").first().innerText().catch(() => ""),
    columns: await page.getByTestId("compare-column").count(),
    body: (await page.locator("body").innerText()).slice(0, 1200),
  };
  await shot(page, "D-01-compare-self");
  save();
  console.log("[D]", JSON.stringify(record.findings.D, null, 2));
  await page.close();
}

await browser.close();
console.log("[done]", path.join(out, "repro-log.json"));
