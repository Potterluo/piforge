/**
 * Screenshots for the two PARTIAL rows finished in this batch, taken against
 * the *deployed* instance (http://127.0.0.1:7722), not the fake-peer e2e server.
 *
 *   node scripts/shots-partial-batch2.mjs
 *
 * It creates one throw-away project/session, writes the screenshots under
 * e2e/.artifacts/partial-batch2/, then deletes the project. The assertions live
 * in tests/72-observe-input.spec.ts and tests/73-git-overview.spec.ts; this
 * script only produces the visual evidence.
 */
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const BASE = process.env.PIFORGE_BASE ?? "http://127.0.0.1:7722";
const OUT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../.artifacts/partial-batch2");
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };

async function api(page, method, url, data) {
  const cookies = await page.context().cookies();
  const csrf = cookies.find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
  const headers = { "X-PF-CSRF": csrf };
  if (typeof data === "string") headers["Content-Type"] = "text/plain; charset=utf-8";
  const response = await page.request.fetch(`${BASE}${url}`, { method, headers, data });
  const body = await response.json().catch(() => null);
  if (!response.ok()) throw new Error(`${method} ${url} -> ${response.status()} ${JSON.stringify(body)}`);
  return body;
}

// The page writes its own event log into the title, so the screenshot shows
// whether a click reached it: the tab strip renders that title.
//
// The deployed Chromium runs inside the pf-local container (the control plane
// launches it there), so 127.0.0.1 is the container itself. A tiny helper
// container publishes the page on a host port, and Chrome is pointed at the
// docker gateway — the only host address it can reach without extra mappings.
const helper = "pf-shot-page";
const helperPort = 17723;
const pageDir = mkdtempSync(path.join(tmpdir(), "pf-shot-page-"));
writeFileSync(
  path.join(pageDir, "index.html"),
  `<!doctype html>
<html><head><title>observe-target</title></head>
<body style="margin:0;height:100vh;background:#fff">
  <p style="font:16px system-ui;padding:24px">observe target</p>
  <script>
    addEventListener("click", () => { document.title = "clicked"; });
    addEventListener("keydown", (event) => { document.title = "key-" + event.key; });
  </script>
</body></html>`,
);
const docker = (...args) => execFileSync("docker", args, { stdio: "ignore" });
try {
  docker("rm", "-f", helper);
} catch {
  /* not running */
}
docker(
  "run", "-d", "--rm", "--name", helper,
  "-p", `${helperPort}:18080`,
  "-v", `${pageDir}:/page`,
  "python:3.12-alpine", "python", "-m", "http.server", "18080", "--directory", "/page",
);
const origin = `http://172.17.0.1:${helperPort}`;

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
let projectId = null;

try {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill(USER.password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });

  const created = await api(page, "POST", "/v1/projects", {
    name: `shots-batch2-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  projectId = created.project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { model_ref: "" });
  const sessionId = session.session.id;

  // --- FR-12.4: counts and risk markers ------------------------------------
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=base.txt`, "one\ntwo\nthree\n");
  await api(page, "POST", `/v1/sessions/${sessionId}/git/stage`, { paths: ["base.txt"] });
  await api(page, "POST", `/v1/sessions/${sessionId}/git/commit`, { message: "base", paths: [] });
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=base.txt`, "one\nTWO\nthree\n");
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=notes.txt`, "a\nb\n");
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=package-lock.json`, "{\n}\n");
  await api(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=.github%2Fworkflows%2Fci.yml`,
    "name: ci\n",
  );

  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("workspace-panels").waitFor({ timeout: 30_000 });
  await page.getByTestId("panel-tab-changes").click();
  await page.getByTestId("git-change-totals").waitFor({ timeout: 30_000 });
  await page.screenshot({ path: path.join(OUT, "fr-12.4-change-overview.png") });

  // --- FR-11.2: observe drops the click, takeover delivers it --------------
  await page.getByTestId("panel-tab-browser").click();
  const panel = page.locator(
    '[data-testid="browser-panel"][data-browser-status="open"]',
  );
  await panel.waitFor({ timeout: 60_000 });

  // Open the target as a real tab and select it: the same path the tab strip
  // uses, and it does not depend on the address bar's URL normalisation.
  const opened = await api(page, "POST", `/v1/sessions/${sessionId}/browser/tabs`, {
    url: origin,
  });
  const tab = page.locator(`[data-testid="browser-tab"][data-tab="${opened.tab.id}"]`);
  await tab.waitFor({ timeout: 20_000 });
  await tab.click();
  await tab.getByText("observe-target").waitFor({ timeout: 45_000 });
  await page
    .locator('[data-testid="browser-panel"][data-browser-painted="1"]')
    .waitFor({ timeout: 45_000 });

  // Default mode is observe. The click is sent by the panel; the server drops it.
  await page.getByTestId("browser-canvas").click({ position: { x: 120, y: 80 } });
  await page.waitForTimeout(1500);
  await tab.getByText("observe-target").waitFor({ timeout: 10_000 });
  await page.screenshot({ path: path.join(OUT, "fr-11.2-observe-click-dropped.png") });

  // Take over through the UI so the screenshot also shows who is in control.
  await page.getByTestId("browser-mode").click();
  await page.getByRole("button", { name: "接管" }).click();
  await page.waitForFunction(
    () => document.querySelector('[data-testid="browser-mode"]')?.textContent === "控制中",
    undefined,
    { timeout: 20_000 },
  );
  await page.getByTestId("browser-canvas").click({ position: { x: 120, y: 80 } });
  await tab.getByText("clicked").waitFor({ timeout: 20_000 });
  await page.screenshot({ path: path.join(OUT, "fr-11.2-takeover-click-delivered.png") });

  console.log(`screenshots written to ${OUT}`);
} finally {
  try {
    if (projectId) await api(page, "DELETE", `/v1/projects/${projectId}`);
  } catch (error) {
    console.error("cleanup: project", error.message);
  }
  await browser.close();
  try {
    docker("rm", "-f", helper);
  } catch {
    /* already gone */
  }
}
