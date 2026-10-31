// Deployed verification for the fork-transcript truncation fix (pf-local, :7722).
//
//   node e2e/scripts/fix-fork-copy-probe.mjs
//
// The old fork copied the parent's event log with ListEvents(..., 0, 1000) and
// never told the user, so a long parent produced a branch that stopped at event
// 1000. This script builds a real parent longer than that on the deployment
// (many short real-model turns), forks it through the same API the UI uses, and
// asserts the branch page shows the first *and* the last turn, then screenshots
// the transcript top and bottom.
//
// It deletes its own project, sessions and fork before exiting.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-fork-copy");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const OLD_COPY_CAP = 1000;
const stamp = Date.now();

const record = { base: BASE, stamp, checks: {}, findings: [] };
const save = () =>
  writeFileSync(path.join(out, "deployed-probe-log.json"), JSON.stringify(record, null, 2));
const note = (key, value) => {
  record.checks[key] = value;
  console.log(`[${key}]`, JSON.stringify(value));
};
const ok = (condition, message) => {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  record.findings.push({ ok: Boolean(condition), message });
  return Boolean(condition);
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";

async function login(page, who) {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(who.email);
  await page.getByTestId("login-password").fill(who.password);
  const submit = page.getByTestId("login-submit");
  await submit.waitFor({ state: "visible", timeout: 30_000 });
  for (let i = 0; i < 40; i++) {
    if (await submit.isEnabled()) break;
    await page.waitForTimeout(250);
  }
  await submit.click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
}

async function api(page, method, url, data) {
  const res = await page.request.fetch(`${BASE}${url}`, {
    method,
    headers: { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
    data,
    timeout: 180_000,
  });
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status(), body };
}

async function status(page, sessionId) {
  const res = await page.request.get(`${BASE}/v1/sessions/${sessionId}/status`);
  if (!res.ok()) throw new Error(`status ${res.status()}`);
  return await res.json();
}

/** Waits for a real-model turn to finish and returns its newest seq. */
async function settle(page, sessionId, budgetMs = 180_000) {
  const deadline = Date.now() + budgetMs;
  let last = null;
  while (Date.now() < deadline) {
    last = await status(page, sessionId);
    if (!last.running && !last.streaming && last.queue_depth === 0) return last;
    await page.waitForTimeout(500);
  }
  throw new Error(`the run did not settle: ${JSON.stringify(last)}`);
}

async function scroll(page, toBottom) {
  // The transcript's auto-scroll anchors to the last user turn and keeps
  // adjusting while the replay paints, so a one-shot scrollTo can be undone.
  // Scroll, verify the position, and retry until it holds.
  const stream = page.getByTestId("session-stream");
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await stream.evaluate((node, bottom) => {
      node.scrollTo({ top: bottom ? node.scrollHeight : 0, behavior: "instant" });
    }, toBottom);
    await page.waitForTimeout(200);
    const at = await stream.evaluate((node) => ({
      top: node.scrollTop,
      bottom: node.scrollTop + node.clientHeight >= node.scrollHeight - 2,
    }));
    if (toBottom ? at.bottom : at.top === 0) return;
  }
  throw new Error(`the transcript did not scroll to ${toBottom ? "bottom" : "top"}`);
}

