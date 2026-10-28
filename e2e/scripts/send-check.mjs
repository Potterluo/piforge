import { chromium } from "@playwright/test";
const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.on("pageerror", (e) => console.log("[pageerror]", e.message));
page.on("console", (m) => { if (m.type() === "error") console.log("[console]", m.text().slice(0, 300)); });

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const project = listed.projects[0];
const created = await page.request.post(`${BASE}/v1/projects/${project.id}/sessions`, {
  data: { title: "send check" },
  headers: { "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "" },
});
const session = (await created.json()).session;
await page.goto(`${BASE}/p/${project.id}/s/${session.id}`);
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.waitForTimeout(1500);
console.log("run-state before:", await page.getByTestId("run-state").getAttribute("data-state"));
console.log("send disabled:", await page.getByTestId("composer-send").isDisabled());
await page.getByTestId("composer-input").fill("hello there");
await page.getByTestId("composer-send").click();
await page.waitForTimeout(3000);
console.log("run-state after:", await page.getByTestId("run-state").getAttribute("data-state"));
console.log("user turns:", await page.getByTestId("turn-user").count());
await page.screenshot({ path: "../docs/screenshots/50-send-check.png" });
await browser.close();