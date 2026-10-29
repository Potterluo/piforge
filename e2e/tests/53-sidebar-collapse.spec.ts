import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login, openNewSession } from "./helpers";

/**
 * The left sidebar is collapsible, its collapsed rail is usable, and collapsing
 * the right panel gives the width back to the transcript.
 *
 * All of it is measured, not inferred: the user's complaint was a sidebar that
 * looked identical after pressing its control, and a right panel that folded to
 * a strip of icons while the transcript stayed exactly as narrow as before.
 */

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/sidebar-collapse");
mkdirSync(ARTIFACTS, { recursive: true });

declare global {
  interface Window {
    /** Sampled `app-sidebar` widths, to prove a reload never paints it expanded. */
    __sidebarWidths?: number[];
  }
}

const SIDES = [
  { width: 1440, height: 900 },
  { width: 1280, height: 800 },
] as const;

async function box(page: Page, testId: string) {
  const locator = page.getByTestId(testId);
  await expect(locator).toBeVisible();
  const rect = await locator.boundingBox();
  if (!rect) throw new Error(`${testId} has no box`);
  return rect;
}

/** The shell's main column; `SidebarInset` carries a slot, not a test id. */
async function mainColumn(page: Page) {
  const locator = page.locator('[data-slot="sidebar-inset"]');
  await expect(locator).toBeVisible();
  const rect = await locator.boundingBox();
  if (!rect) throw new Error("sidebar inset has no box");
  return rect;
}

async function noHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() => ({
    doc: document.documentElement.scrollWidth - window.innerWidth,
    body: document.body.scrollWidth - window.innerWidth,
  }));
  expect(overflow.doc, "document must not scroll horizontally").toBeLessThanOrEqual(0);
  expect(overflow.body, "body must not scroll horizontally").toBeLessThanOrEqual(0);
}

async function toggle(page: Page) {
  await page.getByTestId("sidebar-collapse-toggle").click();
}

async function waitForSidebarWidth(page: Page, expected: "expanded" | "collapsed") {
  // Wait for the 200ms width transition to finish, not just to start: measuring at
  // the first frame above the threshold recorded a half-collapsed sidebar.
  await expect
    .poll(
      async () => {
        const width = (await box(page, "app-sidebar")).width;
        return expected === "collapsed" ? width < 60 : width > 250;
      },
      { timeout: 15_000 },
    )
    .toBe(true);
}

for (const viewport of SIDES) {
  test(`the left sidebar collapses to an icon rail at ${viewport.width}x${viewport.height}`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await login(page, USER);
    await openNewSession(page);
    await expect(page.getByTestId("workspace-panels")).toBeVisible({ timeout: 40_000 });

    const expandedSidebar = await box(page, "app-sidebar");
    const expandedInset = await mainColumn(page);
    const expandedMain = await box(page, "transcript-column");
    expect(expandedSidebar.width).toBeGreaterThan(200);
    await page.screenshot({ path: path.join(ARTIFACTS, `sidebar-expanded-${viewport.width}.png`) });

    // The control sits in the header, says what it does, and carries the shortcut.
    const control = page.getByTestId("sidebar-collapse-toggle");
    await expect(control).toBeVisible();
    await expect(control).toHaveAccessibleName(/收起侧边栏/);
    await expect(control).toContainText("Ctrl B");

    await toggle(page);
    await waitForSidebarWidth(page, "collapsed");

    const collapsedSidebar = await box(page, "app-sidebar");
    const collapsedInset = await mainColumn(page);
    const collapsedMain = await box(page, "transcript-column");

    // Both states are real: the column shrank to a rail, and the room it gave up
    // went to the main column rather than into a gap.
    expect(collapsedSidebar.width).toBeLessThan(60);
    expect(collapsedSidebar.width).toBeLessThan(expandedSidebar.width - 150);
    expect(collapsedInset.width).toBeGreaterThan(expandedInset.width + 150);
    expect(collapsedMain.width).toBeGreaterThan(expandedMain.width + 60);
    expect(collapsedSidebar.x).toBe(0);

    // The way back is on screen: the rail, and the same toggle in its header.
    const rail = page.getByTestId("sidebar-rail");
    await expect(rail).toBeVisible();
    expect((await rail.boundingBox())?.width ?? 0).toBeLessThan(60);
    await expect(control).toBeVisible();
    const controlBox = await box(page, "sidebar-collapse-toggle");
    expect(controlBox.x).toBeGreaterThanOrEqual(0);
    expect(controlBox.x + controlBox.width).toBeLessThanOrEqual(viewport.width);

    // Every affordance the expanded sidebar has, the rail keeps as an icon.
    await expect(page.getByTestId("project-switcher")).toBeVisible();
    await expect(page.getByTestId("account-menu")).toBeVisible();
    await expect(page.locator('[data-sidebar="menu-button"][aria-label="新会话"]')).toBeVisible();
    await expect(page.getByTestId("project-row").first()).toBeVisible();

    await noHorizontalScroll(page);
    await page.screenshot({ path: path.join(ARTIFACTS, `sidebar-collapsed-${viewport.width}.png`) });

    // Hovering the rail's own toggle explains it.
    await control.hover();
    await expect(
      page.locator('[data-slot="tooltip-content"]', { hasText: "展开侧边栏" }).first(),
    ).toBeVisible({ timeout: 15_000 });

    // Keyboard reachable: focus the toggle, press Enter, and the sidebar returns.
    await control.focus();
    expect(await page.evaluate(() => document.activeElement?.getAttribute("data-testid"))).toBe(
      "sidebar-collapse-toggle",
    );
    await page.keyboard.press("Enter");
    await waitForSidebarWidth(page, "expanded");
    expect(Math.abs((await box(page, "app-sidebar")).width - expandedSidebar.width)).toBeLessThan(2);

    // And a plain round trip with the mouse still works.
    await toggle(page);
    await waitForSidebarWidth(page, "collapsed");
    await toggle(page);
    await waitForSidebarWidth(page, "expanded");
    // The split may redistribute a percent or two when the group is re-measured;
    // what matters is that the transcript is back to its expanded width, not that
    // a sub-pixel rounded layout is bit-identical.
    await expect
      .poll(async () => (await box(page, "transcript-column")).width, { timeout: 10_000 })
      .toBeGreaterThan(expandedMain.width - 8);
    await noHorizontalScroll(page);
  });
}

