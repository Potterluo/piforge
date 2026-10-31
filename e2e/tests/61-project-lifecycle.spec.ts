import { expect, test } from "@playwright/test";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, ensureProject, login, openSession } from "./helpers";

/**
 * A project's lifecycle after creation: rename it from its own kebab, and delete
 * it — with a confirmation that says exactly what goes and what does not.
 *
 * Two rules are load-bearing here:
 *  - deleting a project is destructive but the remote repository is not part of
 *    it, so the dialog must say so rather than let the user guess;
 *  - a project with a turn in flight is refused, not silently killed.
 *  - and a delete that reports success must take the sandbox directory with it:
 *    that directory is on this machine, so the test looks at the real path.
 */

const HERE = path.dirname(fileURLToPath(import.meta.url));
// playwright.config.ts starts piforged with `-data <e2e>/.data`; the local
// sandbox provider puts a project's checkout at sandboxes/<user>/<project>.
const DATA_DIR = path.resolve(HERE, "../.data");
const ARTIFACTS = path.resolve(HERE, "../.artifacts/ux-fix3");
mkdirSync(ARTIFACTS, { recursive: true });
const QA16_ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-qa16");
mkdirSync(QA16_ARTIFACTS, { recursive: true });

/**
 * Renaming onto a name another project owns must show the server's exact sentence
 * under the field, the way create does. The regression was not cosmetic: the 409
 * was real and actionable, and the dialog replaced it with 「操作失败，请稍后重试」
 * — a message that tells nobody what to change (QA-16, FIX B).
 */
test("renaming to an existing name shows the server's conflict message", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const taken = `qa16-taken-${stamp}`;
  const mine = `qa16-rename-${stamp}`;
  const takenId = await ensureProject(page, taken);
  const mineId = await ensureProject(page, mine);

  await page.goto(`/p/${mineId}`);
  await expect(page.getByTestId("project-title")).toHaveText(mine);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-rename").click();

  const input = page.getByTestId("project-rename-input");
  await expect(input).toHaveValue(mine);
  await input.fill(taken);
  await page.getByTestId("project-rename-submit").click();

  const error = page.getByTestId("project-rename-error");
  await expect(error).toBeVisible({ timeout: 20_000 });
  await expect(error).toHaveText(
    `项目名称「${taken}」已存在（不区分大小写），请换一个名称`,
  );
  // The dialog stays open on the field the user has to fix, and the project did
  // not change.
  await expect(input).toBeVisible();
  await expect(input).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByTestId("project-title")).toHaveText(mine);

  // The generic failure that used to replace the server's sentence is gone.
  await expect(
    page.locator("[data-sonner-toast]").filter({ hasText: "操作失败，请稍后重试" }),
  ).toHaveCount(0);
  await page.screenshot({ path: path.join(QA16_ARTIFACTS, "B-rename-conflict-inline.png") });

  // Closing without saving leaves the name alone, and the API agrees.
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("project-title")).toHaveText(mine);
  const stored = (await (await page.request.get(`/v1/projects/${mineId}`)).json()) as {
    project: { name: string };
  };
  expect(stored.project.name).toBe(mine);

  for (const id of [mineId, takenId]) {
    const gone = await apiWrite(page, "DELETE", `/v1/projects/${id}`);
    expect(gone.status).toBeLessThan(300);
  }
});

test("a project can be renamed from its kebab", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const before = `ux61-rename-${stamp}`;
  const after = `ux61-renamed-${stamp}`;
  const projectId = await ensureProject(page, before);

  await page.goto(`/p/${projectId}`);
  await expect(page.getByTestId("project-title")).toHaveText(before);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-rename").click();
  await page.getByTestId("project-rename-input").fill(after);
  await page.getByTestId("project-rename-submit").click();

  await expect(page.getByTestId("project-title")).toHaveText(after, { timeout: 20_000 });
  const stored = (await (await page.request.get(`/v1/projects/${projectId}`)).json()) as {
    project: { name: string };
  };
  expect(stored.project.name).toBe(after);

  const gone = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
  expect(gone.status).toBeLessThan(300);
});

