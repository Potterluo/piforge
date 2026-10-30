import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { apiWrite, openNewSession, sendPrompt } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * FR-14.x: a skill collection arrives from a repository, not from retyping SKILL.md.
 * A local `file://` repository stands in for a remote one — same clone, same walk.
 */
test("skills can be imported from a repository", async ({ page }) => {
  const root = mkdtempSync(path.join(tmpdir(), "pf-skills-"));
  const repo = path.join(root, "skills");
  mkdirSync(path.join(repo, "pdf-tools", "scripts"), { recursive: true });
  mkdirSync(path.join(repo, "review"), { recursive: true });
  writeFileSync(
    path.join(repo, "pdf-tools", "SKILL.md"),
    "---\nname: PDF\n---\nRead and split PDFs.\n",
  );
  writeFileSync(path.join(repo, "pdf-tools", "scripts", "split.py"), "print('split')\n");
  writeFileSync(path.join(repo, "review", "SKILL.md"), "Review diffs carefully.\n");
  writeFileSync(path.join(repo, "README.md"), "a repository of skills\n");

  const git = (...args: string[]) =>
    execFileSync("git", args, { cwd: repo, encoding: "utf8" });
  git("init", "-b", "main");
  git("add", "-A");
  git("-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-m", "skills");

  // Its own account: 69-skill-zip imports the same two names and disables one,
  // and an import replaces content without re-enabling. Sharing an account
  // between the two specs is what made 38 fail on the second run of a data dir.
  await freshAccount(page, "skill-import");
  const imported = await apiWrite(page, "POST", "/v1/skills/import", {
    url: `file://${repo.replace(/\\/g, "/")}`,
  });
  expect(imported.status).toBe(200);
  const skills = (imported.body as { skills: { name: string; files: number }[] }).skills;
  const names = skills.map((skill) => skill.name).sort();
  expect(names).toEqual(["pdf-tools", "review"]);

  // The skill reaches a session's checkout, with the file that sits beside SKILL.md.
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");
  const tooling = await (await page.request.get(`/v1/sessions/${sessionId}/tooling`)).json();
  expect((tooling as { mounted: { skills: string[] } }).mounted.skills).toContain("pdf-tools");
});