// One-off diagnostic: drive the real login flow and print every network call
// and console message so a failure can be attributed instead of guessed.
import { chromium } from "@playwright/test";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();

page.on("console", (m) => console.log(`[console:${m.type()}]`, m.text()));
page.on("pageerror", (e) => console.log("[pageerror]", e.message));
page.on("request", (r) => {
  if (r.url().includes("/v1/")) console.log("[req]", r.method(), r.url());
});
page.on("response", async (r) => {
  if (r.url().includes("/v1/")) {
    console.log("[res]", r.status(), r.url(), "set-cookie:", r.headers()["set-cookie"] ? "yes" : "no");
  }
});
page.on("framenavigated", (f) => {
  if (f === page.mainFrame()) console.log("[nav]", f.url());
});

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();

await page.waitForTimeout(4000);
console.log("[final url]", page.url());
console.log("[cookies]", (await page.context().cookies()).map((c) => `${c.name}=${c.value.slice(0, 6)}…`));
console.log("[sidebar count]", await page.getByTestId("app-sidebar").count());
const body = await page.locator("body").innerText();
console.log("[body head]", body.slice(0, 300).replace(/\n/g, " | "));

await browser.close();
