#!/usr/bin/env node
/**
 * Deployed-app verification for the interruption recovery path (default
 * http://127.0.0.1:7722).
 *
 * It drives two real runs against the real model provider:
 *
 *   1. stop: start a long `sleep`, click 停止, assert the marker names the cause
 *      (停止) and the survived state (dirty files + HEAD), screenshot it, click
 *      继续, and screenshot the turn the agent actually received;
 *   2. restart: start another long run, `docker restart pf-local` while it is in
 *      flight, wait for startup reconciliation to mark it interrupted, reload,
 *      and screenshot the same view with the other cause (服务重启).
 *
 * The copy must differ by cause; the checkpoint asserts that. It cleans up both
 * probe projects and reports any running session it created.
 *
 *   node e2e/scripts/fix-recovery-probe.mjs
 *
 * Evidence: e2e/.artifacts/fix-recovery/probe.json and the *.png screenshots.
 */
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(HERE, "../.artifacts/fix-recovery");
mkdirSync(OUT, { recursive: true });

const BASE = (process.env.BASE ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const CONTAINER = process.env.PIFORGE_CONTAINER ?? "pf-local";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const stamp = Date.now();
const evidence = { base: BASE, container: CONTAINER, at: new Date().toISOString(), steps: [], screenshots: [] };
const save = () => writeFileSync(path.join(OUT, "probe.json"), JSON.stringify(evidence, null, 2));
const step = (name, value = {}) => {
  evidence.steps.push({ name, ...value });
  save();
  console.log(`[step] ${name}`, JSON.stringify(value));
};
const check = (condition, message, detail) => {
  if (!condition) {
    evidence.failed = { message, detail };
    save();
    throw new Error(`[BROKEN] ${message}${detail ? `: ${JSON.stringify(detail)}` : ""}`);
  }
  console.log("[ok]", message);
};

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(fn, { timeout = 60_000, interval = 1_000, what = "condition" } = {}) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await fn();
    if (last) return last;
    await sleep(interval);
  }
  throw new Error(`timed out waiting for ${what}; last=${JSON.stringify(last)}`);
}

async function waitForHealth(timeout = 180_000) {
  await waitFor(
    async () => {
      try {
        const res = await fetch(`${BASE}/healthz`);
        return res.ok;
      } catch {
        return false;
      }
    },
    { timeout, what: `${BASE}/healthz` },
  );
}

// The control plane and the Next.js server start independently inside the
// container; /healthz is the Go process. Reloading before Next is listening
// leaves the browser on a proxy error page, which is not a product failure —
// it is the deployment still coming up.
async function waitForWeb(timeout = 180_000) {
  await waitFor(
    async () => {
      try {
        const res = await fetch(`${BASE}/login`, { redirect: "manual" });
        return res.status === 200 || res.status === 307;
      } catch {
        return false;
      }
    },
    { timeout, what: "the web app to accept requests" },
  );
}

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1500, height: 950 } });
const page = await context.newPage();
const createdProjects = [];
const createdSessions = [];
let auth = false;

const csrf = async () => (await context.cookies()).find((c) => c.name === "pf_csrf")?.value ?? "";
async function api(method, route, data) {
  const res = await page.request.fetch(`${BASE}${route}`, {
    method,
    headers: { "X-PF-CSRF": await csrf() },
    data,
    failOnStatusCode: false,
  });
  return res;
}
async function json(route) {
  const res = await api("GET", route);
  if (!res.ok()) throw new Error(`GET ${route} -> ${res.status()}`);
  return res.json();
}

