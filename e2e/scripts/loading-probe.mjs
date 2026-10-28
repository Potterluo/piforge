import { chromium } from "@playwright/test";
const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const SID = process.env.SID;
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });
const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const project = listed.projects.find((p) => p.name === "probe") ?? listed.projects[0];
await page.goto(`${BASE}/p/${project.id}/s/${SID}`);
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.getByTestId("panel-tab-browser").click();
const panel = page.getByTestId("browser-panel");
for (let i = 0; i < 30; i++) {
  if ((await panel.getAttribute("data-browser-status")) === "open") break;
  await page.waitForTimeout(1000);
}
console.log("status:", await panel.getAttribute("data-browser-status"), "painted:", await panel.getAttribute("data-browser-painted"));
await page.getByTestId("browser-url").fill("https://example.com/");
await page.getByTestId("browser-url").press("Enter");
for (let i = 0; i < 10; i++) {
  await page.waitForTimeout(1000);
  const s = await page.evaluate(() => ({
    loading: document.querySelectorAll('[data-testid="browser-loading"]').length,
    painted: document.querySelector('[data-testid="browser-panel"]')?.getAttribute("data-browser-painted"),
  }));
  console.log(`t+${i + 1}s`, JSON.stringify(s));
  if (s.loading === 0 && s.painted === "1") break;
}
await page.screenshot({ path: "../docs/screenshots/53-browser-loading.png" });
await browser.close();