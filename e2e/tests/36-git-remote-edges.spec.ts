import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * The three things QA pass 11 found in the git remote, all of which made the feature
 * look broken while the happy path worked:
 *   - committing with only untracked files answered 500 instead of "nothing to commit";
 *   - ahead/behind went stale after a commit, so push stayed disabled;
 *   - a pull that brought a commit down reported nothing.
 */
test("git remote answers honestly in the awkward cases", async ({ page }) => {
  const root = mkdtempSync(path.join(tmpdir(), "pf-git-"));
  const bare = path.join(root, "origin.git");
  const seed = path.join(root, "seed");
  const git = (cwd: string, ...args: string[]) =>
    execFileSync("git", args, { cwd, encoding: "utf8" });

  git(root, "init", "--bare", "-b", "main", bare);
  execFileSync("git", ["clone", bare, seed], { cwd: root, encoding: "utf8" });
  for (const args of [
    ["-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "seed"],
    ["push", "origin", "main"],
  ]) {
    git(seed, ...args);
  }

  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `git-${Date.now()}`,
    repo_url: bare,
    default_branch: "main",
  });
  const projectId = (project.body as { project: { id: string } }).project.id;
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "git",
  });
  const sessionId = (created.body as { session: { id: string } }).session.id;
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `g-${Date.now()}`,
    text: "hello",
  });

  // The agent leaves a file behind; committing with nothing staged must say so in a
  // sentence, not a 500.
  await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=draft.txt`, "wip\n");
  const empty = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "nothing staged",
    paths: [],
  });
  expect(empty.status).toBe(409);
  expect(String((empty.body as { error?: { message?: string } }).error?.message)).toContain(
    "没有可提交的改动",
  );

  // Commit for real, and the remote counts must follow without a reload.
  const commit = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "draft",
    paths: ["draft.txt"],
  });
  expect(commit.status, JSON.stringify(commit.body)).toBe(200);
  const after = await (await page.request.get(`/v1/sessions/${sessionId}/git/remote`)).json();
  expect((after as { ahead: number }).ahead).toBe(1);

  // Push it, so the branch is level with the remote and a later pull can be a
  // fast-forward rather than a divergence.
  const pushed = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/push`, {});
  expect(pushed.status).toBe(200);

  // Somebody else pushes; the pull brings it and says it changed.
  git(seed, "pull", "--ff-only", "origin", "main");
  writeFileSync(path.join(seed, "UPSTREAM.md"), "upstream\n");
  git(seed, "add", "-A");
  const commitUp = execFileSync(
    "git",
    ["-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-m", "upstream"],
    { cwd: seed, encoding: "utf8" },
  );
  expect(commitUp).toBeTruthy();
  git(seed, "push", "origin", "main");

  // Fetching is what makes `behind` non-zero: the count is against the tracking ref.
  const behind = await (await page.request.get(`/v1/sessions/${sessionId}/git/remote`)).json();
  expect((behind as { behind: number }).behind).toBe(1);

  const pulled = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/pull`, {});
  expect(pulled.status).toBe(200);
  expect((pulled.body as { changed: boolean }).changed).toBe(true);
});