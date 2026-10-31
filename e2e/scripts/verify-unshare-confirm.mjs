#!/usr/bin/env node
/**
 * Deployed-app verification for the shared-provider unshare fix.
 *
 * Against the real deployment (default http://127.0.0.1:7722) it proves:
 *   1. DELETE /v1/providers/{id} on a shared provider answers the typed
 *      `shared_provider` error (409) naming /v1/admin/providers/{id} — not 404;
 *   2. 取消共享 opens a confirmation that names the provider, says what breaks
 *      for inheriting users and for sessions following the default, and marks
 *      the deployment default;
 *   3. cancelling changes nothing (row and API entry both still there);
 *   4. confirming removes it (row and API entry both gone, /v1/setup says
 *      needs_provider);
 *   5. re-creating `volc` through the admin API puts the deployment back, with
 *      has_key:true and needs_provider:false.
 *
 * The provider key is read from ~/.dsh/.credentials.yaml (ref VOLCE_API_KEY) and
 * is never printed, logged or written to an artifact.
 *
 * Usage: node verify-unshare-confirm.mjs [baseURL]
 */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(HERE, "../.artifacts/fix-unshare");
mkdirSync(OUT, { recursive: true });

const base = (process.argv[2] ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const ADMIN = { email: "admin@piforge.local", password: "piforge-admin-123" };
const PROVIDER = {
  name: "volc",
  api: "openai-completions",
  base_url: "https://api.example.com/v1",
  models: [{ id: "deepseek-v4-flash" }],
};

/** Reads one ref out of the credential store; the value never leaves this function's caller. */
function credential(ref) {
  const file = path.join(os.homedir(), ".dsh", ".credentials.yaml");
  const line = readFileSync(file, "utf8")
    .split(/\r?\n/)
    .find((row) => new RegExp(`^\\s+${ref}:\\s*`).test(row));
  if (!line) throw new Error(`${ref} is not present in the credential store`);
  let value = line.replace(/^\s*[^:]+:\s*/, "").trim();
  if (
    (value.startsWith('"') && value.endsWith('"')) ||
    (value.startsWith("'") && value.endsWith("'"))
  ) {
    value = value.slice(1, -1);
  }
  if (!value) throw new Error(`${ref} resolved to an empty value`);
  return value;
}

const evidence = { base, at: new Date().toISOString(), steps: [], screenshots: [] };
const step = (name, value) => {
  evidence.steps.push({ name, ...value });
  console.log(`${name}:`, JSON.stringify(value));
};

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
const consoleLog = [];
page.on("console", (msg) => consoleLog.push(`${msg.type()}: ${msg.text()}`));
page.on("pageerror", (err) => consoleLog.push(`pageerror: ${err.message}`));

const shot = async (name) => {
  await page.screenshot({ path: path.join(OUT, name), fullPage: false });
  evidence.screenshots.push(name);
};

const csrf = async () =>
  (await context.cookies()).find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
const api = async (method, route, data) =>
  page.request.fetch(`${base}${route}`, {
    method,
    headers: { "X-PF-CSRF": await csrf() },
    data,
  });

const listAdminProviders = async () => {
  const res = await api("GET", "/v1/admin/providers");
  const body = await res.json();
  return body.providers ?? [];
};
const setup = async () => {
  const res = await api("GET", "/v1/setup");
  return res.json();
};

let restored = false;
try {
  // 1. Sign in through the real form.
  await page.goto(`${base}/login`);
  await page.getByTestId("login-email").fill(ADMIN.email);
  await page.getByTestId("login-password").fill(ADMIN.password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
  step("login", { ok: true, email: ADMIN.email });

  const before = await listAdminProviders();
  const volc = before.find((provider) => provider.name === PROVIDER.name);
  if (!volc) throw new Error(`the deployment has no shared provider named ${PROVIDER.name}`);
  if (!volc.has_key) throw new Error(`${PROVIDER.name} has no key before the probe starts`);
  const beforeSetup = await setup();
  step("provider before", {
    id: volc.id,
    name: volc.name,
    api: volc.api,
    base_url: volc.base_url,
    has_key: volc.has_key,
    models: (volc.models ?? []).map((model) => model.id),
  });
  step("setup before", {
    needs_provider: beforeSetup.needs_provider,
    default_provider: beforeSetup.default_provider,
    default_provider_missing: beforeSetup.default_provider_missing,
  });

  // 2. The wrong endpoint answers a typed error while the provider still exists,
  //    and does not delete it.
  const wrong = await api("DELETE", `/v1/providers/${volc.id}`);
  const wrongBody = await wrong.json();
  step("wrong endpoint", {
    status: wrong.status(),
    code: wrongBody?.error?.code,
    names_admin_endpoint: String(wrongBody?.error?.message ?? "").includes(
      `/v1/admin/providers/${volc.id}`,
    ),
  });
  if (wrong.status() !== 409 || wrongBody?.error?.code !== "shared_provider") {
    throw new Error(`wrong endpoint answered ${wrong.status()} ${JSON.stringify(wrongBody)}`);
  }
  if ((await listAdminProviders()).every((provider) => provider.id !== volc.id)) {
    throw new Error("the wrong endpoint deleted the provider");
  }

  // 3. The confirmation itself.
  await page.goto(`${base}/settings/providers`);
  const row = page
    .locator('[data-testid="shared-provider-item"]')
    .filter({ hasText: PROVIDER.name })
    .first();
  await row.waitFor({ timeout: 30_000 });
  await shot("0-before.png");

  await row.getByTestId("shared-provider-unshare").click();
  const dialog = page.getByTestId("shared-provider-unshare-dialog");
  await dialog.waitFor({ timeout: 20_000 });
  // The dialog fades/zooms in; a screenshot mid-animation is unreadable.
  await page.waitForTimeout(400);
  const dialogText = (await dialog.textContent()) ?? "";
  const defaultWarning = page.getByTestId("shared-provider-unshare-default-warning");
  step("dialog", {
    names_provider: dialogText.includes(PROVIDER.name),
    says_every_user_loses_it: dialogText.includes("所有用户"),
    says_sessions_break: dialogText.includes("下一次发送"),
    default_warning_shown: await defaultWarning.isVisible(),
    default_warning_text: await defaultWarning.textContent(),
  });
  if (!dialogText.includes(PROVIDER.name) || !dialogText.includes("所有用户")) {
    throw new Error("the confirmation does not name the provider and who loses it");
  }
  await shot("1-dialog.png");

  // 4. Cancel changes nothing.
  await page.getByTestId("shared-provider-unshare-cancel").click();
  await dialog.waitFor({ state: "hidden", timeout: 20_000 });
  await page.waitForTimeout(300);
  await shot("2-after-cancel.png");
  const afterCancel = await listAdminProviders();
  step("after cancel", {
    row_visible: await row.isVisible(),
    still_listed: afterCancel.some((provider) => provider.id === volc.id),
    has_key: afterCancel.find((provider) => provider.id === volc.id)?.has_key ?? false,
  });
  if (!afterCancel.some((provider) => provider.id === volc.id)) {
    throw new Error("cancelling the confirmation removed the provider");
  }

  // 5. Confirm removes it.
  await row.getByTestId("shared-provider-unshare").click();
  await dialog.waitFor({ timeout: 20_000 });
  await page.getByTestId("shared-provider-unshare-confirm").click();
  await page
    .locator("[data-sonner-toast]")
    .filter({ hasText: "已取消共享" })
    .first()
    .waitFor({ timeout: 20_000 });
  await row.waitFor({ state: "detached", timeout: 20_000 });
  await page.waitForTimeout(500);
  await shot("3-after-confirm.png");
  const afterConfirm = await listAdminProviders();
  const confirmSetup = await setup();
  step("after confirm", {
    still_listed: afterConfirm.some((provider) => provider.id === volc.id),
    needs_provider: confirmSetup.needs_provider,
    default_provider_missing: confirmSetup.default_provider_missing,
  });
  if (afterConfirm.some((provider) => provider.id === volc.id)) {
    throw new Error("confirming did not remove the provider");
  }

  // The id is genuinely unknown now: 404, not a second `shared_provider`.
  const gone = await api("DELETE", `/v1/providers/${volc.id}`);
  const goneBody = await gone.json();
  step("deleted id answered as unknown", {
    status: gone.status(),
    code: goneBody?.error?.code,
  });

  // 6. Re-create exactly what was removed, through the admin API.
  const key = credential("VOLCE_API_KEY");
  const recreated = await api("POST", "/v1/admin/providers", {
    name: PROVIDER.name,
    api: PROVIDER.api,
    base_url: PROVIDER.base_url,
    api_key: key,
    models: PROVIDER.models,
  });
  const recreatedBody = await recreated.json();
  if (recreated.status() !== 200) {
    throw new Error(`re-create failed: ${recreated.status()} ${JSON.stringify(recreatedBody)}`);
  }
  restored = true;
  const restoredProvider = recreatedBody.provider;
  const restoredSetup = await setup();
  step("re-created", {
    status: recreated.status(),
    id: restoredProvider.id,
    name: restoredProvider.name,
    api: restoredProvider.api,
    has_key: restoredProvider.has_key,
    models: (restoredProvider.models ?? []).map((model) => model.id),
    needs_provider: restoredSetup.needs_provider,
    default_provider: restoredSetup.default_provider,
    default_provider_missing: restoredSetup.default_provider_missing,
  });
  if (!restoredProvider.has_key || restoredSetup.needs_provider !== false) {
    throw new Error("the deployment is not back: key missing or needs_provider true");
  }

  // Screenshot the page after a reload, so the restored state is what the UI renders.
  await page.reload();
  const restoredRow = page
    .locator('[data-testid="shared-provider-item"]')
    .filter({ hasText: PROVIDER.name })
    .first();
  await restoredRow.waitFor({ timeout: 30_000 });
  await page.waitForTimeout(300);
  await shot("4-after-recreate.png");
  step("ui after re-create", {
    row_visible: await restoredRow.isVisible(),
    has_key_badge: (await restoredRow.textContent())?.includes("已配置 key"),
    default_badge: (await restoredRow.textContent())?.includes("部署默认"),
  });

  evidence.console = consoleLog.slice(-40);
  writeFileSync(path.join(OUT, "verify.json"), JSON.stringify(evidence, null, 2));
  writeFileSync(path.join(OUT, "console.log"), consoleLog.join("\n"));
  console.log("PASS — the provider is back and the deployment is as found");
} catch (error) {
  evidence.error = String(error?.stack ?? error);
  evidence.console = consoleLog.slice(-80);
  // Never leave the deployment without its provider, even when an assertion
  // above failed mid-flow. Re-creating the same name is an upsert, so this is
  // harmless if the row is still there.
  try {
    const present = (await listAdminProviders()).some(
      (provider) => provider.name === PROVIDER.name,
    );
    if (!present) {
      const healed = await api("POST", "/v1/admin/providers", {
        name: PROVIDER.name,
        api: PROVIDER.api,
        base_url: PROVIDER.base_url,
        api_key: credential("VOLCE_API_KEY"),
        models: PROVIDER.models,
      });
      evidence.recovery = { status: healed.status(), recreated: healed.status() === 200 };
      console.error("recovery: re-created the missing provider after the failure");
    }
  } catch (recoveryError) {
    evidence.recovery = { error: String(recoveryError) };
  }
  writeFileSync(path.join(OUT, "verify.json"), JSON.stringify(evidence, null, 2));
  console.error("FAIL:", error);
  process.exitCode = 1;
} finally {
  await browser.close();
  if (!restored) {
    console.error(
      "WARNING: the provider was not restored by this run; re-create it before leaving the deployment",
    );
  }
}
