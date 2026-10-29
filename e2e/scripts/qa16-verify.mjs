// Verification probe for the four QA-16 findings, run against the deployed app
// (default :7722). It exercises the user's own paths — the admin provider row,
// the project kebab, the compact button, the browser panel — and asserts the
// fixed behaviour. The assertions throw on regression so CI-ish runs fail loudly.
//
//   node e2e/scripts/qa16-verify.mjs
//
// Screenshots and a JSON log land in e2e/.artifacts/fix-qa16/.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-qa16");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const stamp = Date.now();
const record = { base: BASE, stamp, checks: {} };
const save = () => writeFileSync(path.join(out, "verify-log.json"), JSON.stringify(record, null, 2));

const shot = async (page, name) => {
  // Dialogs/toasts animate in; fast-forwarding them keeps the evidence readable
  // instead of catching a half-transparent dialog over the page behind it.
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false, animations: "disabled" });
  console.log("[shot]", `${name}.png`);
};

const check = (condition, message, detail) => {
  if (!condition) {
    record.checks.failed = message;
    save();
    throw new Error(`[BROKEN] ${message}${detail ? `: ${JSON.stringify(detail)}` : ""}`);
  }
  console.log("[ok]", message);
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";

async function login(page, email, password) {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(email);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
}

async function api(page, method, url, data) {
  const res = await page.request.fetch(`${BASE}${url}`, {
    method,
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    data,
  });
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status(), body };
}

const waitSettled = async (page, timeout = 180_000) => {
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") === "settled",
    undefined,
    { timeout },
  );
};

async function send(page, text, { wait = true } = {}) {
  const before = await page.getByTestId("turn-user").count();
  await page.getByTestId("composer-input").fill(text);
  await page.getByTestId("composer-send").click();
  await page.getByTestId("turn-user").nth(before).waitFor({ timeout: 30_000 });
  if (wait) await waitSettled(page);
}

async function newProject(page, name) {
  const created = await api(page, "POST", "/v1/projects", {
    name,
    repo_url: "",
    default_branch: "main",
  });
  check(created.status < 300, `project ${name} created`, created);
  return created.body.project.id;
}

async function newSession(page, projectId, title, modelRef) {
  const created = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title,
    model_ref: modelRef,
  });
  check(created.status < 300, `session ${title} created`, created);
  return created.body.session.id;
}

const browser = await chromium.launch({ channel: "chrome" });
const adminPage = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

await login(adminPage, "admin@piforge.local", "piforge-admin-123");
await login(page, "dev@piforge.local", "piforge-dev-123");

