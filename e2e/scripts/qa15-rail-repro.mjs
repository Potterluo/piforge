// QA-15 repro #2: the right inspector when collapsed to its rail.
//   node e2e/scripts/qa15-rail-repro.mjs
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/sidebar-collapse");
mkdirSync(out, { recursive: true });
const BASE = process.env.BASE ?? "http://127.0.0.1:7722";

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
await page.goto(`${BASE}/login`, { waitUntil: "domcontentloaded" });
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ state: "visible", timeout: 30000 });
await page.waitForTimeout(500);

const projectHref = await page.locator('a[href^="/p/"]').first().getAttribute("href");
await page.goto(`${BASE}${projectHref}`, { waitUntil: "networkidle" });
const sessionHref = await page.locator('[data-testid="session-link"]').first().getAttribute("href");
await page.goto(`${BASE}${sessionHref}`, { waitUntil: "networkidle" });
await page.getByTestId("panel-collapse").click();
await page.waitForTimeout(600);

const rail = await page.evaluate(() => {
  const node = document.querySelector('[data-testid="workspace-rail"]');
  const r = node?.getBoundingClientRect();
  const buttons = [...(node?.querySelectorAll("button") ?? [])].map((b) => ({
    ariaLabel: b.getAttribute("aria-label"),
    title: b.getAttribute("title"),
    testid: b.getAttribute("data-testid"),
    text: (b.textContent ?? "").trim(),
    w: Math.round(b.getBoundingClientRect().width),
    h: Math.round(b.getBoundingClientRect().height),
  }));
  const styles = node ? getComputedStyle(node) : null;
  return {
    railRect: r?.toJSON(),
    buttons,
    railBg: styles?.backgroundColor,
    railBorderLeft: styles?.borderLeftColor,
    inset: document.querySelector('[data-slot="sidebar-inset"]')?.getBoundingClientRect().toJSON(),
  };
});
await page.screenshot({ path: path.join(out, "repro-right-rail.png") });

// Hover the first rail icon and see whether a real tooltip appears.
const first = page.locator('[data-testid="workspace-rail"] button').nth(1);
await first.hover();
await page.waitForTimeout(700);
const tooltip = await page.evaluate(() => {
  const all = [...document.querySelectorAll('[role="tooltip"], [data-slot="tooltip-content"]')];
  return all.map((n) => ({ text: n.textContent, visible: n.getBoundingClientRect().width > 0 }));
});
await page.screenshot({ path: path.join(out, "repro-right-rail-hover.png") });
writeFileSync(path.join(out, "rail-log.json"), JSON.stringify({ rail, tooltip }, null, 2));
console.log(JSON.stringify({ rail, tooltip }, null, 2));
await browser.close();
