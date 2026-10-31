import { expect, test } from "@playwright/test";

import { apiWrite, openNewSession } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * FR-14.2: a session's skills are frozen, not a live pointer at the uploaded
 * bundle, and the tree is read-only where the platform allows it.
 *
 * The spec runs on whichever platform the suite runs on, so it asserts the
 * mechanism the server reports instead of assuming `bind-ro`: Windows has no
 * bind mount and degrades to a per-skill read-only copy, named as such.
 */

const MECHANISMS = ["bind-ro", "copy-ro"];

test("a mounted skill is read-only, frozen, and the mechanism is named", async ({ page }) => {
  await freshAccount(page, "skill-mount");

  const name = `mount-check-${Date.now()}`;
  const v1 = `---\nname: ${name}\ndescription: mount check\n---\n\n# Mount check\nAnswer with MOUNT-ONE.\n`;
  const v2 = `---\nname: ${name}\ndescription: mount check\n---\n\n# Mount check\nAnswer with MOUNT-TWO.\n`;

  const created = await apiWrite(page, "POST", "/v1/skills", {
    name,
    version: "1.0.0",
    description: "mount check",
    body: v1,
  });
  expect(created.status).toBeLessThan(300);
  const skillId = (created.body as { skill: { id: string } }).skill.id;

  const session = await openNewSession(page);
  const mounted = `.pi/skills/${name}/SKILL.md`;

  // Wait until the projection ran and recorded the mount.
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`/v1/sessions/${session}/skills`);
        if (res.status() !== 200) return 0;
        return ((await res.json()) as { skills: unknown[] }).skills.length;
      },
      { timeout: 60_000 },
    )
    .toBeGreaterThan(0);

  // The tooling endpoint reports what the sandbox actually has, including the
  // mechanism this platform uses.
  const tooling = (await (await page.request.get(`/v1/sessions/${session}/tooling`)).json()) as {
    mounted: { skills: string[]; mechanisms?: Record<string, string> };
  };
  expect(tooling.mounted.skills).toContain(name);
  const mechanism = tooling.mounted.mechanisms?.[name];
  expect(MECHANISMS).toContain(mechanism);

  // The API and the panel agree on the word used for it.
  const skillsResponse = (await (
    await page.request.get(`/v1/sessions/${session}/skills`)
  ).json()) as { mechanisms?: Record<string, string> };
  expect(skillsResponse.mechanisms?.[name]).toBe(mechanism);

  // The mounted file is the uploaded content, not a live view of it.
  const before = (await (
    await page.request.get(`/v1/sessions/${session}/files?path=${encodeURIComponent(mounted)}`)
  ).json()) as { body: string };
  expect(before.body).toContain("MOUNT-ONE");

  // Writing through the mounted path is refused (read-only copy or bind mount),
  // and it must not change what the agent reads.
  const write = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${session}/files?path=${encodeURIComponent(mounted)}`,
    "agent was here\n",
  );
  expect(write.status).toBeGreaterThanOrEqual(400);

  const after = (await (
    await page.request.get(`/v1/sessions/${session}/files?path=${encodeURIComponent(mounted)}`)
  ).json()) as { body: string };
  expect(after.body).toContain("MOUNT-ONE");

  // The panel says which mechanism holds the tree read-only. This is the UI half
  // of "do not call a copy a bind mount".
  await page.getByTestId("session-logs-open").click();
  const item = page.getByTestId("session-skill-item").filter({ hasText: name });
  await expect(item).toBeVisible({ timeout: 20_000 });
  const badge = item.getByTestId("session-skill-mechanism");
  await expect(badge).toBeVisible();
  await expect(badge).toHaveAttribute("data-mechanism", mechanism ?? "");
  await page.screenshot({
    path: `.artifacts/partial-batch3/fr-14.2-mechanism-${mechanism}.png`,
    fullPage: true,
  });

  // Editing the bundle does not reach the running session; a new session gets
  // the edit. (FR-14.5's spec asserts the recorded hash; this asserts the bytes
  // the agent actually reads.)
  const replaced = await apiWrite(page, "PUT", `/v1/skills/${skillId}/bundle`, {
    files: [{ path: "SKILL.md", content: v2 }],
  });
  expect(replaced.status).toBe(200);

  const stillOld = (await (
    await page.request.get(`/v1/sessions/${session}/files?path=${encodeURIComponent(mounted)}`)
  ).json()) as { body: string };
  expect(stillOld.body).toContain("MOUNT-ONE");

  const freshSession = await openNewSession(page);
  await expect
    .poll(
      async () => {
        const res = await page.request.get(
          `/v1/sessions/${freshSession}/files?path=${encodeURIComponent(mounted)}`,
        );
        if (res.status() !== 200) return "";
        return ((await res.json()) as { body: string }).body;
      },
      { timeout: 60_000 },
    )
    .toContain("MOUNT-TWO");

  await apiWrite(page, "DELETE", `/v1/skills/${skillId}`);
});
