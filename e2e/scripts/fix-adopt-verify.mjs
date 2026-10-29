// Deployed-app verification for the adopt/compare fixes.
//
// Runs against the real container (http://127.0.0.1:7722) with the real `volc`
// provider: one short turn on the trunk, one on a branch that commits a file, one
// on a branch that only leaves a dirty file. Then it adopts the committed branch
// through the UI and proves the commit is reachable from the trunk's HEAD.
//
//   node scripts/fix-adopt-verify.mjs
//
// Screenshots and a JSON evidence file land in .artifacts/fix-adopt/ (gitignored).
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const BASE = process.env.PIFORGE_URL ?? "http://127.0.0.1:7722";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.resolve(here, "../.artifacts/fix-adopt");
mkdirSync(outDir, { recursive: true });

const evidence = { base: BASE, started_at: new Date().toISOString() };
const log = (...args) => console.log("[verify]", ...args);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const browser = await chromium.launch({ channel: "chrome", headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();

const csrf = async () => {
  const cookies = await context.cookies();
  return cookies.find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
};

const api = async (method, endpoint, options = {}) => {
  const headers = { ...(options.headers ?? {}) };
  if (method !== "GET") headers["X-PF-CSRF"] = await csrf();
  const response = await context.request.fetch(BASE + endpoint, {
    method,
    headers,
    data: options.data,
  });
  const text = await response.text();
  let body = text;
  try {
    body = JSON.parse(text);
  } catch {
    // plain text body
  }
  if (response.status() >= 400 && options.allowError !== true) {
    throw new Error(`${method} ${endpoint} -> ${response.status()}: ${text.slice(0, 400)}`);
  }
  return { status: response.status(), body };
};

const putFile = (session, file, content) =>
  api("PUT", `/v1/sessions/${session}/files?path=${encodeURIComponent(file)}`, {
    headers: { "Content-Type": "text/plain; charset=utf-8" },
    data: content,
  });

const sessionStatus = async (session) => (await api("GET", `/v1/sessions/${session}/status`)).body;
const sessionStats = async (session) => (await api("GET", `/v1/sessions/${session}/stats`)).body;
const gitLog = async (session, limit = 100) =>
  (await api("GET", `/v1/sessions/${session}/git/log?limit=${limit}`)).body;

async function waitFor(label, fn, timeoutMs = 240_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError = null;
  while (Date.now() < deadline) {
    try {
      const value = await fn();
      if (value) return value;
    } catch (error) {
      lastError = error;
    }
    await sleep(1_500);
  }
  throw new Error(`timed out waiting for ${label}${lastError ? `: ${lastError.message}` : ""}`);
}

async function expectText(locator, text, timeoutMs = 30_000) {
  await locator.waitFor({ timeout: timeoutMs });
  await waitFor(`"${text}" in the preview`, async () => {
    const body = await locator.textContent();
    return body?.includes(text) ? body : null;
  }, timeoutMs);
}

// A run is done when the agent is not running and the turn has been recorded;
// poll twice so a start that lands between two polls is not mistaken for a finish.
async function sendTurn(session, text) {
  const before = (await sessionStats(session)).user_messages;
  const sent = await api("POST", `/v1/sessions/${session}/messages`, {
    data: { client_msg_id: `fix-adopt-${crypto.randomUUID()}`, text },
  });
  if (sent.status !== 200) throw new Error(`prompt rejected: ${JSON.stringify(sent.body)}`);
  let settled = 0;
  await waitFor("the run to settle", async () => {
    const status = await sessionStatus(session);
    const stats = await sessionStats(session);
    if (!status.running && stats.user_messages > before) {
      settled += 1;
    } else {
      settled = 0;
    }
    return settled >= 2;
  });
  log(`turn settled for ${session}`);
}

function reachable(head, commits) {
  const byHash = new Map(commits.map((commit) => [commit.hash, commit]));
  const seen = new Set();
  const pending = [head];
  while (pending.length > 0) {
    const hash = pending.pop();
    if (!hash || seen.has(hash)) continue;
    seen.add(hash);
    for (const parent of byHash.get(hash)?.parents ?? []) pending.push(parent);
  }
  return seen;
}

try {
  // ---- login through the real UI -------------------------------------------
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill(USER.password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
  log("logged in");

  // ---- a fresh project + trunk session -------------------------------------
  const projectName = `fix-adopt-${Date.now()}`;
  const project = (
    await api("POST", "/v1/projects", {
      data: { name: projectName, repo_url: "", default_branch: "main" },
    })
  ).body.project;
  const main = (
    await api("POST", `/v1/projects/${project.id}/sessions`, {
      data: { title: "trunk attempt" },
    })
  ).body.session;
  evidence.project = { id: project.id, name: projectName };
  evidence.main = main.id;
  log(`project ${project.id}, trunk ${main.id}`);

  // One short real turn, so clones inherit a transcript and usage.
  await sendTurn(main.id, "用一句话简短回答：你好。");
  const mainBaseHead = (await gitLog(main.id, 5)).head;
  log(`trunk head before branches: ${mainBaseHead}`);

  // ---- branch A: the agent creates a file and commits it -------------------
  const branchA = (
    await api("POST", `/v1/sessions/${main.id}/clone`, { data: {} })
  ).body.session;
  evidence.branch_committed = branchA.id;
  log(`committed branch ${branchA.id} (${branchA.branch ?? ""})`);

  await sendTurn(
    branchA.id,
    "只做这一件事：在仓库根目录创建文件 branch-commit.txt，内容两行：adopted 和 by agent。然后运行 git add branch-commit.txt && git commit -m 'branch: agent commit'。完成后用一句话回复，不要做别的。",
  );

  let branchHead = null;
  try {
    branchHead = await waitFor(
      "the branch agent to commit",
      async () => {
        const graph = await gitLog(branchA.id, 10);
        return graph.head && graph.head !== mainBaseHead ? graph.head : null;
      },
      120_000,
    );
  } catch (error) {
    log(`the agent did not commit on its own: ${error.message}; committing through the product endpoint`);
    await putFile(branchA.id, "branch-commit.txt", "adopted\nby agent\n");
    const committed = await api("POST", `/v1/sessions/${branchA.id}/git/commit`, {
      data: { message: "branch: committed through the product", paths: ["branch-commit.txt"] },
    });
    branchHead = committed.body.commit;
    evidence.branch_commit_fallback = true;
  }
  evidence.branch_head = branchHead;
  log(`branch commit: ${branchHead}`);

  // ---- branch B: a real turn that leaves an uncommitted file ---------------
  // Distinct clone label -> distinct branch/worktree; two clones of the same
  // session would otherwise share one checkout.
  await api("PATCH", `/v1/sessions/${main.id}`, { data: { title: "trunk attempt two" } });
  const branchB = (
    await api("POST", `/v1/sessions/${main.id}/clone`, { data: {} })
  ).body.session;
  evidence.branch_dirty = branchB.id;
  log(`dirty branch ${branchB.id}`);

  await sendTurn(
    branchB.id,
    "只做这一件事：在仓库根目录创建文件 branch-dirty.txt，内容一行 dirty。不要执行 git add 或 git commit。完成后用一句话回复。",
  );
  try {
    await waitFor(
      "the dirty file",
      async () => (await api("GET", `/v1/sessions/${branchB.id}/files?path=branch-dirty.txt`, { allowError: true })).status === 200,
      60_000,
    );
  } catch (error) {
    log(`the agent did not create the dirty file: ${error.message}; writing it through the product`);
    await putFile(branchB.id, "branch-dirty.txt", "dirty\n");
    evidence.dirty_file_fallback = true;
  }

  // ---- compare -------------------------------------------------------------
  const compare = (
    await api("GET", `/v1/sessions/compare?ids=${main.id},${branchA.id},${branchB.id}`)
  ).body.rows;
  evidence.compare = compare;
  console.log("[verify] compare rows:", JSON.stringify(compare, null, 2));

  const rowA = compare.find((row) => row.session_id === branchA.id);
  const rowB = compare.find((row) => row.session_id === branchB.id);
  if (!rowA || rowA.committed_files < 1 || rowA.files_changed < 1 || rowA.additions < 1) {
    throw new Error(`committed branch compare row is zero: ${JSON.stringify(rowA)}`);
  }
  if (!rowB || rowB.uncommitted_files < 1 || rowB.additions < 1) {
    throw new Error(`dirty branch compare row is zero: ${JSON.stringify(rowB)}`);
  }
  if (rowA.inherited_user_messages < 1 || rowA.inherited_tokens_in < 1) {
    throw new Error(`clone did not inherit usage: ${JSON.stringify(rowA)}`);
  }

  await page.goto(`${BASE}/p/${project.id}/compare?ids=${main.id},${branchA.id},${branchB.id}`);
  await page.getByTestId("compare-grid").waitFor({ timeout: 30_000 });
  await page.locator('[data-testid="compare-column"]').first().waitFor();
  await sleep(1_000);
  await page.screenshot({ path: path.join(outDir, "compare.png"), fullPage: true });
  log("screenshot compare.png");

  // ---- adopt the committed branch through the UI ---------------------------
  await page.goto(`${BASE}/p/${project.id}/s/${branchA.id}`);
  await page.locator(`[data-session-id="${branchA.id}"]`).waitFor({ timeout: 40_000 });
  await page.locator(`[data-session-actions="${branchA.id}"]`).click();
  await page.getByTestId("session-adopt").click();
  const toast = page.getByText("已采纳回主干");
  await toast.waitFor({ timeout: 120_000 });
  // Screenshot before doing anything else: Sonner's toast auto-dismisses.
  await page.screenshot({ path: path.join(outDir, "adopt-toast.png"), fullPage: false });
  const toastText = await page
    .locator('[data-sonner-toast], [role="status"], li')
    .filter({ hasText: "已采纳回主干" })
    .first()
    .textContent()
    .catch(() => null);
  evidence.adopt_toast = toastText;
  log(`adopt toast: ${toastText}`);

  // ---- prove the file and the history are on the trunk ---------------------
  const landed = await api("GET", `/v1/sessions/${main.id}/files?path=branch-commit.txt`, {
    allowError: true,
  });
  if (landed.status !== 200) throw new Error(`file did not land on the trunk: ${landed.status}`);
  evidence.main_file = landed.body.body;

  const trunkLog = await gitLog(main.id, 200);
  const ancestors = reachable(trunkLog.head, trunkLog.commits);
  if (!ancestors.has(branchHead)) {
    throw new Error(`branch commit ${branchHead} is not reachable from trunk head ${trunkLog.head}`);
  }
  const mergeCommit = trunkLog.commits.find((commit) => commit.hash === trunkLog.head);
  evidence.trunk = {
    head: trunkLog.head,
    branch_commit_reachable: true,
    head_commit: mergeCommit,
    parents: mergeCommit?.parents ?? [],
  };
  log(`trunk head ${trunkLog.head} contains ${branchHead}`);
  console.log("[verify] trunk log:", JSON.stringify(
    trunkLog.commits.slice(0, 8).map((commit) => ({
      hash: commit.short,
      parents: commit.parents.map((parent) => parent.slice(0, 7)),
      subject: commit.subject,
    })),
    null,
    2,
  ));

  // The trunk page, with the adopted file in its tree and the history that proves
  // the branch's commit is on the trunk.
  await page.goto(`${BASE}/p/${project.id}/s/${main.id}`);
  await page.locator(`[data-session-id="${main.id}"]`).waitFor({ timeout: 40_000 });
  const fileNode = page.getByTestId("file-node").filter({ hasText: "branch-commit.txt" }).first();
  await fileNode.waitFor({ timeout: 40_000 });
  await fileNode.click();
  await expectText(page.getByTestId("file-preview-body"), "adopted", 30_000);
  await page.screenshot({ path: path.join(outDir, "trunk-file.png"), fullPage: false });

  await page.getByTestId("panel-tab-git").click();
  await page.getByTestId("git-history").waitFor({ timeout: 30_000 });
  await sleep(1_000);
  await page.screenshot({ path: path.join(outDir, "trunk-git-history.png"), fullPage: false });
  log("screenshot trunk-file.png and trunk-git-history.png");

  evidence.finished_at = new Date().toISOString();
  evidence.ok = true;
  writeFileSync(path.join(outDir, "evidence.json"), JSON.stringify(evidence, null, 2));
  console.log("[verify] PASS");
} catch (error) {
  evidence.ok = false;
  evidence.error = String(error?.stack ?? error);
  writeFileSync(path.join(outDir, "evidence.json"), JSON.stringify(evidence, null, 2));
  await page.screenshot({ path: path.join(outDir, "failure.png"), fullPage: true }).catch(() => {});
  console.error("[verify] FAIL", error);
  process.exitCode = 1;
} finally {
  await browser.close();
}
