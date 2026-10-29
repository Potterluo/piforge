import { expect, test } from "@playwright/test";

import { ADMIN, USER, apiWrite, login } from "./helpers";

/**
 * A shared skill belongs to the deployment. QA found the UI saying 共享 Skill 由管理员维护
 * while there was no admin path to do it either — a refusal with no alternative is worse
 * than a plain "no".
 */
test("only an admin can delete a shared skill", async ({ page }) => {
  await login(page, USER);
  const skills = (await (await page.request.get("/v1/skills")).json()) as {
    skills: { id: string; name: string; scope: string }[];
  };
  const shared = skills.skills.find((skill) => skill.scope === "admin");
  expect(shared).toBeTruthy();

  const denied = await apiWrite(page, "DELETE", `/v1/skills/${shared!.id}`);
  expect(denied.status).toBe(403);
  expect(String((denied.body as { error?: { message?: string } }).error?.message)).toContain(
    "共享 Skill 由管理员维护",
  );

  // The admin can, and the bundle goes with it.
  await login(page, ADMIN);
  const created = await apiWrite(page, "POST", "/v1/skills", {
    name: `shared-${Date.now()}`,
    description: "temporary",
    body: "# temp\n",
  });
  expect(created.status).toBeLessThan(300);
  const id = (created.body as { skill: { id: string } }).skill.id;
  const removed = await apiWrite(page, "DELETE", `/v1/skills/${id}`);
  expect(removed.status).toBe(200);
});