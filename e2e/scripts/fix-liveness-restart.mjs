// Fail-first and fix for the remote-liveness gap, with real binaries and no
// Docker: a fake runner service and a fake AIO sandbox stand in for a container
// deployment, and both binaries are the real control plane.
//
//   node e2e/scripts/fix-liveness-restart.mjs
//
// For each phase it prepares the one state a control-plane restart leaves
// behind — a session whose row says running and whose run is started, with the
// agent process gone because the process that spawned it was killed — and then
// boots the phase's binary with `-sandbox runner` pointed at a fake runner whose
// sandbox reports a *fresh heartbeat* for that session:
//
//   old  the binary from the commit before this fix (51ff223). It only knows its
//        in-process registry, which is empty after a restart, so it writes
//        interrupted over a live remote run. That is the defect.
//   new  this tree's binary. Its probe asks the sandbox, sees the heartbeat and
//        keeps the session running.
//
// Evidence lands in e2e/.artifacts/fix-liveness/ (restart-<phase>.json and the
// two binaries' boot logs).
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "../..");
const backend = path.join(repo, "backend");
const out = path.resolve(here, "../.artifacts/fix-liveness");
mkdirSync(out, { recursive: true });

const OLD_REF = process.env.LIVENESS_OLD_REF ?? "51ff223";
const HOST = "127.0.0.1";
const APP_PORT = Number(process.env.LIVENESS_APP_PORT ?? 8099);
const RUNNER_PORT = Number(process.env.LIVENESS_RUNNER_PORT ?? 7855);
const SANDBOX_PORT = Number(process.env.LIVENESS_SANDBOX_PORT ?? 7856);
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const findings = [];

function ok(condition, message) {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  findings.push({ phase: currentPhase, ok: Boolean(condition), message });
  return Boolean(condition);
}
let currentPhase = "build";

function record(phase, value) {
  writeFileSync(path.join(out, `restart-${phase}.json`), JSON.stringify(value, null, 2));
}

function build(bin, dir, label, pkg = "./cmd/piforged") {
  console.log(`[build] ${label} -> ${bin}`);
  const result = spawnSync("go", ["build", "-o", bin, pkg], {
    cwd: dir,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, CGO_ENABLED: "0" },
  });
  if (result.status !== 0) throw new Error(`${label} build failed: ${result.stderr ?? result.error}`);
  if (!existsSync(bin)) throw new Error(`${label} build produced no binary`);
  return bin;
}

function buildOld(ref, worktreeRoot) {
  const worktree = path.join(worktreeRoot, "old");
  const add = spawnSync("git", ["worktree", "add", "--detach", worktree, ref], {
    cwd: repo,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (add.status !== 0) throw new Error(`git worktree add ${ref}: ${add.stderr}`);
  const bin = path.join(worktreeRoot, process.platform === "win32" ? "piforged-old.exe" : "piforged-old");
  build(bin, path.join(worktree, "backend"), `old (${ref})`);
  return { bin, worktree };
}

// --- fake runner + fake AIO sandbox -----------------------------------------

function startFakeRunner() {
  const state = { gets: 0, record: null };
  const server = createServer((req, res) => {
    const url = new URL(req.url, `http://${HOST}:${RUNNER_PORT}`);
    if (req.method === "GET" && url.pathname.startsWith("/v1/sandboxes/")) {
      state.gets += 1;
      const id = decodeURIComponent(url.pathname.split("/").pop());
      state.record = {
        id,
        state: "READY",
        root_dir: state.rootDir ?? "",
        container_path_root: "/workspace",
        endpoint: `http://${HOST}:${SANDBOX_PORT}`,
      };
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify(state.record));
      return;
    }
    if (req.method === "POST" && url.pathname === "/v1/sandboxes") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ id: "sbx_fake", state: "READY", container_path_root: "/workspace", endpoint: `http://${HOST}:${SANDBOX_PORT}` }));
      return;
    }
    res.writeHead(204);
    res.end();
  });
  return new Promise((resolve) => {
    server.listen(RUNNER_PORT, HOST, () => resolve({ server, state }));
  });
}

// The fake sandbox says the session's heartbeat is fresh: that is a remote agent
// that survived the restart, which is exactly the case the old binary got wrong.
function startFakeSandbox() {
  const state = { execs: 0, commands: [] };
  const server = createServer((req, res) => {
    if (req.method !== "POST" || !req.url.startsWith("/v1/shell/exec")) {
      res.writeHead(404);
      res.end();
      return;
    }
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      state.execs += 1;
      try {
        state.commands.push(JSON.parse(body).command ?? "");
      } catch {
        state.commands.push("");
      }
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ data: { output: String(Math.floor(Date.now() / 1000)), exit_code: 0 } }));
    });
  });
  return new Promise((resolve) => {
    server.listen(SANDBOX_PORT, HOST, () => resolve({ server, state }));
  });
}

// --- the app under test -----------------------------------------------------

