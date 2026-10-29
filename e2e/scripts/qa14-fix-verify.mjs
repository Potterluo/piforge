// Verification probe for the three worst QA-14 findings, run against the
// deployed app (default :7722).
//
// It exercises the user's own paths — the composer, the model picker, the stop
// button, a fork — and asserts the fixed behaviour. Before the fix it reports
// each finding as BROKEN instead of throwing, so one run documents all three.
//
//   node e2e/scripts/qa14-fix-verify.mjs
//
// Screenshots and a JSON log land in e2e/.artifacts/fix-qa14/.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-qa14");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const stamp = Date.now();
// ONLY=fork|model|interrupt runs one finding, for a re-check without paying for
// the other two on the deployed provider.
const ONLY = process.env.ONLY ?? '';
const wants = (name) => ONLY === '' || ONLY === name;
const record = { base: BASE, stamp, checks: {}, findings: [] };
const save = () => writeFileSync(path.join(out, "verify-log.json"), JSON.stringify(record, null, 2));

const shot = async (page, name) => {
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false });
  console.log("[shot]", `${name}.png`);
};

const ok = (condition, message) => {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  record.findings.push({ ok: Boolean(condition), message });
  return Boolean(condition);
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";

async function login(page, email = "dev@piforge.local", password = "piforge-dev-123") {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(email);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
}

async function api(page, method, url, data) {
  const res = await page.request.fetch(`${BASE}${url}`, {
    method,
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    data,
  });
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status(), body };
}

// The run-state marker is a hidden span (`hidden` in the DOM), so waiting for it
// to become *visible* never succeeds: poll the attribute instead.
const waitSettled = async (page, timeout = 120_000) => {
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") === "settled",
    undefined,
    { timeout },
  );
};

const transcript = (page) => page.getByTestId("session-stream").innerText();

async function send(page, text, { wait = true } = {}) {
  const before = await page.getByTestId("turn-user").count();
  await page.getByTestId("composer-input").fill(text);
  await page.getByTestId("composer-send").click();
  await page.getByTestId("turn-user").nth(before).waitFor({ timeout: 30_000 });
  if (wait) await waitSettled(page);
}

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await login(page);

// ---------------------------------------------------------------------------
// Project + a short first turn, so there is a fork point.
// ---------------------------------------------------------------------------
const project = await api(page, "POST", "/v1/projects", {
  name: `fixqa14-${stamp}`,
  repo_url: "",
  default_branch: "main",
});
if (project.status >= 300) throw new Error(`create project: ${project.status} ${JSON.stringify(project.body)}`);
const projectId = project.body.project.id;
const created = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
  title: `fixqa14 session ${stamp}`,
});
const parentId = created.body.session.id;

await page.goto(`${BASE}/p/${projectId}/s/${parentId}`);
await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
await send(page, "只回答两个字：可以。不要调用工具。");
// A second user message: forking at the first one truncates to nothing, so the
// branch would have no history and Pi would never write its file — the branch
// process would then create one itself, hiding the inherited-cwd bug.
await send(page, "再回答两个字：好的。不要调用工具。");
await shot(page, "01-parent-settled");
const parentStatus = await api(page, "GET", `/v1/sessions/${parentId}/status`);
record.checks.parent = { status: parentStatus.body };
console.log("[parent]", JSON.stringify(parentStatus.body));

// ---------------------------------------------------------------------------
// FINDING 1 — a forked session must run in its own worktree on its own branch.
// ---------------------------------------------------------------------------
const CWD_PROMPT =
  "只做只读检查：用 bash 依次运行 pwd 和 git rev-parse --abbrev-ref HEAD，把两行输出原样贴给我。不要修改任何文件，不要提交。";

/** Opens a branch/clone, asks it where it is, and returns what it said. */
async function probeWorkspace(sessionId, name) {
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  const branch =
    (await page
      .locator('[data-testid="git-status-branch"]')
      .first()
      .innerText({ timeout: 20_000 })
      .catch(() => "")) || "";
  await send(page, CWD_PROMPT);
  await shot(page, name);
  const text = await transcript(page);
  const status = await api(page, "GET", `/v1/sessions/${sessionId}/status`);
  return { branch, text, sessionFile: status.body?.session_file, status: status.body };
}

let forkId = null;
try {
  const points = await api(page, "GET", `/v1/sessions/${parentId}/fork-points`);
  const entry = points.body?.points?.[1]?.entry_id ?? points.body?.points?.[0]?.entry_id;
  if (!entry) throw new Error("no fork point");
  const label = `qa14 fix fork ${stamp}`;
  const forked = await api(page, "POST", `/v1/sessions/${parentId}/fork`, { entry_id: entry, label });
  if (forked.status >= 300) throw new Error(`fork: ${forked.status} ${JSON.stringify(forked.body)}`);
  forkId = forked.body.session.id;

  const fork = await probeWorkspace(forkId, "02-fork-cwd");
  record.checks.fork = { id: forkId, ...fork, transcript: fork.text.slice(-1200) };
  console.log("[fork]", JSON.stringify(record.checks.fork, null, 2));

  const worktreeShaped = /worktrees[\\/][^\s]+/.test(fork.text);
  const onOwnBranch = /piforge\//.test(fork.text);
  const onMain = /\bmain\b/.test(fork.text.split("输出")[0] ?? fork.text);
  ok(worktreeShaped, "forked agent pwd is inside a worktrees/ directory");
  ok(onOwnBranch, "forked agent HEAD is its own piforge/* branch");
  ok(!onMain, "forked agent is not on main");
} catch (error) {
  record.checks.fork = { error: String(error) };
  ok(false, `fork check could not run: ${error}`);
}