test("a collapsed sidebar survives a reload without an expanded flash", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, USER);
  await openNewSession(page);
  await expect(page.getByTestId("workspace-panels")).toBeVisible({ timeout: 40_000 });

  await toggle(page);
  await waitForSidebarWidth(page, "collapsed");
  expect(
    await page.evaluate(() => window.localStorage.getItem("piforge.sidebar.open")),
  ).toBe("false");

  // Sample the sidebar's width on every frame from the first paint, so "no flash"
  // is a measurement rather than a claim: a frame at 256px would be recorded.
  await page.addInitScript(() => {
    window.__sidebarWidths = [];
    const tick = () => {
      const el = document.querySelector('[data-testid="app-sidebar"]');
      if (el) {
        const width = Math.round(el.getBoundingClientRect().width);
        if (width > 0) window.__sidebarWidths!.push(width);
      }
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });

  await page.reload({ waitUntil: "domcontentloaded" });
  // Evidence at the exact moment the DOM is ready, before anything has settled.
  await page.screenshot({
    path: path.join(ARTIFACTS, "sidebar-reload-domcontentloaded-1440.png"),
  });

  await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 30_000 });
  await waitForSidebarWidth(page, "collapsed");
  expect((await box(page, "app-sidebar")).width).toBeLessThan(60);
  await expect(page.getByTestId("sidebar-rail")).toBeVisible();

  const widths = await page.evaluate(() => window.__sidebarWidths ?? []);
  expect(widths.length).toBeGreaterThan(5);
  expect(Math.max(...widths), "the sidebar must never paint expanded on reload").toBeLessThan(80);
});

test("Ctrl+B collapses the sidebar and the palette keeps Ctrl+K", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, USER);

  // Ctrl+K is the palette. Ctrl+B is the sidebar; neither may eat the other.
  await page.keyboard.press("Control+k");
  await expect(page.getByRole("dialog")).toBeVisible({ timeout: 10_000 });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect
    .poll(async () => (await box(page, "app-sidebar")).width)
    .toBeGreaterThan(200);

  await page.keyboard.press("Control+b");
  await waitForSidebarWidth(page, "collapsed");
  await page.keyboard.press("Control+b");
  await waitForSidebarWidth(page, "expanded");

  // The shortcut is advertised on the control, so it is discoverable.
  await expect(page.getByTestId("sidebar-collapse-toggle")).toContainText("Ctrl B");
});

