// QA-15 verification probe against a locally-served build (BASE=http://127.0.0.1:3100)
// pointed at the deployed control plane, so it sees the real 35 QA projects.
//   node e2e/scripts/qa15-verify.mjs
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/sidebar-collapse");
mkdirSync(out, { recursive: true });
const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const log = { base: BASE, checks: {}, errors: [] };
const save = () => writeFileSync(path.join(out, "verify-log.json"), JSON.stringify(log, null, 2));

const browser = await chromium.launch({ channel: "chrome" });

async function newPage(viewport) {
  const context = await browser.newContext({ viewport });
  const page = await context.newPage();
  page.on("console", (m) => m.type() === "error" && log.errors.push(`${viewport.width}: ${m.text()}`));
  page.on("pageerror", (e) => log.errors.push(`${viewport.width}: pageerror ${e.message}`));
  return { context, page };
}

async function login(page) {
  await page.goto(`${BASE}/login`, { waitUntil: "domcontentloaded" });
  await page.getByTestId("login-email").fill("dev@piforge.local");
  await page.getByTestId("login-password").fill("piforge-dev-123");
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ state: "visible", timeout: 30000 });
}

const metrics = (page) =>
  page.evaluate(() => {
    const rect = (sel) => {
      const el = document.querySelector(sel);
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { x: Math.round(r.x), w: Math.round(r.width), h: Math.round(r.height) };
    };
    const sidebar = document.querySelector('[data-testid="app-sidebar"]');
    const toggle = document.querySelector('[data-testid="sidebar-collapse-toggle"]');
    const tRect = toggle?.getBoundingClientRect();
    return {
      innerWidth: window.innerWidth,
      docScrollWidth: document.documentElement.scrollWidth,
      bodyScrollWidth: document.body.scrollWidth,
      appSidebar: rect('[data-testid="app-sidebar"]'),
      sidebarState: sidebar?.getAttribute("data-state"),
      rail: rect('[data-testid="sidebar-rail"]'),
      toggle: tRect
        ? { x: Math.round(tRect.x), w: Math.round(tRect.width), h: Math.round(tRect.height), label: toggle.getAttribute("aria-label") }
        : null,
      transcript: rect('[data-testid="transcript-column"]'),
      workspacePanels: rect('[data-testid="workspace-panels"]'),
      workspaceRail: rect('[data-testid="workspace-rail"]'),
      projectRows: document.querySelectorAll('[data-testid="project-row"]').length,
      projectFilter: Boolean(document.querySelector('[data-testid="project-filter"]')),
      totalText: document.querySelector('[data-testid="project-list-scope"]')?.textContent?.trim(),
    };
  });

const shot = (page, name) => page.screenshot({ path: path.join(out, `${name}.png`) });

