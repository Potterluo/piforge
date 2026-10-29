import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * FR-8.7: a commit made in the workbench can leave the machine. The "remote" is a local
 * bare repository, so this is real git over real refs with no network — the same code
 * path an HTTPS remote takes.
 *
 * The project is created with `repo_url`, which is also the honest way a user gets a
 * remote: the checkout is a clone of it from the start.
 */
test("a session pushes its commit to the project's remote", async ({ page }) => {
  const root = mkdtempSync(path.join(tmpdir(), "pf-remote-"));
  const bare = path.join(root, "origin.git");
  const seed = path.join(root, "seed");
  const git = (cwd: string, ...args: string[]) =>
    execFileSync("git", args, { cwd, encoding: "utf8" });

  git(root, "init", "--bare", "-b", "main", bare);
  execFileSync("git", ["clone", bare, seed], { cwd: root, encoding: "utf8" });
  writeFileSync(path.join(seed, "README.md"), "seed\n");
  for (const args of [
    ["add", "-A"],
    ["-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-m", "seed"],
    ["push", "origin", "main"],
  ]) {
    git(seed, ...args);
  }

  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `remote-${Date.now()}`,
    repo_url: bare,
    default_branch: "main",
  });
  expect(project.status).toBeLessThan(300);
  const projectId = (project.body as { project: { id: string } }).project.id;

  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "push",
  });
  expect(created.status).toBeLessThan(300);
  const sessionId = (created.body as { session: { id: string } }).session.id;

  // The first message provisions the clone.
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `p-${Date.now()}`,
    text: "hello",
  });

  const remote = await (
    await page.request.get(`/v1/sessions/${sessionId}/git/remote`)
  ).json();
  expect((remote as { remote: boolean }).remote).toBe(true);

  // Staging nothing is a state, not a fault: the server says so in a sentence.
  const empty = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "nothing staged",
    paths: [],
  });
  expect(empty.status).toBe(409);
  expect(String((empty.body as { error?: { message?: string } }).error?.message ?? "")).toContain(
    "没有可提交的改动",
  );

  // Write a file through the panel's own endpoint, stage it, commit, push.
  const wrote = await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=notes.txt`, "hello\n");
  expect(wrote.status).toBeLessThan(300);
  const commit = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "from the workbench",
    paths: ["notes.txt"],
  });
  expect(commit.status).toBe(200);

  const pushed = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/push`, {});
  expect(pushed.status).toBe(200);
  const branch = (pushed.body as { branch: string }).branch;

  const log = execFileSync("git", ["--git-dir", bare, "log", "--format=%s", branch], {
    cwd: root,
    encoding: "utf8",
  });
  expect(log).toContain("from the workbench");
});