// The clone path is the deterministic one: CloneSession copies the parent's
// JSONL byte for byte, header included, so its stored cwd is the parent's repo.
try {
  const cloned = await api(page, "POST", `/v1/sessions/${parentId}/clone`, {});
  if (cloned.status >= 300) throw new Error(`clone: ${cloned.status} ${JSON.stringify(cloned.body)}`);
  const cloneId = cloned.body.session.id;
  const clone = await probeWorkspace(cloneId, "03-clone-cwd");
  record.checks.clone = { id: cloneId, ...clone, transcript: clone.text.slice(-1200) };
  console.log("[clone]", JSON.stringify(record.checks.clone, null, 2));
  ok(/worktrees[\\/][^\s]+/.test(clone.text), "cloned agent pwd is inside a worktrees/ directory");
  ok(/piforge\//.test(clone.text), "cloned agent HEAD is its own piforge/* branch");
} catch (error) {
  record.checks.clone = { error: String(error) };
  ok(false, `clone check could not run: ${error}`);
}

// ---------------------------------------------------------------------------
// FINDING 2 — the model picker must actually change the next turn's model.
// ---------------------------------------------------------------------------
if (wants("model")) {
  // A fresh session: the picker bug is about a *running* agent keeping the model
  // it spawned with, which needs a session that has already spoken.
  const second = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: `fixqa14 model ${stamp}`,
  });
  const sessionId = second.body.session.id;
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await send(page, "只回答两个字：可以。不要调用工具。");
  const before = await api(page, "GET", `/v1/sessions/${sessionId}/status`);

  // Pick a provider that has no key, exactly as the QA pass did.
  await page.getByTestId("composer-model").click();
  const option = page.getByRole("option", { name: /qa14-nokey\/deepseek-v4-flash/ }).first();
  const pickerHadOption = await option
    .waitFor({ timeout: 15_000 })
    .then(() => true)
    .catch(() => false);
  let after = null;
  let answer = "";
  let toast = "";
  let answeredAgain = false;
  if (pickerHadOption) {
    await option.click();
    await page.waitForTimeout(1200);
    await shot(page, "03-model-picked-keyless");

    const userTurns = await page.getByTestId("turn-user").count();
    const assistantTurns = await page.getByTestId("turn-assistant").count();
    await page.getByTestId("composer-input").fill("只回答两个字：可以。不要调用工具。");
    await page.getByTestId("composer-send").click();
    // Either the send is refused (the honest fix) or the turn fails; both are
    // "the picker took effect". A fresh answer is the bug. Wait for the *failure*
    // toast specifically: the switch announcement is still on screen.
    const sawFailure = await page
      .locator("[data-sonner-toast]")
      .filter({ hasText: "发送失败" })
      .first()
      .waitFor({ timeout: 20_000 })
      .then(() => true)
      .catch(() => false);
    await page.waitForTimeout(2_000);
    toast = await page
      .locator("[data-sonner-toast]")
      .allInnerTexts()
      .then((all) => all.join("\n"))
      .catch(() => "");
    const started =
      (await page.getByTestId("turn-user").count()) > userTurns ||
      (await page.getByTestId("turn-assistant").count()) > assistantTurns;
    if (started) await waitSettled(page).catch(() => {});
    answeredAgain = (await page.getByTestId("turn-assistant").count()) > assistantTurns;
    answer = await transcript(page);
    after = await api(page, "GET", `/v1/sessions/${sessionId}/status`);
    record.checks.keyless = { sawFailure };
    await shot(page, "04-model-switch-result");
  }

  const statusBefore = before.body?.model_ref ?? "";
  record.checks.model = {
    pickerHadOption,
    statusBefore,
    statusAfter: after?.body?.model_ref ?? "",
    dbRef: (await api(page, "GET", `/v1/sessions/${sessionId}`)).body?.session?.model_ref,
    toast,
    answeredAgain,
    answer: answer.slice(-1200),
  };
  console.log("[model]", JSON.stringify(record.checks.model, null, 2));

  ok(pickerHadOption, "the model picker lists the keyless provider");
  const refused = /还没有 API Key/.test(toast);
  const failedTurn = /模型调用失败|不存在|no key configured|model_not_found/.test(answer);
  ok(
    (refused || failedTurn) && !answeredAgain,
    "selecting a keyless provider does not silently answer from the old model",
  );

  // A model id the (working) provider does not serve must reach the model and
  // come back as a 404-class failure, with advice about the id — not the key.
  await page.getByTestId("composer-model").click();
  await page.getByRole("option", { name: "自定义模型 id…" }).first().click();
  const ghost = `volc/no-such-model-fixqa14-${stamp}`;
  await page.getByTestId("custom-model-input").fill(ghost);
  await page.getByTestId("custom-model-submit").click();
  const retryUsers = await page.getByTestId("turn-user").count();
  await page.getByTestId("composer-input").fill("只回答两个字：可以。不要调用工具。");
  await page.getByTestId("composer-send").click();
  await page.getByTestId("turn-user").nth(retryUsers).waitFor({ timeout: 30_000 });
  await waitSettled(page, 180_000).catch(() => {});
  await page.waitForTimeout(800);
  await shot(page, "05-nonexistent-model");
  const ghostText = await transcript(page);
  const ghostStatus = await api(page, "GET", `/v1/sessions/${sessionId}/status`);
  record.checks.nonexistent = {
    ref: ghost,
    runtimeModel: ghostStatus.body?.model_ref ?? "",
    transcript: ghostText.slice(-900),
  };
  console.log("[nonexistent]", JSON.stringify(record.checks.nonexistent, null, 2));
  ok(/404|model_not_found|does not exist/.test(ghostText), "a nonexistent model fails with a 404-class error");
  ok(/模型 id/.test(ghostText) && !/地址与密钥/.test(ghostText), "the advice is about the model id, not the key");

  // Back to the working model: the picker must take effect again, which means a
  // new process (the previous one was spawned with the ghost id).
  await page.getByTestId("composer-model").click();
  await page.getByRole("option", { name: /volc\/deepseek-v4-flash/ }).first().click();
  await page.waitForTimeout(1200);
  const backUsers = await page.getByTestId("turn-user").count();
  const backAssistant = await page.getByTestId("turn-assistant").count();
  await page.getByTestId("composer-input").fill("只回答两个字：可以。不要调用工具。");
  await page.getByTestId("composer-send").click();
  await page.getByTestId("turn-user").nth(backUsers).waitFor({ timeout: 30_000 });
  await waitSettled(page, 180_000);
  await page.waitForTimeout(500);
  await shot(page, "06-switched-back");
  const backText = await transcript(page);
  record.checks.switchedBack = { transcript: backText.slice(-600) };
  ok(
    (await page.getByTestId("turn-assistant").count()) > backAssistant && /可以/.test(backText),
    "switching back to the working model answers again",
  );
  record.checks.model.sessionId = sessionId;
}