// ---------------------------------------------------------------------------
// FIX A — an admin edits a shared provider in place; pinned sessions survive.
// ---------------------------------------------------------------------------
{
  const name = `qa16-edit-${stamp}`;
  const shared = await api(adminPage, "POST", "/v1/admin/providers", {
    name,
    api: "openai-compat",
    // A local endpoint that answers fast, so a pinned message gets a provider
    // error instead of a DNS timeout. The edit changes it to an invalid host.
    base_url: `${BASE}/healthz`,
    api_key: "sk-qa16-original",
    models: [{ id: "qa16-model-a" }],
  });
  check(shared.status === 200, "shared provider shared", shared);
  const providerId = shared.body.provider.id;

  const projectId = await newProject(adminPage, `qa16-shared-proj-${stamp}`);
  const sessionId = await newSession(adminPage, projectId, "pinned to the shared provider", `${name}/qa16-model-a`);
  await adminPage.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await adminPage.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  // 200 means the message was accepted: a provider/model that does not resolve
  // is 400 no_model before any run starts.
  const before = await api(adminPage, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `qa16-before-${stamp}`,
    text: "只回答一个字：好。不要调用工具。",
  });
  check(before.status === 200 || before.status === 201, "pinned session accepted a message before the edit", before);
  // The provider endpoint is a local 404 and the agent retries it; stop the run
  // and wait for it to settle so the post-edit message is not refused as busy.
  await api(adminPage, "POST", `/v1/sessions/${sessionId}/interrupt`, {});
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const status = await api(adminPage, "GET", `/v1/sessions/${sessionId}/status`);
    if (status.body?.running === false) break;
    await adminPage.waitForTimeout(500);
  }

  await adminPage.goto(`${BASE}/settings/providers`);
  const row = adminPage.getByTestId("shared-provider-item").filter({ hasText: name });
  await row.waitFor({ timeout: 30_000 });
  await row.getByTestId("shared-provider-edit").click();
  await adminPage.getByTestId("shared-provider-edit-dialog").waitFor();
  check(
    (await adminPage.getByTestId("shared-provider-edit-key").inputValue()) === "",
    "the edit dialog leaves the key field empty so the stored key is kept",
  );
  await adminPage.getByTestId("shared-provider-edit-base").fill("https://qa16-edited.example.invalid/v1");
  await adminPage.getByTestId("shared-provider-edit-models").fill("qa16-model-a\nqa16-model-b");
  await shot(adminPage, "A-shared-provider-edit-dialog");
  await adminPage.getByTestId("shared-provider-edit-submit").click();
  await adminPage.locator("[data-sonner-toast]").filter({ hasText: "共享 provider 已更新" }).waitFor({ timeout: 30_000 });

  const listed = await api(adminPage, "GET", "/v1/admin/providers");
  const edited = listed.body.providers.find((provider) => provider.name === name);
  record.checks.sharedProvider = edited;
  check(edited && edited.id === providerId, "the edit kept the provider row id (pinned refs survive)", edited);
  check(edited.base_url === "https://qa16-edited.example.invalid/v1", "the edit changed the base URL", edited);
  check(edited.models.some((model) => model.id === "qa16-model-b"), "the edit changed the model list", edited);
  check(edited.has_key === true, "the key-less edit kept the stored key", edited);

  const session = await api(adminPage, "GET", `/v1/sessions/${sessionId}`);
  check(
    session.body.session.model_ref === `${name}/qa16-model-a`,
    "the pinned session still points at the same provider/model",
    session.body.session,
  );
  const after = await api(adminPage, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `qa16-after-${stamp}`,
    text: "只回答一个字：可以。不要调用工具。",
  });
  record.checks.messageAfterEdit = after;
  check(after.status === 200 || after.status === 201, "the pinned session still resolves and accepts a message after the edit", after);
  await adminPage.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await adminPage.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await shot(adminPage, "A-pinned-session-after-edit");

  await api(adminPage, "DELETE", `/v1/admin/providers/${providerId}`);
  await api(adminPage, "DELETE", `/v1/projects/${projectId}`);
}

// ---------------------------------------------------------------------------
// FIX B — renaming onto an existing name shows the server's sentence inline.
// ---------------------------------------------------------------------------
{
  const taken = `qa16-taken-${stamp}`;
  const mine = `qa16-rename-${stamp}`;
  const takenId = await newProject(page, taken);
  const mineId = await newProject(page, mine);

  await page.goto(`${BASE}/p/${mineId}`);
  await page.getByTestId("project-title").waitFor({ timeout: 30_000 });
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-rename").click();
  await page.getByTestId("project-rename-input").fill(taken);
  await page.getByTestId("project-rename-submit").click();
  const error = page.getByTestId("project-rename-error");
  await error.waitFor({ timeout: 20_000 });
  const text = (await error.innerText()).trim();
  record.checks.renameError = text;
  check(
    text === `项目名称「${taken}」已存在（不区分大小写），请换一个名称`,
    "the rename dialog shows the server's exact conflict sentence",
    text,
  );
  check(await page.getByTestId("project-rename-input").isVisible(), "the dialog stays open on the field");
  check(
    (await page.getByTestId("project-title").innerText()).trim() === mine,
    "the refused rename did not change the project",
  );
  await shot(page, "B-rename-conflict-inline");
  await page.keyboard.press("Escape");
  await api(page, "DELETE", `/v1/projects/${mineId}`);
  await api(page, "DELETE", `/v1/projects/${takenId}`);
}

// ---------------------------------------------------------------------------
// FIX C — a no-op compaction draws no marker.
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(page, `qa16-compact-${stamp}`);
  const sessionId = await newSession(page, projectId, "compact probe");
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await send(page, "只回答两个字：收到。不要调用工具。");

  const button = page.getByTestId("compact-context");
  await button.waitFor({ timeout: 30_000 });
  await page.waitForFunction(
    () => {
      const element = document.querySelector('[data-testid="compact-context"]');
      return element instanceof HTMLButtonElement && !element.disabled;
    },
    undefined,
    { timeout: 60_000 },
  );
  await button.click();
  const status = page.getByTestId("compact-status");
  await status.waitFor({ timeout: 60_000 });
  const statusText = (await status.innerText()).trim();
  record.checks.compactStatus = statusText;
  check(statusText === "没有可压缩的上下文", "the API reported nothing to compact", statusText);

  const transcript = await page.getByTestId("session-stream").innerText();
  record.checks.transcriptHasMarker = /上下文已压缩/.test(transcript);
  check(!/上下文已压缩/.test(transcript), "the transcript draws no compaction marker for a no-op", transcript.slice(-400));
  const entries = await page.request.get(`${BASE}/v1/sessions/${sessionId}/entries`);
  const entriesText = JSON.stringify(await entries.json());
  check(!entriesText.includes('"compaction"'), "the entries contain no compaction record either");
  await shot(page, "C-noop-no-marker");
  await api(page, "DELETE", `/v1/projects/${projectId}`);
}

