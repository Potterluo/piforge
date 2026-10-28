// Re-shoot only the browser panel, pointed at a page that renders instantly and
// proves the screencast is live (our own app, served from the same host).
import { chromium } from "@playwright/test";
import path from "node:path";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const OUT = path.resolve(process.env.OUT ?? "../docs/screenshots");
const URL = process.env.SHOT_URL ?? `${BASE}/login`;

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
page.on("pageerror", (e) => console.log("[pageerror]", e.message));

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 20000 });

const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const projectId = listed.projects.find((p) => p.name === "pi-forge").id;
await page.goto(`${BASE}/p/${projectId}`);
await page.locator('a[href*="/s/"]').first().click();
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.getByTestId("panel-tab-browser").click();
await page.getByTestId("browser-panel").waitFor({ timeout: 20000 });
await page.waitForFunction(
  () => document.querySelector('[data-testid="browser-panel"]')?.getAttribute("data-browser-status") === "open",
  undefined,
  { timeout: 45000 },
);

await page.getByTestId("browser-url").fill(URL);
await page.getByTestId("browser-url").press("Enter");
await page.waitForTimeout(5000);
await page.screenshot({ path: path.join(OUT, "09-browser.png") });
console.log("shot 09-browser ->", URL);

await browser.close();
