import { chromium } from "@playwright/test";
import path from "node:path";
const BASE = process.env.BASE;
const OUT = path.resolve("../docs/screenshots");
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("admin@piforge.local");
await page.getByTestId("login-password").fill("piforge-admin-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

await page.goto(`${BASE}/admin/users`);
await page.waitForTimeout(1500);
await page.screenshot({ path: path.join(OUT, "40-admin-users.png") });
await page.getByTestId("admin-create-user").click();
await page.waitForTimeout(600);
await page.getByTestId("new-user-email").fill("bob@example.com");
await page.getByTestId("new-user-password").fill("bobpassword");
await page.screenshot({ path: path.join(OUT, "41-admin-create-user.png") });
await page.keyboard.press("Escape");

await page.goto(`${BASE}/settings/providers`);
await page.waitForTimeout(1200);
const edit = page.getByTestId("provider-edit").first();
if (await edit.count()) {
  await edit.click();
  await page.waitForTimeout(600);
  await page.screenshot({ path: path.join(OUT, "42-provider-edit.png") });
  console.log("edit dialog captured");
} else {
  console.log("no editable provider");
}
await browser.close();
console.log("done");