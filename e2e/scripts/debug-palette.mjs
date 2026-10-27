// Diagnostic: why does opening the command palette crash the page?
import { chromium } from "@playwright/test";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();

page.on("console", (m) => console.log(`[console:${m.type()}]`, m.text().slice(0, 400)));
page.on("pageerror", (e) => {
  console.log("[pageerror]", e.message);
  console.log((e.stack ?? "").split("\n").slice(0, 8).join("\n"));
});

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 20000 });

await page.goto(`${BASE}/`);
await page.waitForTimeout(1500);
console.log("[before]", (await page.locator("body").innerText()).slice(0, 80).replace(/\n/g, " | "));

await page.keyboard.press("Control+k");
await page.waitForTimeout(2500);
console.log("[after]", (await page.locator("body").innerText()).slice(0, 200).replace(/\n/g, " | "));
console.log("[dialogs]", await page.getByRole("dialog").count());

await browser.close();