let projectId = "";
let parentId = "";
let childId = "";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
try {
  await login(page, USER);

  // --- the long parent ------------------------------------------------------
  const project = await api(page, "POST", "/v1/projects", {
    name: `fork-copy-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  if (!ok(project.status < 300, `created a probe project (${project.status})`)) {
    throw new Error(`project create: ${project.status} ${JSON.stringify(project.body)}`);
  }
  projectId = project.body.project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: "long transcript before fork",
  });
  if (!ok(session.status < 300, `created the parent session (${session.status})`)) {
    throw new Error(`session create: ${session.status} ${JSON.stringify(session.body)}`);
  }
  parentId = session.body.session.id;

  const prompts = [];
  let parentStatus = null;
  for (let turn = 1; turn <= 12; turn += 1) {
    const text =
      `第 ${turn} 轮：请用大约 200 个汉字说明 Go 的 defer 执行顺序和常见误区，` +
      `不要调用任何工具。最后单独一行写 END-${turn}。`;
    const sent = await api(page, "POST", `/v1/sessions/${parentId}/messages`, {
      client_msg_id: `forkcopy-${stamp}-${turn}`,
      text,
    });
    if (!ok(sent.status === 200, `turn ${turn} was accepted (${sent.status})`)) {
      throw new Error(`send: ${sent.status} ${JSON.stringify(sent.body)}`);
    }
    prompts.push({ turn, text, end: `END-${turn}` });
    parentStatus = await settle(page, parentId);
    note(`turn_${turn}_last_seq`, parentStatus.last_seq);
    if (parentStatus.last_seq > OLD_COPY_CAP + 100) break;
  }
  const parentSeq = parentStatus.last_seq;
  note("parent_last_seq", parentSeq);
  note("prompts_sent", prompts.length);
  if (
    !ok(
      parentSeq > OLD_COPY_CAP,
      `the deployed parent is longer than the old ${OLD_COPY_CAP}-event copy cap (seq ${parentSeq})`,
    )
  ) {
    throw new Error("the parent never crossed the old cap");
  }

  // --- fork at the last user message ---------------------------------------
  const points = await page.request.get(`${BASE}/v1/sessions/${parentId}/fork-points`);
  const pointBody = await points.json();
  note("fork_points", pointBody.points.length);
  if (!ok(pointBody.points.length > 0, "the parent reports fork points")) {
    throw new Error("no fork points");
  }
  const entry = pointBody.points[pointBody.points.length - 1];
  const forked = await api(page, "POST", `/v1/sessions/${parentId}/fork`, {
    entry_id: entry.entry_id,
    label: "long transcript fork",
  });
  if (!ok(forked.status < 300, `fork created (${forked.status})`)) {
    throw new Error(`fork: ${forked.status} ${JSON.stringify(forked.body)}`);
  }
  childId = forked.body.session.id;
  note("forked_at_seq", forked.body.session.forked_at_seq ?? 0);
  ok(
    (forked.body.session.forked_at_seq ?? 0) >= OLD_COPY_CAP,
    `the fork recorded a lineage cut, not a copy (forked_at_seq ${forked.body.session.forked_at_seq})`,
  );

  // --- the branch page ------------------------------------------------------
  await page.goto(`${BASE}/p/${projectId}/s/${childId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 60_000 });
  const users = page.getByTestId("turn-user");
  await users.first().waitFor({ timeout: 90_000 });
  const deadline = Date.now() + 120_000;
  let count = 0;
  while (Date.now() < deadline) {
    count = await users.count();
    if (count >= prompts.length) break;
    await page.waitForTimeout(500);
  }
  note("child_user_turns", count);
  note("expected_user_turns", prompts.length);
  ok(count === prompts.length, `the fork shows all ${prompts.length} user turns (${count})`);
  const firstText = (await users.first().innerText()).replace(/\s+/g, " ");
  const lastText = (await users.last().innerText()).replace(/\s+/g, " ");
  note("first_turn_has_marker", firstText.includes("第 1 轮"));
  note("last_turn_has_marker", lastText.includes(`第 ${prompts.length} 轮`));
  ok(firstText.includes("第 1 轮"), "the fork's first turn is the parent's first prompt");
  ok(
    lastText.includes(`第 ${prompts.length} 轮`),
    `the fork's last turn is the parent's last prompt (turn ${prompts.length})`,
  );
  const assistant = page.getByTestId("turn-assistant").last();
  const assistantText = (await assistant.innerText()).replace(/\s+/g, " ");
  note("last_assistant_has_end_marker", assistantText.includes(prompts[prompts.length - 1].end));
  ok(
    assistantText.includes(prompts[prompts.length - 1].end),
    `the answer that followed the last prompt is in the fork (${prompts[prompts.length - 1].end})`,
  );

  // The page opens scrolled to the last anchor, so the first shot is the tail.
  // Let the replay settle before scrolling: a screenshot taken mid-paint is a
  // picture of the previous scroll position, not of the transcript's end.
  await page.waitForTimeout(1_500);
  await scroll(page, true);
  await page.screenshot({ path: path.join(out, "deployed-fork-bottom.png") });
  await scroll(page, false);
  await page.screenshot({ path: path.join(out, "deployed-fork-top.png") });
  await scroll(page, true);

  // --- the API's own transcript (export uses the merged reader) -------------
  const exported = await page.request.get(`${BASE}/v1/sessions/${childId}/export?format=md`);
  const markdown = await exported.text();
  const userLines = (markdown.match(/^## 我$/gm) ?? []).length;
  note("export_user_messages", userLines);
  ok(userLines === prompts.length, `the export carries every user message (${userLines})`);
  ok(
    markdown.includes(prompts[prompts.length - 1].end),
    `the export carries the tail answer (${prompts[prompts.length - 1].end})`,
  );
  writeFileSync(path.join(out, "deployed-fork-export.md"), markdown);
} catch (error) {
  record.error = String(error?.stack ?? error);
  console.log("[BROKEN]", record.error);
} finally {
  // --- cleanup: stop the probe's sessions and project, in dependency order --
  if (childId) {
    const deleted = await api(page, "DELETE", `/v1/sessions/${childId}`).catch(() => null);
    note("cleanup_fork", deleted?.status ?? "error");
  }
  if (parentId) {
    const deleted = await api(page, "DELETE", `/v1/sessions/${parentId}`).catch(() => null);
    note("cleanup_parent", deleted?.status ?? "error");
  }
  if (projectId) {
    const deleted = await api(page, "DELETE", `/v1/projects/${projectId}`).catch(() => null);
    note("cleanup_project", deleted?.status ?? "error");
  }
  save();
  await browser.close();
}

const failed = record.findings.filter((finding) => !finding.ok);
console.log(`[RESULT] ${failed.length === 0 ? "PASS" : "FAIL"} ${record.findings.length} checks`);
process.exitCode = failed.length === 0 && !record.error ? 0 : 1;
