import { expect, test } from "@playwright/test";

import { ADMIN, apiWrite, login, openNewSession } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * FR-14.1 / FR-16.3: two skill scopes, both creatable.
 *
 * Shared skills already had a delete half ("共享 Skill 由管理员维护") and no way
 * to create one: `handleCreateSkill` always wrote `scope=user`, even for an
 * admin. This spec drives the whole promise through the UI:
 *
 *   - an admin sees the "share with everyone" switch and the skill is stored as
 *     a shared one;
 *   - a normal user, on their own account, sees it in the skill list and can
 *     invoke it from `/` — visibility is not just a badge;
 *   - the normal user cannot edit it: the switch is disabled and the API refuses
 *     the toggle, the delete, a bundle replacement, and a shared create.
 *
 * The skill is deployment-wide, so it is deleted in a `finally`: a leftover would
 * make the next run's pre-flight see four shared skills where it expects three.
 */
test("an admin creates a shared skill every user can see and nobody else can edit", async ({
  page,
  browser,
}) => {
  await login(page, ADMIN);
  await page.goto("/settings/skills");

  const name = `shared-${Date.now()}`;
  await page.getByTestId("skill-add").click();
  // The affordance exists only for an admin; that is the half that was missing.
  const sharedSwitch = page.getByTestId("skill-shared");
  await expect(sharedSwitch).toBeVisible();
  await page.getByTestId("skill-name").fill(name);
  await page.getByTestId("skill-description").fill("共享给所有人的技能");
  await page.getByTestId("skill-body").fill("# 共享技能\n\n所有用户都能用。\n");
  await sharedSwitch.click();
  await page.getByTestId("skill-submit").click();

  const row = page.locator(`[data-testid="skill-item"][data-name="${name}"]`);
  await expect(row).toBeVisible({ timeout: 20_000 });
  await expect(row).toContainText("共享");
  await expect(row).toContainText("v0.0.0");

  const stored = (await (await page.request.get("/v1/skills")).json()) as {
    skills: { id: string; name: string; scope: string; enabled: boolean }[];
  };
  const created = stored.skills.find((skill) => skill.name === name);
  expect(created?.scope).toBe("admin");
  expect(created?.enabled).toBe(true);
  const id = created!.id;

  try {
    const other = await browser.newContext();
    const otherPage = await other.newPage();
    try {
      await freshAccount(otherPage, "shared-skill");
      await otherPage.goto("/settings/skills");

      const seen = otherPage.locator(`[data-testid="skill-item"][data-name="${name}"]`);
      await expect(seen).toBeVisible({ timeout: 20_000 });
      await expect(seen).toContainText("共享");
      await expect(seen).toContainText("共享给所有人的技能");

      // Read-only for a normal user: the toggle and delete are disabled, and
      // the reason is on the control rather than implied.
      await expect(seen.getByRole("switch")).toBeDisabled();
      await expect(seen.getByTestId("skill-delete")).toBeDisabled();

      // `/skill:` is the second visibility surface: the skill is in the agent's
      // palette, not only in a settings list.
      await openNewSession(otherPage);
      const composer = otherPage.getByTestId("composer-input");
      await composer.click();
      await otherPage.keyboard.type("/");
      await expect(otherPage.getByTestId("skill-palette")).toBeVisible();
      await otherPage.keyboard.type(name);
      await expect(
        otherPage.getByTestId("skill-palette-item").filter({ hasText: name }),
      ).toBeVisible({ timeout: 20_000 });
      await otherPage.keyboard.press("Escape");

      // The API is the gate, not only the disabled controls.
      const patch = await apiWrite(otherPage, "PATCH", `/v1/skills/${id}`, { enabled: false });
      expect(patch.status).toBe(403);
      const put = await apiWrite(otherPage, "PUT", `/v1/skills/${id}/bundle`, {
        files: [{ path: "SKILL.md", content: "# hijacked\n" }],
      });
      expect(put.status).toBe(403);
      const del = await apiWrite(otherPage, "DELETE", `/v1/skills/${id}`);
      expect(del.status).toBe(403);
      const create = await apiWrite(otherPage, "POST", "/v1/skills", {
        scope: "admin",
        name: `nope-${Date.now()}`,
        description: "x",
        body: "# x\n",
      });
      expect(create.status).toBe(403);
      expect((create.body as { error: { message: string } }).error.message).toContain("管理员");
    } finally {
      await other.close();
    }
  } finally {
    // Deployment-wide state: the next run's pre-flight expects exactly the
    // three seeded shared skills.
    const cleanup = await apiWrite(page, "DELETE", `/v1/skills/${id}`);
    expect([200, 404]).toContain(cleanup.status);
  }
});