for (const viewport of [{ width: 1440, height: 900 }, { width: 1280, height: 800 }]) {
  const tag = viewport.width;
  const { context, page } = await newPage(viewport);
  await login(page);
  await page.goto(`${BASE}/`, { waitUntil: "networkidle" });
  await page.locator('a[href^="/p/"]').first().waitFor();
  // A project page will not do: the workbench (and its right panel) is the target.
  const projectHref = await page.locator('a[href^="/p/"]').first().getAttribute("href");
  await page.goto(`${BASE}${projectHref}`, { waitUntil: "networkidle" });
  const sessionHref = await page.locator('[data-testid="session-link"]').first().getAttribute("href");
  await page.goto(`${BASE}${sessionHref}`, { waitUntil: "networkidle" });
  await page.waitForTimeout(1200);

  log.checks[`${tag}-expanded`] = await metrics(page);
  await shot(page, `expanded-${tag}`);

  // --- left sidebar collapse -------------------------------------------------
  for (let attempt = 0; attempt < 3; attempt++) {
    await page.getByTestId("sidebar-collapse-toggle").click();
    await page.waitForTimeout(450);
    const state = await page.evaluate(
      () => document.querySelector('[data-slot="sidebar"]')?.getAttribute("data-state"),
    );
    if (state === "collapsed") break;
    log.checks[`${tag}-clickAttempts`] = (log.checks[`${tag}-clickAttempts`] ?? 0) + 1;
  }
  log.checks[`${tag}-annotated`] = await metrics(page);
  log.checks[`${tag}-collapsed`] = await metrics(page);
  await shot(page, `collapsed-${tag}`);

  // Hover the collapsed toggle: the way back must announce itself.
  await page.getByTestId("sidebar-collapse-toggle").hover();
  await page.waitForTimeout(600);
  log.checks[`${tag}-toggleTooltip`] = await page.evaluate(() => {
    const tip = document.querySelector('[data-slot="tooltip-content"], [role="tooltip"]');
    return tip ? { text: tip.textContent?.trim(), visible: tip.getBoundingClientRect().width > 0 } : null;
  });
  await shot(page, `collapsed-${tag}-tooltip`);

  // --- Ctrl+B round trip -----------------------------------------------------
  await page.keyboard.press("Control+b");
  await page.waitForTimeout(400);
  log.checks[`${tag}-afterCtrlB`] = await metrics(page);
  await page.keyboard.press("Control+b");
  await page.waitForTimeout(400);
  log.checks[`${tag}-afterCtrlBTwice`] = await metrics(page);

  // --- right panel collapse --------------------------------------------------
  const before = await metrics(page);
  await page.getByTestId("panel-collapse").click();
  await page.waitForTimeout(600);
  const after = await metrics(page);
  log.checks[`${tag}-panel`] = { before, after, delta: (after.transcript?.w ?? 0) - (before.transcript?.w ?? 0) };
  await shot(page, `right-collapsed-${tag}`);

  const railButtons = await page.evaluate(() =>
    [...document.querySelectorAll('[data-testid="workspace-rail"] button')].map((b) => ({
      label: b.getAttribute("aria-label"),
      testid: b.getAttribute("data-testid"),
      w: Math.round(b.getBoundingClientRect().width),
      h: Math.round(b.getBoundingClientRect().height),
    })),
  );
  log.checks[`${tag}-railButtons`] = railButtons;

  await page.getByTestId("rail-terminal").hover();
  await page.waitForTimeout(700);
  log.checks[`${tag}-railTooltip`] = await page.evaluate(() => {
    const tip = document.querySelector('[data-slot="tooltip-content"], [role="tooltip"]');
    return tip ? { text: tip.textContent?.trim(), visible: tip.getBoundingClientRect().width > 0 } : null;
  });
  await shot(page, `right-rail-tooltip-${tag}`);

  // Keyboard: focus the rail and expand the panel with Enter.
  await page.getByTestId("rail-git").focus();
  log.checks[`${tag}-railFocused`] = await page.evaluate(() => document.activeElement?.getAttribute("data-testid"));
  await page.keyboard.press("Enter");
  await page.waitForTimeout(600);
  log.checks[`${tag}-afterRailEnter`] = await metrics(page);
  await shot(page, `right-expanded-by-keyboard-${tag}`);

  // --- project filter --------------------------------------------------------
  // The filter only exists in the expanded sidebar by design, so make sure it is
  // expanded before measuring the list.
  const currentState = await page.evaluate(
    () => document.querySelector('[data-slot="sidebar"]')?.getAttribute("data-state"),
  );
  if (currentState === "collapsed") {
    await page.getByTestId("sidebar-collapse-toggle").click();
    await page.waitForTimeout(450);
  }
  log.checks[`${tag}-filterCount`] = await page.getByTestId("project-filter").count();
  if ((await page.getByTestId("project-filter").count()) > 0) {
    await page.getByTestId("project-filter").waitFor({ state: "visible" });
    const total = Number((await page.getByTestId("project-list-scope").innerText()).match(/共 (\d+) 个/)?.[1] ?? 0);
    await page.getByTestId("project-filter").fill("qa13");
    await page.waitForTimeout(300);
    const narrowed = await page.locator('[data-testid="project-row"]').count();
    await shot(page, `filter-qa13-${tag}`);
    await page.getByTestId("project-clear-filter").click();
    await page.waitForTimeout(300);
    const cleared = await page.locator('[data-testid="project-row"]').count();
    await page.getByTestId("project-show-all").click();
    await page.waitForTimeout(300);
    const all = await page.locator('[data-testid="project-row"]').count();
    await shot(page, `all-projects-${tag}`);
    log.checks[`${tag}-filter`] = { total, narrowed, cleared, all };
  }

  await context.close();
}

await browser.close();
save();
console.log(JSON.stringify(log, null, 2));
