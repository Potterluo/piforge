import path from "node:path";

import { expect, test } from "@playwright/test";

import { ADMIN, apiWrite, login, openNewSession } from "./helpers";
import { freshAccount } from "./isolation";

const ARTIFACTS = path.join(process.cwd(), ".artifacts", "fix-palette-shadow");

/**
 * FR-14.1 「同名覆盖」: a normal user's private skill with the same name as a
 * shared one is the version their sessions mount, and the list says so instead
 * of showing two identical rows.
 *
 * The shared row is deployment-wide, so this spec creates it as the admin and
 * deletes it in a `finally` (a leftover makes the next run's pre-flight see four
 * shared skills where it expects three). The private row lives in a throwaway
 * account, so it cannot leak into another spec or the next run.
 */
test("a private skill shadows the shared one, and disabling it falls back", async ({
  page,
  browser,
}) => {
  await login(page, ADMIN);

  const stamp = Date.now();
  const name = `review-${stamp}`;
  const sharedBody = `---\nname: ${name}\ndescription: 共享审查口径\n---\n\n# 共享版本\n`;
  const privateBody = `---\nname: ${name}\ndescription: 我的审查口径\n---\n\n# 私有版本\n`;

  const created = await apiWrite(page, "POST", "/v1/skills", {
    scope: "admin",
    name,
    version: "1.0.0",
    description: "共享审查口径",
    body: sharedBody,
  });
  expect(created.status).toBe(201);
  const sharedId = (created.body as { skill: { id: string } }).skill.id;

  try {
    const other = await browser.newContext();
    const userPage = await other.newPage();
    try {
      await freshAccount(userPage, "skill-shadow");
      await userPage.setViewportSize({ width: 1440, height: 900 });

      // The user's own skill has the same name and a higher version; the rule is
      // about scope, so this is the one their sessions must get.
      const mine = await apiWrite(userPage, "POST", "/v1/skills", {
        name,
        version: "2.0.0",
        description: "我的审查口径",
        body: privateBody,
      });
      expect(mine.status).toBe(201);
      const privateId = (mine.body as { skill: { id: string } }).skill.id;

      // --- the API resolves which row is in effect --------------------------
      const listed = (await (await userPage.request.get("/v1/skills")).json()) as {
        skills: { id: string; scope: string; enabled: boolean; effective: boolean }[];
      };
      const rows = listed.skills.filter((skill) => skill.name === name);
      expect(rows).toHaveLength(2);
      expect(rows.find((skill) => skill.id === privateId)?.effective).toBe(true);
      expect(rows.find((skill) => skill.id === sharedId)?.effective).toBe(false);

      // --- the settings list explains the two rows --------------------------
      await userPage.goto("/settings/skills");
      const privateRow = userPage.locator(
        `[data-testid="skill-item"][data-name="${name}"][data-scope="user"]`,
      );
      const sharedRow = userPage.locator(
        `[data-testid="skill-item"][data-name="${name}"][data-scope="admin"]`,
      );
      await expect(privateRow).toBeVisible({ timeout: 20_000 });
      await expect(privateRow).toHaveAttribute("data-effective", "true");
      await expect(privateRow.getByTestId("skill-effective")).toContainText("生效中");
      await expect(privateRow.getByTestId("skill-overrides")).toContainText("覆盖共享版本");

      // The shared row is still listed — the user has to be able to see what
      // their copy hides — but it says it is shadowed.
      await expect(sharedRow).toBeVisible();
      await expect(sharedRow).toHaveAttribute("data-effective", "false");
      await expect(sharedRow).toHaveAttribute("data-shadowed", "true");
      await expect(sharedRow.getByTestId("skill-shadowed")).toContainText("覆盖");
      await expect(sharedRow.getByTestId("skill-shadow-explain")).toContainText("回退");
      await userPage.screenshot({ path: path.join(ARTIFACTS, "shadow-settings.png") });

      // --- a new session mounts the private bundle --------------------------
      const sessionId = await openNewSession(userPage);
      const mounted = await userPage.request.get(
        `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(`.pi/skills/${name}/SKILL.md`)}`,
      );
      expect(mounted.status()).toBe(200);
      expect(((await mounted.json()) as { body: string }).body).toContain("# 私有版本");

      // The session record names the row that was really mounted (FR-14.5).
      const records = (await (
        await userPage.request.get(`/v1/sessions/${sessionId}/skills`)
      ).json()) as { skills: { name: string; skill_id: string; version: string }[] };
      const record = records.skills.find((skill) => skill.name === name);
      expect(record?.skill_id).toBe(privateId);
      expect(record?.version).toBe("2.0.0");

      // --- the `/` palette offers one row, with the reason ------------------
      const composer = userPage.getByTestId("composer-input");
      await composer.click();
      await userPage.keyboard.type(`/${name}`);
      const options = userPage.getByTestId("skill-palette-item");
      await expect(options).toHaveCount(1);
      await expect(options.first()).toHaveAttribute("data-name", name);
      await expect(options.first()).toHaveAttribute("data-version", "2.0.0");
      await expect(options.first()).toHaveAttribute("data-overrides", "shared");
      await expect(options.first()).toContainText("覆盖同名共享 Skill");
      await userPage.screenshot({ path: path.join(ARTIFACTS, "shadow-palette.png") });
      await userPage.keyboard.press("Escape");

      // --- disabling the private copy falls back to the shared one ----------
      const disabled = await apiWrite(userPage, "PATCH", `/v1/skills/${privateId}`, {
        enabled: false,
      });
      expect(disabled.status).toBe(200);

      const secondSession = await openNewSession(userPage);
      const fallback = await userPage.request.get(
        `/v1/sessions/${secondSession}/files?path=${encodeURIComponent(`.pi/skills/${name}/SKILL.md`)}`,
      );
      expect(fallback.status()).toBe(200);
      expect(((await fallback.json()) as { body: string }).body).toContain("# 共享版本");

      const secondRecords = (await (
        await userPage.request.get(`/v1/sessions/${secondSession}/skills`)
      ).json()) as { skills: { name: string; skill_id: string }[] };
      expect(secondRecords.skills.find((skill) => skill.name === name)?.skill_id).toBe(sharedId);

      // The list says why the skill did not disappear: the shared row is now
      // effective and the disabled private row points at it.
      await userPage.goto("/settings/skills");
      await expect(privateRow).toHaveAttribute("data-effective", "false");
      await expect(privateRow.getByTestId("skill-fallback")).toContainText("共享版本");
      await expect(privateRow.getByTestId("skill-fallback-explain")).toContainText("已停用");
      await expect(sharedRow).toHaveAttribute("data-effective", "true");
      await expect(sharedRow.getByTestId("skill-effective")).toContainText("生效中");
      await expect(sharedRow.getByTestId("skill-shadowed")).toHaveCount(0);
      await userPage.screenshot({ path: path.join(ARTIFACTS, "shadow-fallback.png") });
    } finally {
      await other.close();
    }
  } finally {
    const cleanup = await apiWrite(page, "DELETE", `/v1/skills/${sharedId}`);
    expect([200, 404]).toContain(cleanup.status);
  }
});
