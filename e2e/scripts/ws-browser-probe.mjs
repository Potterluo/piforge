import { chromium } from "@playwright/test";
const BASE = process.env.BASE ?? "http://127.0.0.1:7733";
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const project = listed.projects.find((p) => p.name === "browser-proof");
const session = (await (await page.request.post(`${BASE}/v1/projects/${project.id}/sessions`, {
  data: { title: "ws probe", model_ref: "vold/deepseek-v4-flash" },
  headers: { "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "" },
})).json()).session;

const wsUrl = `ws://127.0.0.1:${new URL(BASE).port}/v1/sessions/${session.id}/browser`;
const result = await page.evaluate((url) => new Promise((resolve) => {
  const frames = [];
  const ws = new WebSocket(url);
  ws.onmessage = (e) => frames.push(String(e.data).slice(0, 300));
  ws.onerror = () => frames.push("ERROR");
  ws.onclose = (e) => resolve({ frames, code: e.code, reason: e.reason });
  setTimeout(() => { try { ws.close(); } catch {} resolve({ frames, timeout: true }); }, 12000);
}), wsUrl);
console.log(JSON.stringify(result, null, 2).slice(0, 1200));
await browser.close();
