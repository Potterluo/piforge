import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { crc32 } from "node:zlib";

import { expect, test, type Page } from "@playwright/test";

import { apiHeaders, openNewSession, sendPrompt } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * FR-14.4: installing a skill is "one click" from a git URL *or a zip*.
 *
 * The git half is covered by 38-skill-import.spec.ts. This spec covers the zip
 * half, and the second half of the same requirement that was also missing: a
 * skill can be disabled, and a disabled skill stops being mounted. Both are
 * asserted through what a user gets — the settings list and the skills a new
 * session actually has on disk — not through the store.
 */

/** A minimal ZIP writer (stored entries: no compression, no data descriptors). */
function zip(files: { path: string; body: string }[]): Buffer {
  const locals: Buffer[] = [];
  const centrals: Buffer[] = [];
  let offset = 0;

  for (const file of files) {
    const name = Buffer.from(file.path, "utf8");
    const data = Buffer.from(file.body, "utf8");
    const sum = crc32(data);

    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4); // version needed
    local.writeUInt16LE(0x0800, 6); // UTF-8 names
    local.writeUInt16LE(0, 8); // stored
    local.writeUInt16LE(0, 10); // time
    local.writeUInt16LE(0x21, 12); // 1980-01-01
    local.writeUInt32LE(sum, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    locals.push(local, name, data);

    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt16LE(0, 12);
    central.writeUInt16LE(0x21, 14);
    central.writeUInt32LE(sum, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt16LE(0, 30); // extra
    central.writeUInt16LE(0, 32); // comment
    central.writeUInt16LE(0, 34); // disk
    central.writeUInt16LE(0, 36); // internal attrs
    central.writeUInt32LE(0, 38); // external attrs
    central.writeUInt32LE(offset, 42);
    centrals.push(central, name);

    offset += local.length + name.length + data.length;
  }

  const directory = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  end.writeUInt16LE(0, 20);

  return Buffer.concat([...locals, directory, end]);
}

/** A collection laid out like a repository of skills, with a harmless README. */
function skillZip(directory: string): string {
  const file = path.join(directory, "skills.zip");
  writeFileSync(
    file,
    zip([
      { path: "collection/pdf-tools/SKILL.md", body: "---\nname: PDF\n---\nRead and split PDFs.\n" },
      { path: "collection/pdf-tools/scripts/split.py", body: "print('split')\n" },
      { path: "collection/review/SKILL.md", body: "Review diffs carefully.\n" },
      { path: "README.md", body: "a collection of skills\n" },
    ]),
  );
  return file;
}

/** Uploads a file through the real multipart endpoint, bypassing the browser. */
async function uploadZip(
  page: Page,
  file: string,
): Promise<{ status: number; body: unknown; text: string }> {
  const headers = await apiHeaders(page);
  const res = await page.request.post("/v1/skills/import/zip", {
    headers,
    multipart: {
      file: { name: path.basename(file), mimeType: "application/zip", buffer: readFileSync(file) },
    },
  });
  return { status: res.status(), body: await res.json().catch(() => null), text: await res.text() };
}


test("a zip of skills imports through the settings page, and a disabled skill stops mounting", async ({
  page,
}) => {
  const directory = mkdtempSync(path.join(tmpdir(), "pf-skills-zip-"));
  const file = skillZip(directory);

  await freshAccount(page, "skill-zip");
  await page.goto("/settings/skills");

  await page.getByTestId("skill-import").click();
  await page.getByTestId("skill-import-zip").setInputFiles(file);
  await page.getByTestId("skill-import-zip-submit").click();

  // The list, not the toast alone: a toast that says "imported" over an empty
  // list is exactly the kind of lie this project has shipped before.
  const pdf = page.locator('[data-testid="skill-item"][data-name="pdf-tools"]');
  const review = page.locator('[data-testid="skill-item"][data-name="review"]');
  await expect(pdf).toBeVisible({ timeout: 20_000 });
  await expect(review).toBeVisible();
  await expect(pdf).toContainText("Read and split PDFs");

  // Only the skill's own directory travels: the README beside it does not.
  const list = (await (await page.request.get("/v1/skills")).json()) as {
    skills: { id: string; name: string; enabled: boolean }[];
  };
  const imported = list.skills.find((skill) => skill.name === "pdf-tools");
  expect(imported).toBeTruthy();
  const bundle = (await (
    await page.request.get(`/v1/skills/${imported!.id}/bundle`)
  ).json()) as { files: { path: string }[] };
  expect(bundle.files.map((entry) => entry.path).sort()).toEqual([
    "SKILL.md",
    "scripts/split.py",
  ]);

  // A session created now really has both skills on disk.
  const before = await openNewSession(page);
  await sendPrompt(page, "hello");
  const toolingBefore = (await (
    await page.request.get(`/v1/sessions/${before}/tooling`)
  ).json()) as { mounted: { skills: string[] } };
  expect(toolingBefore.mounted.skills).toEqual(
    expect.arrayContaining(["pdf-tools", "review"]),
  );

  // Back to the list: creating the session navigated away from settings.
  // Disable one from the list (the second half of "一键安装 + 可禁用").
  // It is disabled on this spec's own account and never re-enabled: a leftover
  // here used to break 38-skill-import on the next run, because both import a
  // skill called pdf-tools and an import replaces content without re-enabling.
  // Per-account isolation means there is nothing to restore.
  await page.goto("/settings/skills");
  await expect(pdf).toBeVisible();
  const toggle = pdf.getByRole("switch", { name: "启用 pdf-tools" });
  await toggle.click();
  await expect(pdf.getByText("已停用")).toBeVisible();
  const afterDisable = (await (await page.request.get("/v1/skills")).json()) as {
    skills: { name: string; enabled: boolean }[];
  };
  expect(afterDisable.skills.find((skill) => skill.name === "pdf-tools")?.enabled).toBe(false);

  // A new session does not get it; the enabled one still arrives.
  const after = await openNewSession(page);
  await sendPrompt(page, "hello");
  const toolingAfter = (await (
    await page.request.get(`/v1/sessions/${after}/tooling`)
  ).json()) as { mounted: { skills: string[] } };
  expect(toolingAfter.mounted.skills).toContain("review");
  expect(toolingAfter.mounted.skills).not.toContain("pdf-tools");
});

test("a zip that tries to escape its directory is refused", async ({ page }) => {
  const directory = mkdtempSync(path.join(tmpdir(), "pf-skills-evil-"));
  const file = path.join(directory, "evil.zip");
  writeFileSync(
    file,
    zip([
      { path: "../escape/SKILL.md", body: "escape\n" },
      { path: "fine/SKILL.md", body: "fine\n" },
    ]),
  );

  await freshAccount(page, "skill-zip");
  const result = await uploadZip(page, file);
  expect(result.status).toBe(400);
  expect(result.text).toContain("not allowed");

  // Refused as a whole: no half-imported skill from the same archive.
  const list = (await (await page.request.get("/v1/skills")).json()) as {
    skills: { name: string }[];
  };
  expect(list.skills.map((skill) => skill.name)).not.toContain("escape");
  expect(list.skills.map((skill) => skill.name)).not.toContain("fine");
});

test("the zip import refuses an archive with no SKILL.md", async ({ page }) => {
  const directory = mkdtempSync(path.join(tmpdir(), "pf-skills-empty-"));
  const file = path.join(directory, "empty.zip");
  writeFileSync(file, zip([{ path: "docs/README.md", body: "nothing here\n" }]));

  await freshAccount(page, "skill-zip");
  const result = await uploadZip(page, file);
  expect(result.status).toBe(404);
  expect(result.text).toContain("SKILL.md");
});
