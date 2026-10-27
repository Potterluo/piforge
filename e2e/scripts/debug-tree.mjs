// Diagnostic: what does the file tree actually render after a write + reload?
import { chromium } from "@playwright/test";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();

page.on("response", async (r) => {
  if (r.url().includes("/files")) console.log("[res]", r.status(), r.url().replace(BASE, ""));
});
page.on("console", (m) => {
  if (m.type() === "error") console.log("[console:error]", m.text());
});

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 20000 });

const project = await page.request.post(`${BASE}/v1/projects`, {
  headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
  data: { name: `tree-${Date.now()}`, repo_url: "", default_branch: "main" },
});
const projectId = (await project.json()).project.id;
const csrf = csrfOf(await page.context().cookies());
const sess = await page.request.post(`${BASE}/v1/projects/${projectId}/sessions`, {
  headers: { "X-PF-CSRF": csrf },
  data: { title: "tree" },
});
const sessionId = (await sess.json()).session.id;

// Prompt so the sandbox exists, then wait it out.
await page.request.post(`${BASE}/v1/sessions/${sessionId}/messages`, {
  headers: { "X-PF-CSRF": csrf },
  data: { client_msg_id: `cm_${Date.now()}`, text: "hello" },
});
await page.waitForTimeout(4000);

const put = await page.request.put(
  `${BASE}/v1/sessions/${sessionId}/files?path=${encodeURIComponent("internal/cache/README.md")}`,
  {
    headers: { "X-PF-CSRF": csrf, "Content-Type": "text/plain; charset=utf-8" },
    data: "# Cache\n\nThe eviction race lives in lru.go.\n",
  },
);
console.log("[put]", put.status());

await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
await page.getByTestId("composer-input").waitFor({ timeout: 30000 });
await page.getByTestId("panel-tab-files").click();
await page.waitForTimeout(3000);

console.log("[file-tree count]", await page.getByTestId("file-tree").count());
console.log("[file-node count]", await page.getByTestId("file-node").count());
const paths = await page.locator('[data-testid="file-node"]').evaluateAll((els) =>
  els.map((el) => ({
    path: el.getAttribute("data-path"),
    text: (el.textContent ?? "").trim().slice(0, 40),
  })),
);
console.log("[nodes]", JSON.stringify(paths));
const boxes = await page.getByTestId("file-tree-canvas").evaluateAll((els) =>
  els.map((el) => ({ w: el.clientWidth, h: el.clientHeight })),
);
console.log("[canvas size]", JSON.stringify(boxes));

await browser.close();
