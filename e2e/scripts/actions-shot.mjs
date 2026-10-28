import { chromium } from "@playwright/test";
import path from "node:path";
const BASE = "http://127.0.0.1:7722";
const OUT = path.resolve("../docs/screenshots");
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

const csrf = (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "";
const created = await page.request.post(`${BASE}/v1/projects`, {
  data: { name: "actions", default_branch: "main" }, headers: { "X-PF-CSRF": csrf },
});
const project = (await created.json()).project;
for (const title of ["重构缓存层", "排查登录超时"]) {
  await page.request.post(`${BASE}/v1/projects/${project.id}/sessions`, {
    data: { title }, headers: { "X-PF-CSRC": csrf, "X-PF-CSRF": csrf },
  });
}
const list = await (await page.request.get(`${BASE}/v1/sessions?project=${project.id}`)).json();
await page.goto(`${BASE}/p/${project.id}/s/${list.sessions[0].id}`);
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.getByTestId("session-link").first().hover();
await page.getByTestId("session-actions").first().click();
await page.waitForTimeout(600);
await page.screenshot({ path: path.join(OUT, "54-session-actions.png") });
console.log("menu items:", await page.locator('[data-testid^="session-"]').allTextContents());
await browser.close();