async function login() {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill(USER.password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
  auth = true;
}

async function ensureLoggedIn() {
  if (page.url().includes("/login")) {
    await login();
    return;
  }
  if (!auth) await login();
}

async function createProject(label) {
  const res = await api("POST", "/v1/projects", {
    name: `probe-recovery-${label}-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  check(res.status() < 300, `project ${label} created`, { status: res.status() });
  const body = await res.json();
  createdProjects.push(body.project.id);
  return body.project.id;
}

async function createSession(projectId) {
  const res = await api("POST", `/v1/projects/${projectId}/sessions`, {
    title: `recovery probe ${stamp}`,
  });
  check(res.status() < 300, "session created", { status: res.status() });
  const sessionId = (await res.json()).session.id;
  createdSessions.push(sessionId);
  return sessionId;
}

async function writeDirtyFile(sessionId) {
  const res = await api(
    "PUT",
    `/v1/sessions/${sessionId}/files?path=recovery-probe.txt`,
    "written before the interrupted run\n",
  );
  check(res.status() < 300, "dirty file written", { status: res.status() });
}

async function openSession(projectId, sessionId) {
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.locator(`[data-session-id="${sessionId}"]`).waitFor({ timeout: 40_000 });
}

async function sendLongRun(prompt) {
  const before = await page.getByTestId("turn-user").count();
  await page.getByTestId("composer-input").fill(prompt);
  await page.getByTestId("composer-send").click();
  await waitFor(
    async () => (await page.getByTestId("run-state").getAttribute("data-state")) === "running",
    { timeout: 60_000, interval: 250, what: "the run to report running" },
  );
  // A tool call is the reliable "still in flight" anchor; if the model answers
  // with prose instead, the run indicator above is still the fallback.
  try {
    await page.getByTestId("tool-group").first().waitFor({ timeout: 120_000 });
    step("tool call started", { before });
  } catch {
    step("no tool call observed; continuing with the run still running", { before });
  }
}

async function markerView() {
  const marker = page.getByTestId("run-interrupted");
  await marker.waitFor({ timeout: 60_000 });
  await marker.scrollIntoViewIfNeeded();
  await page.getByTestId("recovery-survival").waitFor({ timeout: 30_000 });
  const text = await marker.innerText();
  const survival = await page.getByTestId("recovery-survival").innerText();
  const cause = (await page.getByTestId("recovery-cause").innerText()).trim();
  return { text, survival, cause };
}

async function shot(name) {
  await page.screenshot({ path: path.join(OUT, name), animations: "disabled" });
  evidence.screenshots.push(name);
  save();
  console.log("[shot]", name);
}

const PROMPT =
  "请只做一件事：用 bash 工具运行 `sleep 240`，在命令结束之前不要回复，也不要修改任何文件。" +
  "这是恢复流程的探针。";

try {
  await login();
  step("logged in", { user: USER.email });
  const baseline = await json("/v1/sessions?limit=200");
  const baselineRunning = (baseline.sessions ?? []).filter((s) => s.status === "running").map((s) => s.id);
  step("baseline running sessions", { ids: baselineRunning });

  // ---------------- stop ----------------
  const stopProject = await createProject("stop");
  const stopSession = await createSession(stopProject);
  await writeDirtyFile(stopSession);
  await openSession(stopProject, stopSession);
  await sendLongRun(PROMPT);

  await page.getByTestId("composer-stop").click();
  const stopView = await markerView();
  check(stopView.cause.includes("停止"), "the stop marker names 停止", stopView);
  check(stopView.text.includes("用户停止，本次运行被中断"), "the stop marker carries the server sentence", stopView);
  check(/工作区有 [1-9]\d* 个未提交改动/.test(stopView.survival), "the stop marker counts dirty files", stopView);
  check(/最后一次提交 [0-9a-f]{7}/.test(stopView.survival), "the stop marker names a commit", stopView);
  await shot("01-stop-marker.png");
  step("stop marker", stopView);

  const stopRecovery = await json(`/v1/sessions/${stopSession}/recovery`);
  check(stopRecovery.cause === "stop" && stopRecovery.can_continue === true, "GET recovery agrees", stopRecovery);
  check(stopRecovery.last_request === PROMPT, "the last request is the one that was sent", stopRecovery);

  await page.getByTestId("recovery-continue").click();
  await expectCount(page.getByTestId("turn-user"), 2, 60_000);
  const sent = await page.getByTestId("turn-user").last().innerText();
  check(sent.includes("修复") || sent.includes(PROMPT), "the continued turn carries the request", { sent });
  check(sent.includes("用户停止，本次运行被中断"), "the continued turn names the cause", { sent });
  check(sent.includes("请先检查当前状态") && sent.includes("git status"), "the continued turn asks to verify state", { sent });
  await page.getByTestId("turn-user").last().scrollIntoViewIfNeeded();
  await shot("02-stop-continued-turn.png");
  step("stopped and continued", { sent });

  const afterContinue = await json(`/v1/sessions/${stopSession}/recovery`);
  check(afterContinue.can_continue === false, "继续 is not offered twice", afterContinue);

  // End the continued run so the cleanup delete is not refused as "a running
  // turn". The second stop is a normal outcome; it is not part of the evidence.
  try {
    const stopAgain = page.getByTestId("composer-stop");
    if (await stopAgain.isVisible()) await stopAgain.click();
    await waitFor(
      async () => (await page.getByTestId("run-state").getAttribute("data-state")) === "settled",
      { timeout: 60_000, interval: 250, what: "the continued run to settle" },
    );
  } catch (error) {
    step("could not stop the continued run before cleanup", { error: error.message });
  }

  // ---------------- restart ----------------
  const restartProject = await createProject("restart");
  const restartSession = await createSession(restartProject);
  await writeDirtyFile(restartSession);
  await openSession(restartProject, restartSession);
  await sendLongRun(PROMPT);

  step("restarting the deployment", { container: CONTAINER });
  execFileSync("docker", ["restart", CONTAINER], { stdio: "inherit" });
  await waitForHealth();
  await waitForWeb();
  await waitFor(
    async () => {
      const view = await json(`/v1/sessions/${restartSession}/recovery`);
      return view.interrupted ? view : false;
    },
    { timeout: 120_000, what: "startup reconciliation to mark the run interrupted" },
  );
  step("deployment restarted and reconciliation ran");

  const sessionUrl = `${BASE}/p/${restartProject}/s/${restartSession}`;
  let loaded = false;
  for (let attempt = 0; attempt < 4 && !loaded; attempt += 1) {
    await page.goto(sessionUrl);
    try {
      await page.locator(`[data-session-id="${restartSession}"]`).waitFor({ timeout: 30_000 });
      loaded = true;
    } catch {
      if (page.url().includes("/login")) await login();
      else await sleep(2_000);
    }
  }
  check(loaded, "the session page loads after the restart", { url: page.url() });
  const restartView = await markerView();
  check(restartView.cause.includes("服务重启"), "the restart marker names 服务重启", restartView);
  check(restartView.text.includes("服务重启，本次运行被中断"), "the restart marker carries the server sentence", restartView);
  check(/工作区有 [1-9]\d* 个未提交改动/.test(restartView.survival), "the restart marker still measures the checkout", restartView);
  check(restartView.text !== stopView.text, "the copy differs by cause", { stop: stopView.text, restart: restartView.text });
  await shot("03-restart-marker.png");
  step("restart marker", restartView);

  const restartRecovery = await json(`/v1/sessions/${restartSession}/recovery`);
  check(restartRecovery.cause === "restart", "GET recovery reports the restart cause", restartRecovery);
} finally {
  // Clean up the probe projects. Deleting a project stops its processes,
  // destroys its sandbox and removes its rows, so it is also the way to leave
  // no running session behind.
  for (const sessionId of createdSessions) {
    try {
      await api("POST", `/v1/sessions/${sessionId}/interrupt`, {});
    } catch {
      // An idle session has nothing to interrupt; the delete below is the point.
    }
  }
  await sleep(1_000);
  for (const id of createdProjects) {
    try {
      let status = 0;
      for (let attempt = 0; attempt < 4; attempt += 1) {
        const res = await api("DELETE", `/v1/projects/${id}`);
        status = res.status();
        console.log(`[cleanup] DELETE project ${id} -> ${status}`);
        if (status < 300) break;
        // 409 means a turn is still running; stop it and retry.
        for (const sessionId of createdSessions) {
          await api("POST", `/v1/sessions/${sessionId}/interrupt`, {});
        }
        await sleep(1_500);
      }
      if (status >= 300) evidence.cleanup_failed = true;
    } catch (error) {
      console.log(`[cleanup] DELETE project ${id} failed: ${error.message}`);
      evidence.cleanup_failed = true;
    }
  }
  try {
    if (auth) {
      const after = await json("/v1/sessions?limit=200");
      const running = (after.sessions ?? []).filter((s) => s.status === "running");
      evidence.running_after = running.map((s) => s.id);
      const probe = (after.sessions ?? []).filter((s) => s.title?.startsWith(`recovery probe ${stamp}`));
      evidence.probe_sessions_after = probe.map((s) => s.id);
      console.log(`[cleanup] running sessions: ${running.length}; probe sessions: ${probe.length}`);
      if (evidence.running_after.length > 0 || probe.length > 0) {
        evidence.cleanup_failed = true;
      }
    }
  } catch (error) {
    console.log(`[cleanup] final survey failed: ${error.message}`);
    evidence.cleanup_failed = true;
  }
  save();
  await browser.close();
}

async function expectCount(locator, count, timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if ((await locator.count()) === count) return;
    await sleep(500);
  }
  throw new Error(`expected ${count} elements, found ${await locator.count()}`);
}