function startApp(binary, dataDir, extraArgs, logPath) {
  const child = spawn(
    binary,
    ["-addr", `${HOST}:${APP_PORT}`, "-data", dataDir, "-fake-pi", "-dev", ...extraArgs],
    {
      env: {
        ...process.env,
        PIFORGE_SEED_EMAIL: "admin@piforge.local",
        PIFORGE_SEED_PASSWORD: "piforge-admin-123",
        PIFORGE_SEED_USER_EMAIL: USER.email,
        PIFORGE_SEED_USER_PASSWORD: USER.password,
        // The binary lives in a temp dir with no sibling fakepi; name the peer
        // explicitly instead of letting the server try to build one into the
        // data directory.
        PIFORGE_FAKE_PI: process.env.PIFORGE_FAKE_PI ?? "",
        PIFORGE_FAKE_PI_DELAY_MS: "40",
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  const lines = [];
  const capture = (chunk) => {
    const text = chunk.toString();
    for (const line of text.split("\n")) if (line.trim()) lines.push(line.trim());
  };
  child.stdout.on("data", capture);
  child.stderr.on("data", capture);
  return { child, lines, logPath, stop: () => stopTree(child, lines, logPath) };
}

function stopTree(child, lines, logPath) {
  if (!child || child.exitCode !== null) {
    if (logPath) writeFileSync(logPath, lines.join("\n") + "\n");
    return;
  }
  if (process.platform === "win32") {
    spawnSync("taskkill", ["/PID", String(child.pid), "/T", "/F"], { stdio: "ignore" });
  } else {
    try {
      process.kill(-child.pid, "SIGKILL");
    } catch {
      child.kill("SIGKILL");
    }
  }
  if (logPath) writeFileSync(logPath, lines.join("\n") + "\n");
}

async function waitHealthy(ms = 30_000) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`http://${HOST}:${APP_PORT}/healthz`);
      if (res.ok) return true;
    } catch {
      // not up yet
    }
    await sleep(250);
  }
  return false;
}

async function login() {
  const res = await fetch(`http://${HOST}:${APP_PORT}/v1/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(USER),
  });
  if (!res.ok) throw new Error(`login: HTTP ${res.status} ${await res.text()}`);
  const cookies = res.headers.getSetCookie?.() ?? [];
  const jar = cookies.map((c) => c.split(";")[0]).join("; ");
  const csrf = cookies.find((c) => c.startsWith("pf_csrf="))?.split(";")[0]?.split("=")[1] ?? "";
  return { jar, csrf };
}

async function api(jar, csrf, method, url, body) {
  const res = await fetch(`http://${HOST}:${APP_PORT}${url}`, {
    method,
    headers: {
      "Content-Type": "application/json",
      Cookie: jar,
      ...(csrf ? { "X-PF-CSRF": csrf } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let json = null;
  try {
    json = await res.json();
  } catch {
    json = null;
  }
  return { status: res.status, body: json };
}

// sseCollect reads the event stream for a bounded window, so an interrupted
// session's run.settled/platform.error events can be seen without waiting for a
// stream that a kept session would hold open forever.
async function sseCollect(jar, sessionId, ms = 2500) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), ms);
  let text = "";
  try {
    const res = await fetch(`http://${HOST}:${APP_PORT}/v1/sessions/${sessionId}/events?after_seq=0`, {
      headers: { Cookie: jar, Accept: "text/event-stream" },
      signal: controller.signal,
    });
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      text += decoder.decode(value, { stream: true });
      if (text.includes("run.settled")) break;
    }
  } catch {
    // aborted: the collected prefix is what we assert on
  } finally {
    clearTimeout(timer);
  }
  return text;
}

// prepare creates the crash state with *this* tree's binary: one project, one
// session, one run in flight, then the control plane killed mid-run so the row
// stays "running" with nothing serving it.
async function prepare(dataDir, binary) {
  const prep = startApp(binary, dataDir, [], path.join(out, "restart-prepare-boot.log"));
  try {
    if (!(await waitHealthy())) {
      throw new Error(`the preparing server never became healthy:\n${prep.lines.join("\n")}`);
    }
    const { jar, csrf } = await login();
    const stamp = Date.now();
    const project = await api(jar, csrf, "POST", "/v1/projects", {
      name: `probe-liveness-${stamp}`,
      repo_url: "",
      default_branch: "main",
    });
    if (project.status !== 201 && project.status !== 200) throw new Error(`create project: ${project.status}`);
    const projectId = project.body.project.id;
    const session = await api(jar, csrf, "POST", `/v1/projects/${projectId}/sessions`, { title: "remote run" });
    const sessionId = session.body.session.id;
    const send = await api(jar, csrf, "POST", `/v1/sessions/${sessionId}/messages`, {
      client_msg_id: `cm_${stamp}`,
      text: "hold this run open [[hold-run]]",
    });
    if (send.status !== 200) throw new Error(`send: ${send.status} ${JSON.stringify(send.body)}`);
    let running = false;
    for (let i = 0; i < 40; i++) {
      const status = await api(jar, csrf, "GET", `/v1/sessions/${sessionId}`);
      if (status.body?.session?.status === "running") {
        running = true;
        break;
      }
      await sleep(50);
    }
    if (!running) throw new Error("the run never reached running before the crash");
    return { projectId, sessionId, runId: send.body?.run_id ?? null };
  } finally {
    prep.stop();
    await sleep(700);
  }
}

