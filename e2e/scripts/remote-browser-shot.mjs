import { chromium } from "@playwright/test";
import path from "node:path";

const BASE = process.env.BASE ?? "http://127.0.0.1:17700";
const OUT = path.resolve(process.env.OUT ?? "../docs/screenshots");

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
page.on("pageerror", (e) => console.log("[pageerror]", e.message));

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

const csrf = (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "";
const api = page.request;

let project = (await (await api.get(`${BASE}/v1/projects`)).json()).projects.find((p) => p.name === "browser-proof");
if (!project) {
  project = (await (await api.post(`${BASE}/v1/projects`, {
    data: { name: "browser-proof", repo_url: "", default_branch: "main" },
    headers: { "X-PF-CSRF": csrf },
  })).json()).project;
}

const created = await api.post(`${BASE}/v1/projects/${project.id}/sessions`, {
  data: { title: "browser check", model_ref: "volc/deepseek-v4-flash" },
  headers: { "X-PF-CSRF": csrf },
});
const createdBody = await created.json();
const sessionId = createdBody?.session?.id;
if (!sessionId) throw new Error(`no session: ${JSON.stringify(createdBody).slice(0, 200)}`);
console.log("session:", sessionId);

await page.goto(`${BASE}/p/${project.id}/s/${sessionId}`);
// The tabs stay disabled until the sandbox for this session is ready.
const tab = page.getByTestId("panel-tab-browser");
for (let i = 0; i < 45; i++) {
  if (await tab.isEnabled()) break;
  await page.waitForTimeout(2000);
}
console.log("tab enabled:", await tab.isEnabled());
await tab.click();
await page.getByTestId("browser-panel").waitFor({ timeout: 30000 });
for (let i = 0; i < 20; i++) {
  const st = await page.getByTestId("browser-panel").getAttribute("data-browser-status");
  if (st === "live" || st === "error") { console.log("status reached:", st, "after", i * 2, "s"); break; }
  await page.waitForTimeout(2000);
}
console.log("status before navigate:", await page.getByTestId("browser-panel").getAttribute("data-browser-status"));

await page.getByTestId("browser-url").fill("https://example.com/");
await page.getByTestId("browser-url").press("Enter");
await page.waitForTimeout(12000);
const painted = await page.evaluate(() => (document.querySelector('[data-testid="browser-canvas"]')?.width ?? 0) > 0);
await page.screenshot({ path: path.join(OUT, "23-container-browser.png") });
console.log("canvas width>0:", painted, "| status:", await page.getByTestId("browser-panel").getAttribute("data-browser-status"));
await browser.close();
console.log("done");