// ---------------------------------------------------------------------------
// FIX D — 新标签页 is a blank page, and capturing it is refused with 空白页.
// ---------------------------------------------------------------------------
{
  const projectId = await newProject(page, `qa16-browser-${stamp}`);
  const sessionId = await newSession(page, projectId, "browser probe");
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await send(page, "只回答两个字：好的。不要调用工具。");

  await page.getByTestId("panel-tab-browser").click();
  const panel = page.getByTestId("browser-panel");
  await panel.waitFor({ timeout: 30_000 });
  await page.waitForFunction(
    () => document.querySelector('[data-testid="browser-panel"]')?.getAttribute("data-browser-status") === "open",
    undefined,
    { timeout: 90_000 },
  );
  await page.getByTestId("browser-url").fill("https://example.com/");
  await page.getByTestId("browser-url").press("Enter");
  await page.waitForFunction(
    () => document.querySelector('[data-testid="browser-panel"]')?.getAttribute("data-browser-painted") === "1",
    undefined,
    { timeout: 60_000 },
  );

  const tabsBefore = await page.getByTestId("browser-tab").count();
  await page.getByTestId("browser-tab-add").click();
  await page.waitForFunction(
    () => document.querySelector('[data-testid="browser-url"]')?.value === "about:blank",
    undefined,
    { timeout: 30_000 },
  );
  const tabsAfter = await page.getByTestId("browser-tab").count();
  record.checks.browserTabs = { before: tabsBefore, after: tabsAfter };
  check(tabsAfter > tabsBefore, "新标签页 opened a new tab", record.checks.browserTabs);
  const tabs = await (await page.request.get(`${BASE}/v1/sessions/${sessionId}/browser/tabs`)).json();
  record.checks.browserServerTabs = tabs;
  check(
    (tabs.tabs ?? []).some((tab) => tab.url === "about:blank"),
    "the new tab is really about:blank on the server",
    tabs,
  );
  await shot(page, "D-new-tab-about-blank");

  // Switching pages reconnects the viewer; the camera is enabled once the socket
  // is open again.
  await page.waitForFunction(
    () => document.querySelector('[data-testid="browser-panel"]')?.getAttribute("data-browser-status") === "open",
    undefined,
    { timeout: 90_000 },
  );
  await page.getByTestId("browser-screenshot").click();
  const toast = page.locator("[data-sonner-toast]").filter({ hasText: "截图失败" });
  await toast.waitFor({ timeout: 40_000 });
  const toastText = await toast.innerText();
  record.checks.blankCaptureToast = toastText;
  check(/空白/.test(toastText) && /about:blank/.test(toastText), "capturing the blank tab is refused with the 空白页 message", toastText);
  await shot(page, "D-blank-capture-refused");
  await api(page, "DELETE", `/v1/projects/${projectId}`);
}

// ---------------------------------------------------------------------------
// MINOR — the new-project preview uses the server's UTC date.
// ---------------------------------------------------------------------------
{
  await page.goto(`${BASE}/`);
  await page.getByTestId("new-project-button").first().click();
  const description = page.getByTestId("project-name-derived");
  await description.waitFor({ timeout: 20_000 });
  const text = await description.innerText();
  const now = new Date();
  const expected = `${String(now.getUTCMonth() + 1).padStart(2, "0")}-${String(now.getUTCDate()).padStart(2, "0")}`;
  record.checks.namePreview = { text, expected };
  check(text.includes(`空白工作区 · ${expected}`), "the name preview uses the server's UTC date", record.checks.namePreview);
  await shot(page, "E-name-preview-utc");
  await page.keyboard.press("Escape");
}

record.ok = true;
save();
console.log("[done]", path.join(out, "verify-log.json"));
await browser.close();
