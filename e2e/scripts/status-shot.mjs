import { chromium } from "@playwright/test";
import path from "node:path";
const BASE = "http://127.0.0.1:7722";
const OUT = path.resolve("../docs/screenshots");
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("admin@piforge.local");
await page.getByTestId("login-password").fill("piforge-admin-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

await page.goto(`${BASE}/admin/status`);
await page.getByTestId("admin-status").waitFor({ timeout: 30000 });
await page.waitForTimeout(1200);
console.log("users:", await page.getByTestId("stat-users").textContent());
await page.screenshot({ path: path.join(OUT, "55-admin-status.png") });
await browser.close();