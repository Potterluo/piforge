import { expect, test } from "@playwright/test";

import { USER, login } from "./helpers";

/**
 * QA-13 finding A: settings had no navigation.
 *
 * 设置 always landed on /settings/providers, the other four pages were only
 * reachable through Ctrl+K, and nothing on screen opened the palette. This spec
 * walks the visible navigation on every settings page and opens the palette with
 * a real button instead of a keyboard shortcut.
 */
test("settings exposes a visible navigation on every page and a palette trigger", async ({
  page,
}) => {
  await login(page, USER);
  await page.getByRole("link", { name: "设置", exact: true }).first().click();
  await expect(page).toHaveURL(/\/settings\/providers$/, { timeout: 20_000 });

  const nav = page.getByTestId("settings-nav");
  await expect(nav).toBeVisible();

  const pages = [
    { slug: "providers", heading: "模型与凭据", path: "/settings/providers" },
    { slug: "skills", heading: "Skill 中心", path: "/settings/skills" },
    { slug: "mcp", heading: "MCP 中心", path: "/settings/mcp" },
    { slug: "api-keys", heading: "API Key", path: "/settings/api-keys" },
    { slug: "general", heading: "通用", path: "/settings/general" },
  ];

  // Every destination is named in the navigation, on the page it belongs to.
  for (const item of pages) {
    await expect(page.getByTestId(`settings-nav-${item.slug}`)).toBeVisible();
  }

  await expect(page.getByTestId("settings-nav-providers")).toHaveAttribute(
    "aria-current",
    "page",
  );

  for (const item of pages) {
    await page.getByTestId(`settings-nav-${item.slug}`).click();
    await expect(page).toHaveURL(new RegExp(`${item.path}$`), { timeout: 20_000 });
    await expect(page.locator("h1").first()).toContainText(item.heading);
    // The navigation is part of the settings frame, not of one page.
    await expect(page.getByTestId("settings-nav")).toBeVisible();
    await expect(page.getByTestId(`settings-nav-${item.slug}`)).toHaveAttribute(
      "aria-current",
      "page",
    );
  }

  // A visible door to the palette, with an accessible name — Ctrl+K is not the
  // only way in any more. Next keeps a hidden copy of the previous route's shell
  // in the DOM (0×0), so the assertion is on the visible one.
  const trigger = page.locator('[data-testid="command-palette-trigger"]:visible');
  await expect(trigger).toHaveCount(1);
  await expect(trigger).toHaveAttribute("aria-label", "打开命令面板");
  await trigger.click();
  const search = page.locator('input[placeholder="搜索命令、项目…"]:visible');
  await expect(search).toBeVisible({ timeout: 10_000 });
  // The palette still offers the settings destinations it always did.
  await expect(page.locator('[role="dialog"]').filter({ hasText: "模型与凭据" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(search).toBeHidden({ timeout: 10_000 });
});
