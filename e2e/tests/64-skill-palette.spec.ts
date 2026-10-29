import { expect, test } from "@playwright/test";

import { USER, login, openNewSession } from "./helpers";

/**
 * FR-14.6: `/` in the composer opens a skill palette that works with the
 * keyboard alone, filters as you type, closes on Escape and inserts the same
 * `/skill:name` invocation the backend already understands.
 *
 * "Invoke" is not a second mechanism: the inserted text is sent as a normal
 * prompt, and the skill is already mounted at `.pi/skills/<name>/SKILL.md` for
 * the agent to discover. The test checks both halves — the message and the mount.
 */
test("the / palette opens, filters, inserts and invokes a mounted skill", async ({ page }) => {
  await login(page, USER);
  const sessionId = await openNewSession(page);

  const input = page.getByTestId("composer-input");
  await input.click();

  // --- open with `/` --------------------------------------------------------
  await page.keyboard.type("/");
  const palette = page.getByTestId("skill-palette");
  await expect(palette).toBeVisible();
  await expect(page.getByRole("listbox", { name: "Skill 命令" })).toBeVisible();
  await expect(input).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByTestId("composer-skill-chip")).toHaveCount(0);

  const items = page.getByTestId("skill-palette-item");
  await expect(items.first()).toBeVisible();
  const names = await items.evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-name") ?? ""),
  );
  expect(names).toEqual(expect.arrayContaining(["code-review", "test-gap"]));
  // Every row carries the current version/hash, derived by the server.
  const first = items.first();
  await expect(first).toContainText(/v\d+\.\d+\.\d+/);
  await expect(first).toContainText(/[0-9a-f]{8}/);

  // --- arrow keys move the selection ---------------------------------------
  await page.keyboard.press("ArrowDown");
  await expect(items.nth(1)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("ArrowUp");
  await expect(items.first()).toHaveAttribute("aria-selected", "true");

  // --- Escape closes without sending or interrupting ------------------------
  await page.keyboard.press("Escape");
  await expect(palette).toHaveCount(0);
  await expect(input).toHaveAttribute("aria-expanded", "false");
  await expect(input).toHaveValue("/");
  await expect(page.getByTestId("turn-user")).toHaveCount(0);

  // --- filter --------------------------------------------------------------
  await input.press("Control+A");
  await input.press("Backspace");
  await page.keyboard.type("/test-gap");
  await expect(palette).toBeVisible();
  await expect(items).toHaveCount(1);
  await expect(items.first()).toHaveAttribute("data-name", "test-gap");

  await input.press("Control+A");
  await input.press("Backspace");
  await page.keyboard.type("/zzz-nothing-matches");
  await expect(page.getByTestId("skill-palette-no-match")).toContainText("没有匹配");

  // --- Enter inserts the invocation and shows the version chip --------------
  await input.press("Control+A");
  await input.press("Backspace");
  await page.keyboard.type("/test-gap");
  await expect(items).toHaveCount(1);
  await input.press("Enter");

  await expect(palette).toHaveCount(0);
  await expect(input).toHaveValue("/skill:test-gap ");
  const chip = page.getByTestId("composer-skill-chip");
  await expect(chip).toBeVisible();
  await expect(chip).toHaveAttribute("data-name", "test-gap");
  const chipHash = await chip.getAttribute("data-hash");
  const chipVersion = await chip.getAttribute("data-version");
  expect(chipHash).toMatch(/^[0-9a-f]{64}$/);
  expect(chipVersion).toBeTruthy();
  await expect(chip).toContainText("本会话挂载的版本");

  // Arguments are part of the message; the inserted text makes the shape clear.
  await page.keyboard.type("列出这次改动里缺少测试覆盖的行为");

  // --- invoke: send it as an ordinary prompt --------------------------------
  await input.press("Enter");
  await expect(page.getByTestId("turn-user")).toHaveCount(1, { timeout: 30_000 });
  const turn = page.getByTestId("turn-user").first();
  await expect(turn).toContainText("/skill:test-gap");
  await expect(turn).toContainText("列出这次改动里缺少测试覆盖的行为");
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
    timeout: 120_000,
  });

  // --- the run was actually given that skill --------------------------------
  const records = (await (
    await page.request.get(`/v1/sessions/${sessionId}/skills`)
  ).json()) as {
    skills: { name: string; version: string; content_hash: string; mounted_path: string }[];
  };
  const testGap = records.skills.find((skill) => skill.name === "test-gap");
  expect(testGap).toBeTruthy();
  expect(testGap?.version).toBe(chipVersion);
  expect(testGap?.content_hash).toBe(chipHash);
  expect(testGap?.mounted_path).toContain(".pi/skills/test-gap");

  const tooling = (await (
    await page.request.get(`/v1/sessions/${sessionId}/tooling`)
  ).json()) as { mounted: { skills: string[] } };
  expect(tooling.mounted.skills).toContain("test-gap");

  const file = await page.request.get(
    `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(".pi/skills/test-gap/SKILL.md")}`,
  );
  expect(file.status()).toBe(200);
  expect(((await file.json()) as { body: string }).body).toContain("test-gap");

  // The version record is visible in the session's logs/info sheet, read-only.
  await page.getByTestId("session-logs-open").click();
  const recorded = page.locator('[data-testid="session-skill-item"][data-name="test-gap"]');
  await expect(recorded).toBeVisible();
  await expect(recorded).toHaveAttribute("data-hash", chipHash ?? "");
});
