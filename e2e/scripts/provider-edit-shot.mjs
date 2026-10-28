import { chromium } from "@playwright/test";
import path from "node:path";
const BASE = process.env.BASE;
const OUT = path.resolve("../docs/screenshots");
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });
await page.goto(`${BASE}/settings/providers`);
await page.waitForTimeout(1500);
const edit = page.getByTestId("provider-edit").first();
if (await edit.count()) {
  await edit.click();
  await page.waitForTimeout(700);
  await page.screenshot({ path: path.join(OUT, "42-provider-edit.png") });
  console.log("captured", await page.getByTestId("edit-provider-models").inputValue());
} else {
  console.log("no editable provider");
}
await browser.close();