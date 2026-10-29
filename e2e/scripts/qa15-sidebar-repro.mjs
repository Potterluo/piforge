// QA-15 repro: the left sidebar as the user saw it. Logs in on the deployed app,
// opens a session page, and records whether a collapse control is actually
// visible, how wide the sidebar is, how many projects the list holds, and what
// the right rail looks like. Screenshots land in e2e/.artifacts/sidebar-collapse/.
//
//   node e2e/scripts/qa15-sidebar-repro.mjs
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/sidebar-collapse");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const log = { base: BASE, steps: [], checks: {} };
const save = () => writeFileSync(path.join(out, "repro-log.json"), JSON.stringify(log, null, 2));

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
const errors = [];
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`));

await page.goto(`${BASE}/login`, { waitUntil: "domcontentloaded" });
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ state: "visible", timeout: 30000 });

// Go to the first project, then the first session if one exists.
await page.goto(`${BASE}/`, { waitUntil: "networkidle" });
const projectLinks = page.locator('[data-sidebar="menu-button"] a[href^="/p/"]');
const projectCount = await page.locator('[data-sidebar="menu"] a[href^="/p/"]').count();
log.checks.projectLinkCount = projectCount;

const firstProject = await page.locator('a[href^="/p/"]').first().getAttribute("href");
log.steps.push({ firstProject });
await page.goto(`${BASE}${firstProject}`, { waitUntil: "networkidle" });
const sessionHref = await page.locator('[data-testid="session-link"]').first().getAttribute("href");
log.steps.push({ sessionHref });
if (sessionHref) await page.goto(`${BASE}${sessionHref}`, { waitUntil: "networkidle" });
await page.waitForTimeout(800);

const measure = async (label) => {
  const data = await page.evaluate(() => {
    const sidebar = document.querySelector('[data-slot="sidebar"]');
    const container = document.querySelector('[data-slot="sidebar-container"]');
    const gap = document.querySelector('[data-slot="sidebar-gap"]');
    const inset = document.querySelector('[data-slot="sidebar-inset"]');
    const trigger = document.querySelector('[data-testid="sidebar-collapse"]');
    const topTrigger = document.querySelector('[data-testid="sidebar-trigger"]');
    const rect = (el) => (el ? el.getBoundingClientRect().toJSON() : null);
    const visible = (el) => {
      if (!el) return false;
      const r = el.getBoundingClientRect();
      const style = getComputedStyle(el);
      return r.width > 0 && r.height > 0 && style.visibility !== "hidden" && style.display !== "none";
    };
    return {
      innerWidth: window.innerWidth,
      scrollWidth: document.documentElement.scrollWidth,
      bodyScrollWidth: document.body.scrollWidth,
      sidebarState: sidebar?.getAttribute("data-state"),
      sidebarCollapsible: sidebar?.getAttribute("data-collapsible"),
      sidebarRect: rect(sidebar),
      containerRect: rect(container),
      gapRect: rect(gap),
      insetRect: rect(inset),
      trigger: { exists: Boolean(trigger), visible: visible(trigger), rect: rect(trigger), text: trigger?.getAttribute("aria-label") },
      topTrigger: { exists: Boolean(topTrigger), visible: visible(topTrigger), rect: rect(topTrigger) },
      mainScrollWidth: inset?.scrollWidth,
      mainClientWidth: inset?.clientWidth,
    };
  });
  log.checks[label] = data;
  return data;
};

const before = await measure("expanded");
await page.screenshot({ path: path.join(out, "repro-expanded-1440.png"), fullPage: false });

// What does the user see as a collapse affordance?
const candidates = await page.evaluate(() => {
  const hits = [];
  const nodes = document.querySelectorAll("button, [role=button]");
  for (const node of nodes) {
    const r = node.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    const label = `${node.getAttribute("aria-label") ?? ""} ${node.getAttribute("title") ?? ""} ${node.textContent ?? ""}`.trim();
    if (/侧边栏|sidebar/i.test(label)) {
      hits.push({ label, testid: node.getAttribute("data-testid"), rect: r.toJSON() });
    }
  }
  return hits;
});
log.checks.collapseAffordances = candidates;

// Try the control the code claims exists.
const toggle = page.getByTestId("sidebar-collapse");
log.checks.toggleCount = await toggle.count();
if ((await toggle.count()) > 0) {
  log.checks.toggleVisible = await toggle.first().isVisible().catch(() => false);
  try {
    await toggle.first().click({ timeout: 5000 });
    await page.waitForTimeout(600);
    log.checks.afterToggleClick = await measure("afterToggleClick");
    await page.screenshot({ path: path.join(out, "repro-after-toggle-click-1440.png") });
  } catch (error) {
    log.checks.toggleClickError = String(error);
  }
}

// The right panel strip next to it.
const rail = await page.evaluate(() => {
  const panel = document.querySelector('[data-testid="workspace-panel"]') ?? document.querySelector('[data-testid="sidecar-dock"]');
  const buttons = [...document.querySelectorAll("button")].filter((b) => {
    const r = b.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && r.left > window.innerWidth - 90 && r.width <= 48;
  });
  return {
    panelFound: Boolean(panel),
    railButtons: buttons.slice(0, 20).map((b) => ({
      title: b.getAttribute("title"),
      ariaLabel: b.getAttribute("aria-label"),
      text: (b.textContent ?? "").trim().slice(0, 40),
      testid: b.getAttribute("data-testid"),
      rect: b.getBoundingClientRect().toJSON(),
    })),
  };
});
log.checks.rightRail = rail;
log.checks.consoleErrors = errors;
save();
await browser.close();
console.log(JSON.stringify(log.checks, null, 2));
