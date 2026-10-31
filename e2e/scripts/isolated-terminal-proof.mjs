// One run of the isolated deployment's user path, with the terminal measured
// from both ends.
//
//   node e2e/scripts/twoblockes-isolated-proof.mjs --base http://127.0.0.1:7723 [--prompt "..."]
//
// Steps, in order:
//   1. sign in through the real form;
//   2. create a project + session and send one real prompt; wait for the run to
//      settle (that is what provisions the sandbox the agent runs in);
//   3. click the 终端 tab and record what the panel says and its
//      data-pty-status;
//   4. open the relay socket directly, type `hostname` and `pwd`, and keep every
//      frame — this is the transcript that decides whether the terminal works
//      and whether its output belongs to the sandbox;
//   5. print the sandbox container name (from the project's sandbox record) so
//      the caller can compare it with the hostname the shell printed.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/two-blockers");
mkdirSync(out, { recursive: true });

const arg = (name, fallback) => {
  const i = process.argv.indexOf(name);
  return i > -1 ? process.argv[i + 1] : fallback;
};
const BASE = arg("--base", "http://127.0.0.1:7723");
const PROMPT = arg("--prompt", "只回复：PROBE-OK");
const TAG = arg("--tag", "proof");
// The sandbox container's hostname (docker inspect --format '{{.Config.Hostname}}')
// and, optionally, the control plane's: the shell must answer with the first and
// must not answer with the second.
const EXPECT_HOST = arg("--expect-host", "");
const CONTROL_HOST = arg("--control-host", "");
const stamp = new Date().toISOString().replace(/[:.]/g, "-");
const log = { base: BASE, prompt: PROMPT, startedAt: new Date().toISOString(), steps: [] };
const render = (state) =>
  [`=== isolated proof ${state.base} ${state.startedAt} ===`]
    .concat(
      state.steps.flatMap((s) => [
        `--- ${s.step} @ ${s.at}`,
        typeof s.detail === "string" ? s.detail : JSON.stringify(s.detail, null, 2),
      ]),
    )
    .join("\n") + "\n";