for (const viewport of SIDES) {
  test(`collapsing the right panel gives its width to the transcript at ${viewport.width}x${viewport.height}`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await login(page, USER);
    await openNewSession(page);
    await expect(page.getByTestId("workspace-panels")).toBeVisible({ timeout: 40_000 });

    const before = await box(page, "transcript-column");
    await page.screenshot({
      path: path.join(ARTIFACTS, `right-expanded-${viewport.width}.png`),
    });

    await page.getByTestId("panel-collapse").click();
    const rail = page.getByTestId("workspace-rail");
    await expect(rail).toBeVisible();
    await expect(page.getByTestId("workspace-panels")).toHaveCount(0);

    // The rail sits at the far edge of the viewport, exactly as wide as it is.
    const railBox = await box(page, "workspace-rail");
    expect(railBox.width).toBeLessThan(60);
    expect(Math.abs(railBox.x + railBox.width - viewport.width)).toBeLessThan(2);

    // The transcript really gets the room back: at least 200px of it.
    await expect
      .poll(async () => (await box(page, "transcript-column")).width, { timeout: 15_000 })
      .toBeGreaterThan(before.width + 200);
    const after = await box(page, "transcript-column");
    await noHorizontalScroll(page);
    await page.screenshot({
      path: path.join(ARTIFACTS, `right-collapsed-${viewport.width}.png`),
    });

    // Every icon on the strip has an accessible name and a real tooltip.
    for (const testId of ["panel-expand", "rail-changes", "rail-git", "rail-browser", "rail-terminal"]) {
      const label = await page.getByTestId(testId).getAttribute("aria-label");
      expect(label?.trim(), `${testId} needs an accessible name`).toBeTruthy();
    }
    await page.getByTestId("rail-terminal").hover();
    await expect(
      page.locator('[data-slot="tooltip-content"]', { hasText: "打开终端面板" }).first(),
    ).toBeVisible({ timeout: 15_000 });
    await page.screenshot({
      path: path.join(ARTIFACTS, `right-rail-tooltip-${viewport.width}.png`),
    });

    // Reachable from the keyboard: focus an icon, press Enter, the panel returns.
    await page.getByTestId("rail-git").focus();
    expect(await page.evaluate(() => document.activeElement?.getAttribute("data-testid"))).toBe(
      "rail-git",
    );
    await page.keyboard.press("Enter");
    await expect(page.getByTestId("workspace-panels")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("panel-tab-git")).toHaveAttribute("aria-selected", "true");
    await expect
      .poll(async () => (await box(page, "transcript-column")).width)
      .toBeGreaterThan(before.width - 3);
    expect(after.width).toBeGreaterThan(before.width + 200);
    await page.screenshot({
      path: path.join(ARTIFACTS, `right-restored-${viewport.width}.png`),
    });
  });
}

test("the project list filters as you type and can show all 40+ projects", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, USER);

  // Forty is what a real account accumulates; the sidebar has to stay usable.
  const stamp = Date.now();
  const prefix = `qa53-${stamp}`;
  for (let index = 1; index <= 42; index += 1) {
    const created = await apiWrite(page, "POST", "/v1/projects", {
      name: `${prefix}-${String(index).padStart(2, "0")}`,
      repo_url: "",
      default_branch: "main",
    });
    expect(created.status).toBeLessThan(300);
  }

  // A reload picks up the new projects; the sidebar is client-rendered after auth.
  await page.goto("/");
  const filter = page.getByTestId("project-filter");
  await expect(filter).toBeVisible({ timeout: 20_000 });
  const rows = page.getByTestId("project-row");

  // The default view is "最近", not a wall of forty rows.
  await expect(page.getByTestId("project-list-scope")).toContainText("最近");
  const recent = await rows.count();
  expect(recent).toBeLessThanOrEqual(7);

  const total = Number(
    (await page.getByTestId("project-list-scope").innerText()).match(/共 (\d+) 个/)?.[1] ?? 0,
  );
  expect(total).toBeGreaterThanOrEqual(42);

  // Typing filters every project, not just the recent slice.
  await filter.fill(prefix);
  await expect(rows).toHaveCount(42, { timeout: 15_000 });
  await expect(page.getByTestId("project-list-scope")).toContainText("匹配 42 个");
  expect(await rows.first().innerText()).toContain(prefix);
  await page.screenshot({ path: path.join(ARTIFACTS, "sidebar-filter-qa53.png") });

  // A few more characters narrow it further.
  await filter.fill(`${prefix}-01`);
  await expect(rows).toHaveCount(1, { timeout: 15_000 });
  await expect(rows.first()).toContainText(`${prefix}-01`);
  await page.screenshot({ path: path.join(ARTIFACTS, "sidebar-filter-narrow.png") });

  // A query with no hits says so instead of showing nothing.
  await filter.fill(`${prefix}-does-not-exist`);
  await expect(page.getByTestId("project-empty")).toBeVisible();

  // Clearing the filter restores the recent slice, and "查看全部" still lists all.
  await filter.fill("");
  await expect(rows).toHaveCount(recent);
  await page.getByTestId("project-show-all").click();
  await expect(rows).toHaveCount(total, { timeout: 15_000 });
  await page.screenshot({ path: path.join(ARTIFACTS, "sidebar-all-projects.png") });

  // Nothing was deleted: the rows are the user's projects, all of them.
  expect(total).toBeGreaterThanOrEqual(42);
});


