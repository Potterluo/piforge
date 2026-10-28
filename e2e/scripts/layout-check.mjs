import { chromium } from "@playwright/test";
import path from "node:path";
const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const OUT = path.resolve("../docs/screenshots");
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const project = listed.projects[0] ?? (await (await page.request.post(`${BASE}/v1/projects`, {
  data: { name: "layout", repo_url: "", default_branch: "main" },
  headers: { "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "" },
})).json()).project;
const session = (await (await page.request.post(`${BASE}/v1/projects/${project.id}/sessions`, {
  data: { title: "layout check" },
  headers: { "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "" },
})).json()).session;

await page.goto(`${BASE}/p/${project.id}/s/${session.id}`);
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.waitForTimeout(1200);

const measure = () =>
  page.evaluate(() => ({
    scrollHeight: document.documentElement.scrollHeight,
    innerHeight: window.innerHeight,
    modelPickerY: Math.round(
      document.querySelector('[data-testid="composer-model"]')?.getBoundingClientRect().bottom ?? -1,
    ),
  }));

const before = await measure();
await page.getByTestId("sidebar-collapse").click();
await page.waitForTimeout(1200);
const after = await measure();
console.log("before:", JSON.stringify(before), "after:", JSON.stringify(after));
console.log("no vertical overflow:", after.scrollHeight <= after.innerHeight);
console.log("model picker in view:", after.modelPickerY > 0 && after.modelPickerY <= after.innerHeight);
await page.screenshot({ path: path.join(OUT, "51-shell-collapsed.png") });
await browser.close();