// ---------------------------------------------------------------------------
// FINDING 3 — an interrupt is a normal outcome, not an auth outage.
// ---------------------------------------------------------------------------
if (wants("interrupt")) {
  const third = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: `fixqa14 interrupt ${stamp}`,
  });
  const sessionId = third.body.session.id;
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
  await send(page, "只回答两个字：可以。不要调用工具。");

  const before = await page.getByTestId("turn-user").count();
  await page.getByTestId("composer-input").fill("用 bash 运行 sleep 30，等命令结束后告诉我完成。");
  await page.getByTestId("composer-send").click();
  await page.getByTestId("turn-user").nth(before).waitFor({ timeout: 30_000 });
  // Let the tool call start, then stop it the way a user does.
  await page.waitForTimeout(6_000);
  await page.getByTestId("composer-stop").click();
  await page.waitForTimeout(900);
  await shot(page, "07-just-after-interrupt");
  await waitSettled(page).catch(() => {});
  await page.waitForTimeout(1_500);
  await shot(page, "08-interrupt-settled");
  const text = await transcript(page);
  const body = await page.locator("body").innerText();
  const marker = page.getByTestId("run-interrupted");
  const markerVisible = (await marker.count()) > 0;
  record.checks.interrupt = {
    transcript: text.slice(-1500),
    hasModelFailure: body.includes("模型调用失败"),
    hasCredentialAdvice: body.includes("检查「设置 → 模型」"),
    markerVisible,
    markerText: markerVisible ? await marker.innerText() : "",
    hasInterruptedTool: /\d+ 个已中断/.test(body),
    hasFailedToolGroup: /\d+ 个失败/.test(body),
    hasRunningToolGroup: /\d+ 个运行中/.test(body),
    sessionId,
  };
  console.log("[interrupt]", JSON.stringify(record.checks.interrupt, null, 2));

  ok(!record.checks.interrupt.hasModelFailure, "an interrupt does not render 模型调用失败");
  ok(!record.checks.interrupt.hasCredentialAdvice, "an interrupt does not show credential advice");
  ok(markerVisible && record.checks.interrupt.markerText.includes("已中断"), "an interrupt renders a neutral 已中断 marker");
  ok(!record.checks.interrupt.hasFailedToolGroup, "an interrupt does not paint a tool group as failed");
  ok(!record.checks.interrupt.hasRunningToolGroup, "an interrupt does not leave a tool group running");
}

record.summary = {
  ok: record.findings.filter((f) => f.ok).length,
  broken: record.findings.filter((f) => !f.ok).length,
};
save();
console.log("[summary]", JSON.stringify(record.summary));
await browser.close();
process.exitCode = record.summary.broken === 0 ? 0 : 1;