const save = () => {
  writeFileSync(path.join(out, `${TAG}-${stamp}.json`), JSON.stringify(log, null, 2));
  writeFileSync(path.join(out, `${TAG}-${stamp}.txt`), render(log));
};
const note = (step, detail) => {
  log.steps.push({ step, at: new Date().toISOString(), detail });
  console.log(`[${step}] ${typeof detail === "string" ? detail : JSON.stringify(detail)}`);
  save();
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
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

const browser = await chromium.launch({ channel: "chrome" });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  // Every socket the app opens, with the frames it received: the panel's own
  // terminal socket is otherwise a black box, and the acceptance's decisive
  // evidence was exactly this frame list.
  await page.addInitScript(() => {
    window.__wslog = [];
    const Original = window.WebSocket;
    window.WebSocket = class extends Original {
      constructor(...args) {
        super(...args);
        const rec = { url: String(args[0]), at: [], frames: [] };
        window.__wslog.push(rec);
        const t0 = performance.now();
        this.addEventListener("open", () => rec.at.push({ ms: Math.round(performance.now() - t0), event: "open" }));
        this.addEventListener("message", (e) => {
          rec.at.push({ ms: Math.round(performance.now() - t0), event: "message" });
          if (typeof e.data === "string") rec.frames.push(`text:${e.data}`);
          else rec.frames.push(`binary:${new TextDecoder().decode(new Uint8Array(e.data))}`);
        });
        this.addEventListener("close", (e) =>
          rec.at.push({ ms: Math.round(performance.now() - t0), event: "close", code: e.code, reason: e.reason }),
        );
        this.addEventListener("error", () => rec.at.push({ ms: Math.round(performance.now() - t0), event: "error" }));
      }
    };
  });
  page.on("console", (m) => {
    if (m.type() === "error") note("console-error", m.text().slice(0, 400));
  });

  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill("dev@piforge.local");
  await page.getByTestId("login-password").fill("piforge-dev-123");
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
  note("login", "signed in as dev@piforge.local");

  const project = await api(page, "POST", "/v1/projects", {
    name: `isolated-${TAG}-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  if (project.status >= 300) throw new Error(`create project: ${project.status}`);
  const projectId = project.body.project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: `${TAG}-run` });
  const sessionId = session.body.session.id;
  note("session", { projectId, sessionId });

  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 60_000 });
  // Wait for the sandbox to be provisioned before prompting: the composer is
  // disabled until the session has a worktree.
  await page.waitForFunction(
    () => {
      const send = document.querySelector('[data-testid="composer-send"]');
      return send !== null && !send.disabled;
    },
    undefined,
    { timeout: 120_000 },
  );
  const sendStartedAt = Date.now();
  const accepted = page.waitForResponse(
    (res) =>
      res.url().includes(`/v1/sessions/${sessionId}/messages`) &&
      res.request().method() === "POST",
    { timeout: 120_000 },
  );
  await page.getByTestId("composer-input").fill(PROMPT);
  await page.getByTestId("composer-send").click();
  const acceptedResponse = await accepted;
  note("prompt", {
    text: PROMPT,
    status: acceptedResponse.status(),
    acceptedMs: Date.now() - sendStartedAt,
  });

  // Provisioning a sandbox happens between the accepted message and the run, so
  // "settled" can be true before the run ever starts. The answer is the signal
  // that a model call really happened.
  await page.waitForFunction(
    () =>
      [...document.querySelectorAll('[data-testid="turn-assistant"]')].some(
        (turn) => (turn.textContent ?? "").trim().length > 0,
      ),
    undefined,
    { timeout: 300_000 },
  );
  await page.waitForFunction(
    () => document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") === "settled",
    undefined,
    { timeout: 300_000 },
  );
  note("settled", { settledMs: Date.now() - sendStartedAt });

  const answer = await page.evaluate(() => {
    const turns = [...document.querySelectorAll('[data-testid="turn-assistant"]')];
    return turns.map((t) => t.textContent.trim()).join("\n").slice(-1200);
  });
  note("answer", answer);

  // The sandbox container the control plane provisioned for this project.
  const sandboxes = await api(page, "GET", `/v1/projects/${projectId}/sandbox`);
  note("sandbox", { status: sandboxes.status, body: sandboxes.body });
  // The panel, opened the way a user opens it.
  await page.getByTestId("panel-tab-terminal").click();
  await page.waitForTimeout(6000);
  const panel = await page.evaluate(() => {
    const pane = document.querySelector('[data-testid="terminal-pane"]');
    return {
      status: pane?.getAttribute("data-pty-status") ?? "",
      notice: document.querySelector('[data-testid="terminal-disconnected"]')?.textContent ?? "",
      rows:
        document
          .querySelector('[data-testid="terminal-tab-panel"] [data-slot="xterm-surface"] .xterm-rows')
          ?.textContent ?? "",
    };
  });
  note("panel", panel);
  const panelSockets = await page.evaluate(() =>
    (window.__wslog ?? []).filter((s) => s.url.includes("/pty")),
  );
  note("panel-sockets", panelSockets);
  await page.screenshot({ path: path.join(out, `${TAG}-panel-${stamp}.png`), animations: "disabled" });

  // The relay itself, typed into.
  const relay = await page.evaluate(
    async ({ sessionId, tab }) => {
      const frames = [];
      const events = [];
      const ws = new WebSocket(
        `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/v1/sessions/${sessionId}/pty?tab=${tab}`,
      );
      ws.binaryType = "arraybuffer";
      const openedAt = Date.now();
      const opened = new Promise((resolve) => ws.addEventListener("open", () => resolve(true)));
      const closed = new Promise((resolve) =>
        ws.addEventListener("close", (e) =>
          resolve({ code: e.code, reason: e.reason, atMs: Date.now() - openedAt }),
        ),
      );
      ws.addEventListener("message", (e) => {
        events.push({ atMs: Date.now() - openedAt, kind: typeof e.data === "string" ? "text" : "binary" });
        if (typeof e.data === "string") frames.push(`text:${e.data}`);
        else frames.push(`binary:${new TextDecoder().decode(new Uint8Array(e.data))}`);
      });
      await Promise.race([opened, new Promise((r) => setTimeout(r, 5000))]);
      await new Promise((r) => setTimeout(r, 1500));
      const send = (s) => ws.readyState === WebSocket.OPEN && ws.send(new TextEncoder().encode(s));
      // One command at a time: the image joins consecutive command outputs
      // without a separator, so a burst of two lines would run them together
      // (`<hostname>/workspace/repo`) and the answer could not be attributed.
      send("hostname\n");
      await new Promise((r) => setTimeout(r, 1800));
      send("pwd\n");
      await new Promise((r) => setTimeout(r, 1800));
      const state = ws.readyState;
      if (ws.readyState === WebSocket.OPEN) ws.close(1000, "probe done");
      const close = await Promise.race([closed, new Promise((r) => setTimeout(() => r(null), 3000))]);
      return { frames, events, close, state };
    },
    { sessionId, tab: `${TAG}-direct` },
  );
  note("relay", relay);

  // ---------------------------------------------------------------------
  // Assertions: this is the acceptance's test, not only its transcript.
  // ---------------------------------------------------------------------
  const failures = [];
  if (!answer.trim()) {
    failures.push("the prompt settled without an assistant answer");
  }
  if (panel.status !== "open") {
    failures.push(`the panel's terminal is ${panel.status || "(no status)"}, want open`);
  }
  const controlFrames = relay.frames.filter((f) => f.startsWith("text:"));
  if (controlFrames.some((f) => f.includes('"type":"exit"'))) {
    failures.push("the relay ended the shell with an exit frame");
  }
  if (controlFrames.some((f) => f.includes('"type":"error"'))) {
    failures.push(
      `the relay reported an error: ${controlFrames.find((f) => f.includes('"type":"error"'))}`,
    );
  }
  const transcript = relay.frames
    .filter((f) => f.startsWith("binary:"))
    .map((f) => f.slice("binary:".length))
    .join("")
    // The container's shell echoes the command and wraps at its own width.
    .replace(/\r/g, "")
    .replace(/\u001b\[[0-9;?]*[a-zA-Z]/g, "");

  const lines = transcript
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
  // A container hostname is the container id's first 12 hex characters; the
  // checkout path is the container path, not a host one.
  const shellHost = lines.find((line) => /^[0-9a-f]{12}$/.test(line)) ?? "";
  const shellPwd = lines.find((line) => line.startsWith("/workspace/")) ?? "";
  note("shell", { hostname: shellHost, pwd: shellPwd, transcript });

  if (!shellHost) failures.push("the shell never answered `hostname`");
  if (!shellPwd) failures.push("the shell never answered `pwd`");
  if (EXPECT_HOST && shellHost && shellHost !== EXPECT_HOST) {
    failures.push(`the terminal ran on ${shellHost}, not in the sandbox ${EXPECT_HOST}`);
  }
  if (CONTROL_HOST && shellHost && shellHost === CONTROL_HOST) {
    failures.push(`the terminal ran in the control plane (${CONTROL_HOST}), not in a sandbox`);
  }
  if (shellPwd && shellPwd !== "/workspace/repo") {
    failures.push(`pwd = ${shellPwd}, want the sandbox checkout /workspace/repo`);
  }

  log.result = failures.length === 0 ? "PASS" : "FAIL";
  log.failures = failures;
  save();
  if (failures.length > 0) {
    for (const failure of failures) console.error(`[FAIL] ${failure}`);
    process.exitCode = 1;
  } else {
    console.log(`[PASS] the terminal ran in the sandbox: hostname=${shellHost} pwd=${shellPwd}`);
  }
} finally {
  await browser.close();
}

console.log(`[done] ${path.join(out, `${TAG}-${stamp}.json`)}`);
