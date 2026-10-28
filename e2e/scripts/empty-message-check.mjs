import { chromium } from "@playwright/test";
import path from "node:path";
const BASE = process.env.BASE ?? "http://127.0.0.1:7700";
const SID = process.env.SID;
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
page.on("pageerror", (e) => console.log("[pageerror]", e.message));
page.on("console", (m) => { if (m.type() === "error") console.log("[console]", m.text().slice(0, 200)); });
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });
const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
await page.goto(`${BASE}/p/${listed.projects[0].id}/s/${SID}`);
await page.waitForTimeout(4000);
await page.screenshot({ path: path.resolve("../docs/screenshots/30-empty-assistant.png") });
const html = await page.evaluate(() => {
  const nodes = Array.from(document.querySelectorAll("[data-slot='message']")).map((n) => ({
    text: (n.textContent ?? "").slice(0, 80),
    height: n.getBoundingClientRect().height,
    html: n.innerHTML.slice(0, 300),
  }));
  return nodes;
});
console.log(JSON.stringify(html, null, 2).slice(0, 1500));
await browser.close();