async function restart(binary, dataDir, { fakeRunnerRoot, phase }) {
  const args = [
    "-sandbox", "runner",
    "-sandbox-runner-url", `http://${HOST}:${RUNNER_PORT}`,
    "-sandbox-runner-token", "probe-token",
    "-sandbox-api-key", "probe-key",
  ];
  const logPath = path.join(out, `restart-${phase}-boot.log`);
  const app = startApp(binary, dataDir, args, logPath);
  return { app, logPath };
}

async function main() {
  const work = mkdtempSync(path.join(tmpdir(), "piforge-liveness-"));
  const newBin = build(
    path.join(work, process.platform === "win32" ? "piforged-new.exe" : "piforged-new"),
    backend,
    "new (this tree)",
  );
  const old = buildOld(OLD_REF, work);
  const fakePi = build(
    path.join(work, process.platform === "win32" ? "fakepi.exe" : "fakepi"),
    backend,
    "fake pi peer",
    "./cmd/fakepi",
  );
  process.env.PIFORGE_FAKE_PI = fakePi;

  const runner = await startFakeRunner();
  const sandbox = await startFakeSandbox();
  try {
    for (const [phase, binary] of [
      ["old", old.bin],
      ["new", newBin],
    ]) {
      currentPhase = phase;
      const dataDir = path.join(out, `data-${phase}`);
      rmSync(dataDir, { recursive: true, force: true });
      mkdirSync(dataDir, { recursive: true });
      console.log(`\n=== phase ${phase}: prepare the crash state with the new binary ===`);
      const prepared = await prepare(dataDir, newBin);
      console.log(`[${phase}] prepared`, prepared);

      console.log(`=== phase ${phase}: restart with ${binary} -sandbox runner ===`);
      const started = await restart(binary, dataDir, { phase });
      const healthy = await waitHealthy();
      ok(healthy, `${phase}: the restarted control plane became healthy`);
      let result = { phase, binary, prepared, health: healthy, boot: started.app.lines, checks: {} };
      if (healthy) {
        const { jar } = await login();
        const after = await api(jar, "", "GET", `/v1/sessions/${prepared.sessionId}`);
        const status = after.body?.session?.status;
        const stream = await sseCollect(jar, prepared.sessionId);
        result.checks = {
          session_status: status,
          sandbox_execs: sandbox.state.execs,
          runner_gets: runner.state.gets,
          has_settled: /run\.settled/.test(stream),
          has_platform_error: /platform\.error/.test(stream),
          boot_kept_live: started.app.lines.filter((l) => /kept_live=/.test(l)),
          boot_interrupted: started.app.lines.filter((l) => /interrupted=/.test(l)),
        };
        console.log(`[${phase}] after restart`, JSON.stringify(result.checks));
        if (phase === "old") {
          ok(status === "interrupted", "old binary: the live remote run was marked interrupted (the defect)");
          ok(/interrupted=1/.test(result.checks.boot_interrupted.join(" ")), "old binary: its boot log reports interrupted=1");
          ok(sandbox.state.execs === 0, "old binary: it never asked the sandbox (it has no probe)");
        } else {
          // The fake sandbox is asked once per candidate by the probe.
          const execsBefore = sandbox.state.execs;
          ok(status === "running", "new binary: the live remote run is still running after the restart");
          ok(!result.checks.has_settled && !result.checks.has_platform_error, "new binary: no recovery event was appended");
          ok(/kept_live=1/.test(result.checks.boot_kept_live.join(" ")), "new binary: its boot log reports kept_live=1");
          ok(/unknown=0/.test(started.app.lines.join(" ")), "new binary: the boot log reports unknown=0");
          ok(execsBefore > 0, "new binary: it asked the sandbox for the heartbeat");
          ok(
            started.app.lines.some((l) => /sandbox heartbeat marker/.test(l)),
            "new binary: the startup log names the sandbox marker probe",
          );
        }
      }
      started.app.stop();
      await sleep(500);
      record(phase, result);
    }
  } finally {
    runner.server.close();
    sandbox.server.close();
    if (old.worktree) {
      spawnSync("git", ["worktree", "remove", "--force", old.worktree], { cwd: repo, stdio: "ignore" });
    }
    rmSync(work, { recursive: true, force: true });
  }
  writeFileSync(path.join(out, "restart-findings.json"), JSON.stringify(findings, null, 2));
  const failed = findings.filter((f) => !f.ok);
  console.log(`\n${findings.length - failed.length}/${findings.length} findings ok`);
  if (failed.length) process.exitCode = 1;
}

try {
  await main();
} catch (error) {
  console.error("[probe] failed:", error);
  writeFileSync(path.join(out, "restart-findings.json"), JSON.stringify({ error: String(error?.stack ?? error), findings }, null, 2));
  process.exitCode = 1;
}
