import { expect, test, type Page } from "@playwright/test";

import { USER, apiWrite, login, openNewSession } from "./helpers";

/**
 * FR-14.5: a session records the skill version it was materialised with, and
 * that record is a fact — editing the skill afterwards changes what a *new*
 * session gets, never what the old one reports.
 *
 * Two sessions in two projects so the mounts are genuinely independent (two
 * main sessions in one project share the primary checkout, which would make the
 * second mount overwrite the first).
 */
interface RecordedSkill {
  session_id: string;
  skill_id: string;
  name: string;
  version: string;
  content_hash: string;
  description?: string;
  mounted_path: string;
  recorded_at: string;
}

async function recordedSkills(page: Page, sessionId: string): Promise<RecordedSkill[]> {
  const res = await page.request.get(`/v1/sessions/${sessionId}/skills`);
  expect(res.status()).toBe(200);
  const body = (await res.json()) as { skills: RecordedSkill[] };
  return body.skills;
}

async function waitForRecord(page: Page, sessionId: string, name: string): Promise<RecordedSkill> {
  await expect
    .poll(
      async () => (await recordedSkills(page, sessionId)).some((skill) => skill.name === name),
      { timeout: 60_000, message: `session ${sessionId} never recorded ${name}` },
    )
    .toBe(true);
  const found = (await recordedSkills(page, sessionId)).find((skill) => skill.name === name);
  expect(found, `no record for ${name}`).toBeTruthy();
  return found as RecordedSkill;
}

async function mountedBody(page: Page, sessionId: string, path: string): Promise<string> {
  const res = await page.request.get(
    `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}`,
  );
  expect(res.status()).toBe(200);
  return ((await res.json()) as { body: string }).body;
}

test("a session keeps the skill version it mounted, while a new session picks up the edit", async ({
  page,
}) => {
  await login(page, USER);

  const name = `e2e-skill-version-${Date.now()}`;
  const v1 = `---\nname: ${name}\ndescription: version one\n---\n\n# Version one\nAnswer with VERSION-ONE.\n`;
  const v2 = `---\nname: ${name}\ndescription: version two\n---\n\n# Version two\nAnswer with VERSION-TWO.\n`;

  const created = await apiWrite(page, "POST", "/v1/skills", {
    name,
    version: "1.0.0",
    description: "e2e versioning",
    body: v1,
  });
  expect(created.status).toBeLessThan(300);
  const skillId = (created.body as { skill: { id: string } }).skill.id;

  // --- session A, created against version one -------------------------------
  const sessionA = await openNewSession(page);
  const recordA = await waitForRecord(page, sessionA, name);
  expect(recordA.version).toBe("1.0.0");
  // The hash is derived from the mounted files, never invented by the client.
  expect(recordA.content_hash).toMatch(/^[0-9a-f]{64}$/);
  expect(recordA.content_hash).not.toBe("pending");
  expect(recordA.mounted_path).toContain(`.pi/skills/${name}`);
  expect(recordA.session_id).toBe(sessionA);
  expect(recordA.skill_id).toBe(skillId);
  expect(Date.parse(recordA.recorded_at)).toBeGreaterThan(0);

  const mountedA = `.pi/skills/${name}/SKILL.md`;
  expect(await mountedBody(page, sessionA, mountedA)).toContain("VERSION-ONE");

  // The agent is actually given the skill: Pi discovers `.pi/skills/<name>/SKILL.md`.
  const toolingA = (await (
    await page.request.get(`/v1/sessions/${sessionA}/tooling`)
  ).json()) as { mounted: { skills: string[] } };
  expect(toolingA.mounted.skills).toContain(name);

  // --- edit the skill after session A exists --------------------------------
  const replaced = await apiWrite(page, "PUT", `/v1/skills/${skillId}/bundle`, {
    files: [{ path: "SKILL.md", content: v2 }],
  });
  expect(replaced.status).toBe(200);
  const replacedHash = (replaced.body as { skill: { content_hash: string } }).skill.content_hash;
  expect(replacedHash).not.toBe(recordA.content_hash);

  // Session A's report is unchanged, and so is the copy it mounted.
  const afterEdit = await recordedSkills(page, sessionA);
  expect(afterEdit.find((skill) => skill.name === name)?.content_hash).toBe(
    recordA.content_hash,
  );
  expect(await mountedBody(page, sessionA, mountedA)).toContain("VERSION-ONE");

  // --- session B starts after the edit --------------------------------------
  const sessionB = await openNewSession(page);
  const recordB = await waitForRecord(page, sessionB, name);
  expect(recordB.content_hash).not.toBe(recordA.content_hash);
  expect(recordB.content_hash).toMatch(/^[0-9a-f]{64}$/);
  // The uploaded bundle's stored hash and the mounted digest agree for the same
  // bytes: one derivation, not two.
  expect(recordB.content_hash).toBe(replacedHash);
  expect(recordB.mounted_path).not.toBe(recordA.mounted_path);

  expect(await mountedBody(page, sessionB, mountedA)).toContain("VERSION-TWO");

  // The two sessions now disagree in the way the feature is for.
  const finalA = (await recordedSkills(page, sessionA)).find((skill) => skill.name === name);
  const finalB = (await recordedSkills(page, sessionB)).find((skill) => skill.name === name);
  expect(finalA?.content_hash).toBe(recordA.content_hash);
  expect(finalB?.content_hash).toBe(recordB.content_hash);
  expect(finalA?.content_hash).not.toBe(finalB?.content_hash);

  // Clean up so a repeated run does not accumulate skills.
  const removed = await apiWrite(page, "DELETE", `/v1/skills/${skillId}`);
  expect(removed.status).toBe(200);
});
