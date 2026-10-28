import { chromium } from "@playwright/test";
import path from "node:path";
const BASE = process.env.BASE;
const SID = process.env.SID;
const OUT = path.resolve("../docs/screenshots");
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const project = listed.projects.find((p) => p.name === "cjk");
await page.goto(`${BASE}/p/${project.id}/s/${SID}`);

const tab = page.getByTestId("panel-tab-browser");
for (let i = 0; i < 45 && !(await tab.isEnabled()); i++) await page.waitForTimeout(2000);
await tab.click();
await page.getByTestId("browser-panel").waitFor({ timeout: 30000 });

for (let i = 0; i < 30; i++) {
  const st = await page.getByTestId("browser-panel").getAttribute("data-browser-status");
  if (st === "open") break;
  await page.waitForTimeout(2000);
}
await page.getByTestId("browser-url").fill("https://www.baidu.com/");
await page.getByTestId("browser-url").press("Enter");
await page.waitForTimeout(9000);

const before = await page.evaluate(() => {
  const c = document.querySelector('[data-testid="browser-canvas"]');
  return { w: c?.width ?? 0, h: c?.height ?? 0, cssW: Math.round(c?.getBoundingClientRect().width ?? 0) };
});
await page.screenshot({ path: path.join(OUT, "32-cjk-fonts.png") });

// collapse the sidebar: the sandbox must re-render at the new width instead of
// letterboxing the old 1280-wide frame
await page.getByTestId("sidebar-collapse").click();
await page.waitForTimeout(6000);
const after = await page.evaluate(() => {
  const c = document.querySelector('[data-testid="browser-canvas"]');
  return { w: c?.width ?? 0, h: c?.height ?? 0, cssW: Math.round(c?.getBoundingClientRect().width ?? 0) };
});
await page.screenshot({ path: path.join(OUT, "33-browser-resized.png") });
console.log("frame before:", JSON.stringify(before), "after:", JSON.stringify(after));
console.log("width grew:", after.w > before.w + 100);
await browser.close();