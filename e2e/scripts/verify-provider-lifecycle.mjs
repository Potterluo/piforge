#!/usr/bin/env node
/**
 * Deployed-app verification for the provider-lifecycle fix and the three small
 * fixes from acceptance pass 3. Drives the real UI at :7722 and writes
 * screenshots + a JSON log to e2e/.artifacts/fix-provider-lifecycle/.
 *
 * It creates its own provider, project and sessions, deletes the provider under
 * a live session, and uses the deployment's default provider for the recovery
 * turn (one short prompt).
 *
 * Usage: node verify-provider-lifecycle.mjs [baseURL]
 */
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(HERE, "../.artifacts/fix-provider-lifecycle");
mkdirSync(OUT, { recursive: true });

const base = (process.argv[2] ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const DEV = { email: "dev@piforge.local", password: "piforge-dev-123" };
const stamp = Date.now();
const evidence = { base, at: new Date().toISOString(), steps: [], screenshots: [] };
const step = (name, value) => {
  evidence.steps.push({ name, ...value });
  console.log(`${name}:`, JSON.stringify(value));
};

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
const log = [];
page.on("console", (msg) => log.push(`${msg.type()}: ${msg.text()}`));
page.on("pageerror", (err) => log.push(`pageerror: ${err.message}`));

const shot = async (name) => {
  const file = path.join(OUT, name);
  await page.screenshot({ path: file, fullPage: false });
  evidence.screenshots.push(name);
};

/** State-changing API calls need the double-submit CSRF header (as the UI does). */
const csrf = async () =>
  (await context.cookies()).find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
const api = async (method, route, data) =>
  page.request.fetch(`${base}${route}`, {
    method,
    headers: { "X-PF-CSRF": await csrf() },
    data,
  });

try {
  await page.goto(`${base}/login`);
  await page.getByTestId("login-email").fill(DEV.email);
  await page.getByTestId("login-password").fill(DEV.password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
  step("login", { ok: true });

  // Create the dying provider, a project and a session pinned to it.
  const providerName = `accept3-ui-${stamp}`;
  const created = await api("POST", "/v1/providers", {
    name: providerName,
    api: "openai-compat",
    base_url: "https://example.invalid/v1",
    api_key: "sk-ui-probe-not-real",
    models: [{ id: "ui-probe-model" }],
  });
  const createdBody = await created.json();
  if (!createdBody?.provider?.id) {
    throw new Error(`create provider failed: ${created.status()} ${JSON.stringify(createdBody)}`);
  }
  const providerId = createdBody.provider.id;
  step("create provider", { status: created.status(), name: providerName });

  const projectRes = await api("POST", "/v1/projects", {
    name: `accept3-ui-${stamp}`,
    repo_url: "",
  });
  const projectId = (await projectRes.json()).project.id;
  const sessionRes = await api("POST", `/v1/projects/${projectId}/sessions`, {
    title: "provider deleted under me",
    model_ref: `${providerName}/ui-probe-model`,
  });
  const sessionId = (await sessionRes.json()).session.id;
  step("create session", { status: sessionRes.status(), projectId, sessionId });

  await page.goto(`${base}/p/${projectId}/s/${sessionId}`);
  await page.locator(`[data-session-id="${sessionId}"]`).waitFor({ timeout: 40_000 });
  await shot("01-session-before-delete.png");

  // The provider disappears under the open session, as in the acceptance run.
  const removed = await api("DELETE", `/v1/providers/${providerId}`);
  step("delete provider", { status: removed.status() });

  // The header must not keep showing a dead model as if it worked, and must
  // never render `unknown`.
  await page.getByTestId("session-meta").waitFor({ timeout: 20_000 });
  await page.waitForTimeout(1_000);
  const metaBefore = (await page.getByTestId("session-meta").textContent()) ?? "";
  step("header before the send", { text: metaBefore, has_unknown: /unknown/i.test(metaBefore) });
  await shot("02-header-after-provider-deleted.png");

  // The idle send button has a real accessible name.
  step("send aria-label", {
    label: await page.getByTestId("composer-send").getAttribute("aria-label"),
  });

  const input = page.getByTestId("composer-input");
  await input.fill("说一句话，证明这个会话还活着");
  await page.getByTestId("composer-send").click();

  const switched = page.locator("[data-sonner-toast]").filter({ hasText: "已自动切换模型" }).first();
  await switched.waitFor({ timeout: 30_000 });
  step("fallback toast", { text: await switched.textContent() });
  await shot("03-fallback-toast.png");

  await page.getByTestId("turn-user").first().waitFor({ timeout: 40_000 });
  // `run-state` is a hidden marker span, so wait for its attribute rather than
  // for visibility (the same signal the e2e suite reads).
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") ===
      "settled",
    undefined,
    { timeout: 180_000 },
  );
  const metaAfter = (await page.getByTestId("session-meta").textContent()) ?? "";
  step("header after the run", { text: metaAfter, has_unknown: /unknown/i.test(metaAfter) });
  await shot("04-session-recovered.png");

  const detail = await page.request.get(`${base}/v1/sessions/${sessionId}`);
  const detailBody = await detail.json();
  step("session row after the run", {
    model_ref: detailBody.session.model_ref,
    status: detail.status(),
  });

  // FIX 3: the file tree header always offers 新建文件.
  await page.getByTestId("panel-tab-files").click();
  await page.getByTestId("file-tree").waitFor({ timeout: 30_000 });
  const createHeader = page.getByTestId("file-create-header");
  await createHeader.waitFor({ timeout: 20_000 });
  step("file tree header action", {
    visible: await createHeader.isVisible(),
    label: await createHeader.getAttribute("aria-label"),
  });
  await shot("05-file-tree-header-new-file.png");

  // FIX 3 / compare: the explanation is on the page, and the main session still
  // reports 0 files (correct, now explained).
  const clone = await api("POST", `/v1/sessions/${sessionId}/clone`, {});
  const cloneId = (await clone.json()).session.id;
  await page.goto(`${base}/p/${projectId}/compare?ids=${sessionId},${cloneId}`);
  const explainer = page.getByTestId("compare-explainer");
  await explainer.waitFor({ timeout: 30_000 });
  step("compare explainer", { text: (await explainer.textContent())?.trim() });
  await shot("06-compare-explainer.png");

  // The default-model copy, in the composer picker.
  await page.goto(`${base}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-model").waitFor({ timeout: 30_000 });
  await page.getByTestId("composer-model").click();
  const inherit = page.getByRole("option", { name: "默认模型" }).first();
  await inherit.waitFor({ timeout: 20_000 });
  step("model picker copy", { default_option: "默认模型", body_has_follow_default: (await page.locator("body").textContent()).includes("跟随默认") });
  await shot("07-model-picker-default-model.png");
  await page.keyboard.press("Escape");

  // Cleanup: this probe owns the sessions and the project.
  await api("DELETE", `/v1/sessions/${cloneId}`);
  await api("DELETE", `/v1/sessions/${sessionId}`);
  await api("DELETE", `/v1/projects/${projectId}`);
  step("cleanup", { ok: true });

  evidence.console = log.slice(0, 50);
  writeFileSync(path.join(OUT, "verify.json"), JSON.stringify(evidence, null, 2));
  console.log(`evidence: ${path.join(OUT, "verify.json")}`);
} catch (error) {
  evidence.error = String(error?.stack ?? error);
  evidence.console = log.slice(0, 80);
  await shot("99-failure.png").catch(() => {});
  writeFileSync(path.join(OUT, "verify.json"), JSON.stringify(evidence, null, 2));
  console.error(evidence.error);
  process.exitCode = 1;
} finally {
  await context.close();
  await browser.close();
}
