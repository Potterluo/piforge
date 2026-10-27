// Diagnostic: sign in, create a session through the API (with CSRF), then open
// the workbench and print every console error so a render crash is attributable.
import { chromium } from "@playwright/test";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();

page.on("console", (m) => {
  if (m.type() === "error" || m.type() === "warning") {
    console.log(`[console:${m.type()}]`, m.text());
  }
});
page.on("pageerror", (e) => {
  console.log("[pageerror]", e.message);
  console.log(e.stack?.split("\n").slice(0, 8).join("\n"));
});

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 20000 });

const cookies = await page.context().cookies();
const csrf = cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
const headers = { "X-PF-CSRF": csrf };

const project = await page.request.post(`${BASE}/v1/projects`, {
  headers,
  data: { name: `dbg-${Date.now()}`, repo_url: "", default_branch: "main" },
});
const projectBody = await project.json();
if (!projectBody.project) {
  console.log("[project failed]", project.status(), JSON.stringify(projectBody));
  process.exit(1);
}
const projectId = projectBody.project.id;

const sess = await page.request.post(`${BASE}/v1/projects/${projectId}/sessions`, {
  headers,
  data: { title: "dbg" },
});
const sessionBody = await sess.json();
if (!sessionBody.session) {
  console.log("[session failed]", sess.status(), JSON.stringify(sessionBody));
  process.exit(1);
}
const sessionId = sessionBody.session.id;
console.log("[ids]", projectId, sessionId, "worktree:", sessionBody.session.worktree_id);

await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
await page.waitForTimeout(5000);
console.log("[url]", page.url());
console.log("[h1]", await page.locator("h1").allInnerTexts());
console.log("[composer]", await page.getByTestId("composer-input").count());
console.log("[body head]", (await page.locator("body").innerText()).slice(0, 600).replace(/\n/g, " | "));

await browser.close();