test("deleting a project confirms what goes, returns home and removes it", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const name = `ux61-delete-${stamp}`;
  const projectId = await ensureProject(page, name);
  await openSession(page, projectId, "session that should disappear");

  // Creating the session materialised the project's sandbox on this disk. This
  // is the directory the old delete left behind.
  const me = (await (await page.request.get("/v1/auth/me")).json()) as {
    user: { id: string };
  };
  const sandbox = path.join(DATA_DIR, "sandboxes", me.user.id, projectId);
  await expect
    .poll(() => existsSync(path.join(sandbox, "repo")), { timeout: 20_000 })
    .toBe(true);

  await page.goto(`/p/${projectId}`);
  await expect(page.getByTestId("project-title")).toHaveText(name);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-delete").click();

  const dialog = page.getByTestId("project-delete-dialog");
  await expect(dialog).toBeVisible();
  // What will be deleted: the sessions (count) and the sandbox.
  await expect(dialog).toContainText("1 个会话");
  await expect(dialog).toContainText("沙箱");
  // What will not: the remote repository.
  await expect(dialog).toContainText("远程仓库不会被删除");
  await page.screenshot({ path: path.join(ARTIFACTS, "61-delete-confirm.png") });

  await page.getByTestId("project-delete-confirm").click();
  await expect(page).toHaveURL(/\/$/, { timeout: 20_000 });
  await expect(
    page.locator("[data-testid=project-card]", { hasText: name }),
  ).toHaveCount(0);
  expect((await page.request.get(`/v1/projects/${projectId}`)).status()).toBe(404);

  // The row and the directory go together. When the directory cannot be
  // destroyed (a held file, a live agent), the API refuses the delete and keeps
  // the project instead — project_delete_test.go and
  // e2e/scripts/fix-orphans-repro.ps1 prove that half; here the honest-success
  // half is asserted on the real path rather than on a toast.
  await expect.poll(() => existsSync(sandbox), { timeout: 15_000 }).toBe(false);
  await page.screenshot({ path: path.join(ARTIFACTS, "61-deleted-home.png") });
});

/**
 * The defect this guards: on Windows `RemoveAll` fails while any process holds a
 * file inside the checkout, and the old handler logged that and deleted the row
 * anyway — `sandboxes/<user>/<project>` stayed on disk with nothing pointing at
 * it. The suite runs on the same OS as the report, so a real open handle is the
 * trigger.
 *
 * Node's own `openSync` is not enough: libuv opens with FILE_SHARE_DELETE, so
 * RemoveAll still succeeds and the test would pass on the old code too. A small
 * PowerShell holder opens the file with FileShare.None and keeps it until this
 * test kills it — which is what a running agent, a terminal or Chrome looks like
 * to the filesystem.
 */
