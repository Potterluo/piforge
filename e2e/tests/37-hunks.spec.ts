import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * FR-12.2: stage one hunk, discard one hunk. The client sends an index, never a patch —
 * the server rebuilds the patch from the diff it produced, so "stage this hunk" cannot
 * become "write this file".
 */
test("one hunk can be staged and another discarded", async ({ page }) => {
  const root = mkdtempSync(path.join(tmpdir(), "pf-hunk-"));
  const seed = path.join(root, "seed");
  const bare = path.join(root, "origin.git");
  execFileSync("git", ["init", "--bare", "-b", "main", bare], { cwd: root, encoding: "utf8" });
  execFileSync("git", ["clone", bare, seed], { cwd: root, encoding: "utf8" });

  const lines = Array.from({ length: 24 }, (_, i) => `line ${i}`);
  writeFileSync(path.join(seed, "f.txt"), lines.join("\n") + "\n");
  execFileSync("git", ["add", "-A"], { cwd: seed });
  execFileSync("git", ["-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-m", "seed"], {
    cwd: seed,
  });
  execFileSync("git", ["push", "origin", "main"], { cwd: seed });

  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `hunk-${Date.now()}`,
    repo_url: bare,
    default_branch: "main",
  });
  const projectId = (project.body as { project: { id: string } }).project.id;
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "hunk" });
  const sessionId = (created.body as { session: { id: string } }).session.id;
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `h-${Date.now()}`,
    text: "hello",
  });

  // Two edits far apart → two hunks.
  const edited = lines.slice();
  edited[1] = "line 1 EDITED";
  edited[20] = "line 20 EDITED";
  const wrote = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${sessionId}/files?path=f.txt`,
    edited.join("\n") + "\n",
  );
  expect(wrote.status).toBeLessThan(300);

  const diff = await (await page.request.get(`/v1/sessions/${sessionId}/git/diff?scope=unstaged`)).json();
  const patch = (diff as { diffs: { patch: string }[] }).diffs[0]?.patch ?? "";
  expect((patch.match(/^@@/gm) ?? []).length).toBe(2);

  const staged = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/hunk`, {
    path: "f.txt",
    index: 0,
    action: "stage",
  });
  expect(staged.status).toBe(200);

  const stagedDiff = await (
    await page.request.get(`/v1/sessions/${sessionId}/git/diff?scope=staged`)
  ).json();
  const stagedPatch = (stagedDiff as { diffs: { patch: string }[] }).diffs[0]?.patch ?? "";
  expect((stagedPatch.match(/EDITED/g) ?? []).length).toBe(1);

  // Discarding the remaining hunk restores only that one.
  const discarded = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/hunk`, {
    path: "f.txt",
    index: 0,
    action: "discard",
  });
  expect(discarded.status).toBe(200);
  const file = await (await page.request.get(`/v1/sessions/${sessionId}/files?path=f.txt`)).json();
  const body = (file as { body: string }).body;
  expect(body).toContain("line 1 EDITED");
  expect(body).not.toContain("line 20 EDITED");
});