import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, ensureProject, login } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-accept2");
mkdirSync(ARTIFACTS, { recursive: true });

/**
 * The new-session dialog offered `shared-anthropic` — a provider that ships
 * without a key — as an ordinary model. Choosing it cost three automatic
 * retries (~30s) and then a raw 502 in the transcript.
 *
 * A provider that cannot answer is not a choice. The row stays visible, marked
 * 缺少 API Key and disabled, with a link to the page where the key goes; this
 * test asserts all three, and that picking it does not change the selection.
 */
test("a keyless provider is shown disabled in the model picker, with the reason and a link", async ({
  page,
}) => {
  await login(page, USER);
  const projectId = await ensureProject(page, `keyless-picker-${Date.now()}`);
  await page.goto(`/p/${projectId}`);
  await expect(page.getByRole("button", { name: "新会话" }).first()).toBeVisible({
    timeout: 30_000,
  });
  await page.getByRole("button", { name: "新会话" }).first().click();

  const dialog = page.locator('[role="dialog"]').first();
  await expect(dialog).toBeVisible({ timeout: 20_000 });

  // Open the model select. The trigger is the combobox labelled by the field.
  const trigger = dialog.locator("#session-model");
  await expect(trigger).toBeVisible();
  await trigger.click();

  const keyless = page.locator(
    '[data-testid="session-model-option"][data-model="shared-anthropic/claude-sonnet-4-5"]',
  );
  await expect(keyless).toBeVisible({ timeout: 20_000 });
  await expect(keyless).toContainText("shared-anthropic");
  await expect(keyless).toContainText("缺少 API Key");
  await expect(keyless).toHaveAttribute("data-selectable", "false");
  await page.screenshot({ path: path.join(ARTIFACTS, "80-keyless-picker.png"), fullPage: true });

  // Picking it must not become the selection, even by force: the click handler
  // of a disabled item does not run.
  await keyless.click({ force: true });
  // A click on the dialog's own heading dismisses the list (a disabled row keeps
  // focus where it was, so Escape alone does not close it).
  await dialog.getByText("新会话", { exact: true }).click();
  await expect(trigger).toContainText("默认模型");
  await expect(trigger).not.toContainText("shared-anthropic");

  // The row explains itself in place, and points at the page where a key goes.
  const hint = dialog.getByTestId("model-blocked-hint");
  await expect(hint).toBeVisible();
  await expect(hint).toContainText("shared-anthropic");
  await expect(hint).toContainText("缺少 API Key");
  await expect(hint.getByRole("link", { name: "去填写" })).toHaveAttribute(
    "href",
    "/settings/providers",
  );
  await page.screenshot({ path: path.join(ARTIFACTS, "80-keyless-hint.png"), fullPage: true });
});