test("a sandbox that cannot be removed keeps the project instead of orphaning it", async ({
  page,
}) => {
  test.skip(process.platform !== "win32", "the held-handle failure mode is Windows-only");

  await login(page, USER);
  const stamp = Date.now();
  const name = `ux61-held-${stamp}`;
  const projectId = await ensureProject(page, name);
  await openSession(page, projectId, "held session");

  const me = (await (await page.request.get("/v1/auth/me")).json()) as {
    user: { id: string };
  };
  const sandbox = path.join(DATA_DIR, "sandboxes", me.user.id, projectId);
  const repo = path.join(sandbox, "repo");
  await expect.poll(() => existsSync(repo), { timeout: 20_000 }).toBe(true);

  const work = mkdtempSync(path.join(os.tmpdir(), "pf-hold-"));
  const held = path.join(repo, "held-by-the-test.txt");
  const ready = path.join(work, "ready");
  const script = path.join(work, "hold.ps1");
  writeFileSync(
    script,
    [
      "param([string]$Path, [string]$Ready)",
      "$f = [System.IO.File]::Open($Path, 'OpenOrCreate', 'ReadWrite', 'None')",
      "try {",
      "  Set-Content -Path $Ready -Value 'held' -NoNewline",
      "  Start-Sleep -Seconds 120",
      "} finally { $f.Dispose() }",
    ].join("\n"),
  );
  const holder = spawn(
    "powershell",
    ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Path", held, "-Ready", ready],
    { stdio: "ignore" },
  );
  try {
    // The marker is written only after the file is open with no sharing, so the
    // delete below is guaranteed to meet the lock rather than race it.
    await expect.poll(() => existsSync(ready), { timeout: 20_000 }).toBe(true);

    await page.goto(`/p/${projectId}`);
    await page.getByTestId("project-actions").click();
    await page.getByTestId("project-delete").click();
    await page.getByTestId("project-delete-confirm").click();

    const toast = page.locator("[data-sonner-toast]").filter({ hasText: "删除失败" });
    await expect(toast).toBeVisible({ timeout: 30_000 });
    await expect(toast).toContainText("沙箱目录删不掉");
    await expect(toast).toContainText("项目已保留");

    // Nothing half-deleted: the project is still there and so is its directory.
    // That pair is the whole promise — the row and the disk agree.
    expect((await page.request.get(`/v1/projects/${projectId}`)).status()).toBe(200);
    expect(existsSync(sandbox)).toBe(true);
    await expect(page).toHaveURL(new RegExp(`/p/${projectId}`));
    await page.screenshot({ path: path.join(ARTIFACTS, "61-delete-refused-held.png") });
  } finally {
    holder.kill();
  }

  // With the holder gone the same action finishes the job, and the directory
  // goes with the row instead of becoming the orphan this test is named after.
  const gone = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
  expect(gone.status).toBe(200);
  expect((await page.request.get(`/v1/projects/${projectId}`)).status()).toBe(404);
  await expect.poll(() => existsSync(sandbox), { timeout: 15_000 }).toBe(false);
});

test("a project with a running turn refuses to be deleted", async ({ page }) => {
  await login(page, USER);
  const stamp = Date.now();
  const name = `ux61-running-${stamp}`;
  const projectId = await ensureProject(page, name);
  const sessionId = await openSession(page, projectId, "busy session");

  // A genuinely live turn, not a status patched by hand. The delete guard now
  // asks the agent registry, so a "running" row with nothing behind it is
  // repaired and the delete proceeds; that is the right behaviour, and it means
  // this test has to hold a real process to exercise the refusal. The dialog is
  // opened first and the [[hold-run]] prompt is sent last, so the paced fake
  // peer cannot settle before the confirm click.
  await page.goto(`/p/${projectId}`);
  await page.getByTestId("project-actions").click();
  await page.getByTestId("project-delete").click();

  const started = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `cm_busy_${sessionId}`,
    text: "hold this run open [[hold-run]]",
  });
  expect(started.status).toBe(200);
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`/v1/sessions/${sessionId}`);
        if (!res.ok()) return `http ${res.status()}`;
        const body = (await res.json()) as { session: { status: string } };
        return body.session.status;
      },
      { timeout: 20_000 },
    )
    .toBe("running");

  await page.getByTestId("project-delete-confirm").click();

  const toast = page.locator("[data-sonner-toast]").filter({ hasText: "删除失败" });
  await expect(toast).toBeVisible({ timeout: 20_000 });
  await expect(toast).toContainText("正在运行");
  // Still on the project: a refusal must not navigate away or half-delete.
  await expect(page).toHaveURL(new RegExp(`/p/${projectId}`));
  expect((await page.request.get(`/v1/projects/${projectId}`)).status()).toBe(200);
  await page.screenshot({ path: path.join(ARTIFACTS, "61-delete-refused-running.png") });

  // The API is the gate, not only the dialog: a direct DELETE is refused too.
  const refused = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
  expect(refused.status).toBe(409);
  expect((refused.body as { error: { message: string } }).error.message).toContain("正在运行");

  // Stop the real turn, then the same request finishes. Interrupting also
  // proves the refusal was about a live agent, not about the row.
  const stopped = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/interrupt`, {});
  expect(stopped.status).toBeLessThan(300);
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`/v1/sessions/${sessionId}`);
        if (!res.ok()) return "gone";
        const body = (await res.json()) as { session: { status: string } };
        return body.session.status;
      },
      { timeout: 20_000 },
    )
    .not.toBe("running");
  const gone = await apiWrite(page, "DELETE", `/v1/projects/${projectId}`);
  expect(gone.status).toBeLessThan(300);
});
