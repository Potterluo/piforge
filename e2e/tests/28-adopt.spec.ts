import { expect, test } from "@playwright/test";

import { USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * FR-2.6: the second half of "try a few approaches and pick one" — the picking. A
 * branch's work has to reach the session it came from, and a dirty target has to be
 * refused rather than quietly overwritten.
 */
test("a branch's work can be adopted back", async ({ page }) => {
  await login(page, USER);
  const sourceId = await openNewSession(page);
  await sendPrompt(page, "the parent of an adopted branch");

  const cloned = await apiWrite(page, "POST", `/v1/sessions/${sourceId}/clone`, {});
  expect(cloned.status).toBe(201);
  const branchId = (cloned.body as { session: { id: string } }).session.id;

  // Write a file in the branch's own checkout.
  const write = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${branchId}/files?path=adopted.txt`,
    "adopted from a branch\n",
  );
  expect(write.status).toBeLessThan(300);

  const adopted = await apiWrite(page, "POST", `/v1/sessions/${branchId}/adopt`, {});
  expect(adopted.status).toBe(200);
  const result = (adopted.body as { adopted: { commit: string; files: string[]; mode: string } })
    .adopted;
  expect(result.mode).toBe("working-tree");
  expect(result.commit).toMatch(/^[0-9a-f]{7,}$/);
  expect(result.files).toContain("adopted.txt");

  // The parent's checkout now has the file.
  const read = await page.request.get(`/v1/sessions/${sourceId}/files?path=adopted.txt`);
  expect(read.status()).toBe(200);
  const content = (await read.json()) as { body: string };
  expect(content.body).toContain("adopted from a branch");

  // Adopting a main session is a client error, not a crash.
  const refused = await apiWrite(page, "POST", `/v1/sessions/${sourceId}/adopt`, {});
  expect(refused.status).toBe(400);
});

/**
 * FR-2.6, the case that used to be impossible: the branch *committed* its work.
 * Before the fix, adopt only replayed uncommitted diffs, so pressing the product's
 * own commit button made the branch un-adoptable (409 这个分支没有可采纳的改动).
 *
 * This has to prove reachability, not just that a file appeared: the branch's commit
 * must be an ancestor of the target's HEAD after the adopt.
 */
test("a branch that committed its work can still be adopted", async ({ page }) => {
  await login(page, USER);
  const main = await openNewSession(page);
  const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";
  await sendPrompt(page, "the parent of a committed branch");

  const cloned = await apiWrite(page, "POST", `/v1/sessions/${main}/clone`, {});
  expect(cloned.status).toBe(201);
  const branch = (cloned.body as { session: { id: string } }).session.id;

  // A real short turn on the branch, so the adopt carries a conversation too.
  await page.goto(`/p/${projectId}/s/${branch}`);
  await expect(page.locator(`[data-session-id="${branch}"]`)).toBeVisible({ timeout: 40_000 });
  await sendPrompt(page, "a short turn on the branch");

  // Commit through the product's commit endpoint — the exact action that used to
  // make the branch un-adoptable.
  const write = await apiWrite(
    page,
    "PUT",
    `/v1/sessions/${branch}/files?path=branch-commit.txt`,
    "committed on the branch\n",
  );
  expect(write.status).toBeLessThan(300);
  const commit = await apiWrite(page, "POST", `/v1/sessions/${branch}/git/commit`, {
    message: "branch commits its file",
    paths: ["branch-commit.txt"],
  });
  expect(commit.status).toBe(200);
  const branchCommit = (commit.body as { commit: string }).commit;
  expect(branchCommit).toMatch(/^[0-9a-f]{7,}$/);

  const adopted = await apiWrite(page, "POST", `/v1/sessions/${branch}/adopt`, {});
  expect(adopted.status).toBe(200);
  const result = (
    adopted.body as {
      adopted: {
        commit: string;
        files: string[];
        mode: string;
        commits: string[];
        merge_commit?: string;
      };
    }
  ).adopted;
  expect(result.files).toContain("branch-commit.txt");
  expect(["merge", "fast-forward"]).toContain(result.mode);
  expect(result.commits).toContain(branchCommit);

  // The file is on main...
  const read = await page.request.get(`/v1/sessions/${main}/files?path=branch-commit.txt`);
  expect(read.status()).toBe(200);
  expect(((await read.json()) as { body: string }).body).toContain("committed on the branch");

  // ...and the branch's commit is reachable from main's HEAD, which is the
  // difference between "the bytes were copied" and "the history was adopted".
  const logRes = await page.request.get(`/v1/sessions/${main}/git/log?limit=100`);
  expect(logRes.status()).toBe(200);
  const graph = (await logRes.json()) as {
    head: string;
    commits: { hash: string; parents: string[] }[];
  };
  expect(graph.head).toBe(result.commit);
  expect(reachableFrom(graph.head, graph.commits)).toContain(branchCommit);
});

/** Every commit reachable from head through first- and second-parent links. */
function reachableFrom(
  head: string,
  commits: { hash: string; parents: string[] }[],
): Set<string> {
  const byHash = new Map(commits.map((commit) => [commit.hash, commit]));
  const seen = new Set<string>();
  const pending = [head];
  while (pending.length > 0) {
    const hash = pending.pop();
    if (!hash || seen.has(hash)) continue;
    seen.add(hash);
    for (const parent of byHash.get(hash)?.parents ?? []) pending.push(parent);
  }
  return seen;
}
