import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { USER, apiWrite, login } from "./helpers";

/**
 * FR-8.7 (empty remote): a brand-new repository is the ordinary case for a new project,
 * and it used to leave the session with no checkout at all — every action answered
 * 还没有 checkout while messages appeared to work.
 */
test("a project cloned from an empty remote still gets a checkout", async ({ page }) => {
  const root = mkdtempSync(path.join(tmpdir(), "pf-empty-"));
  const bare = path.join(root, "origin.git");
  execFileSync("git", ["init", "--bare", "-b", "main", bare], { cwd: root, encoding: "utf8" });

  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `empty-${Date.now()}`,
    repo_url: bare,
    default_branch: "main",
  });
  expect(project.status).toBeLessThan(300);
  const projectId = (project.body as { project: { id: string } }).project.id;

  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "empty",
  });
  const sessionId = (created.body as { session: { id: string } }).session.id;
  await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `e-${Date.now()}`,
    text: "hello",
  });

  // The checkout exists and is on the default branch, unborn or not.
  await expect
    .poll(
      async () => {
        const status = await (await page.request.get(`/v1/sessions/${sessionId}/git/status`)).json();
        return (status as { branch?: string }).branch ?? "";
      },
      { timeout: 45_000 },
    )
    .toBe("main");

  // And the first commit can leave the machine.
  const wrote = await apiWrite(page, "PUT", `/v1/sessions/${sessionId}/files?path=README.md`, "hi\n");
  expect(wrote.status).toBeLessThan(300);
  const commit = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "first commit",
    paths: ["README.md"],
  });
  expect(commit.status).toBe(200);
  const pushed = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/git/push`, {});
  expect(pushed.status).toBe(200);

  writeFileSync(path.join(root, "marker"), "done");
  const log = execFileSync("git", ["--git-dir", bare, "log", "--format=%s", "main"], {
    cwd: root,
    encoding: "utf8",
  });
  expect(log).toContain("first commit");
});