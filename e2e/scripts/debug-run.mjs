// Diagnostic: does a prompt actually start and settle a run, and does the SSE
// stream deliver events through the Next proxy?
import { chromium } from "@playwright/test";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();

page.on("console", (m) => {
  if (m.type() === "error") console.log("[console:error]", m.text());
});
page.on("pageerror", (e) => console.log("[pageerror]", e.message));

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
  data: { name: `d2-${Date.now()}`, repo_url: "", default_branch: "main" },
});
const projectId = (await project.json()).project.id;
const sess = await page.request.post(`${BASE}/v1/projects/${projectId}/sessions`, {
  headers,
  data: { title: "d2" },
});
const sessionId = (await sess.json()).session.id;
console.log("[ids]", sessionId);

const t0 = Date.now();
const send = await page.request.post(`${BASE}/v1/sessions/${sessionId}/messages`, {
  headers,
  data: { client_msg_id: `cm_${t0}`, text: "fix the cache race" },
});
console.log("[send]", send.status(), JSON.stringify(await send.json()).slice(0, 200));

for (let i = 0; i < 40; i++) {
  await page.waitForTimeout(400);
  const st = await page.request.get(`${BASE}/v1/sessions/${sessionId}/status`);
  const body = await st.json();
  if (i % 5 === 0) {
    console.log(`[status ${i}]`, st.status(), "running:", body.running, "streaming:", body.streaming);
  }
  if (!body.running && !body.streaming) {
    console.log("[settled after]", Date.now() - t0, "ms");
    break;
  }
}

const ev = await page.request.get(`${BASE}/v1/sessions/${sessionId}/events?after_seq=0`, {
  timeout: 4000,
}).catch((e) => ({ status: () => "TIMEOUT", text: async () => String(e) }));
console.log("[sse]", ev.status());
const text = await ev.text();
console.log("[sse bytes]", text.length);
console.log("[sse head]", text.slice(0, 300).replace(/\n/g, " | "));

await browser.close();
