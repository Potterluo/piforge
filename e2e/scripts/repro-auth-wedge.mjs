// Reproduction for the "signed out after a few minutes" wedge.
//
// Symptom (seen in three of five full e2e runs): after 4-7 minutes of load,
// `GET /v1/auth/me` answers 401 for a session cookie whose token row is
// present in the database, and restarting piforged on the same data directory
// makes the same cookie work again. This script drives the same shape of load
// without Playwright -- many streaming/list reads that are aborted mid-flight,
// plus a login + /me probe every 200ms -- and stops at the first 401.
//
// Usage: node e2e/scripts/repro-auth-wedge.mjs [--base http://127.0.0.1:8099] [--seconds 240]
//
// It requires a running piforged (`-dev` seeds dev@piforge.local). Every login
// it makes is a fresh token, so a 401 on a token it just received is the bug,
// not an expired session.
import { setTimeout as sleep } from "node:timers/promises";

const args = new Map();
for (let i = 2; i < process.argv.length; i += 2) {
  args.set(process.argv[i], process.argv[i + 1]);
}
const base = args.get("--base") ?? "http://127.0.0.1:8099";
const seconds = Number(args.get("--seconds") ?? 240);
const email = args.get("--email") ?? "dev@piforge.local";
const password = args.get("--password") ?? "piforge-dev-123";

const startedAt = Date.now();
const elapsed = () => ((Date.now() - startedAt) / 1000).toFixed(1);
const log = (...parts) => console.log(`[${elapsed()}s]`, ...parts);

async function login() {
  const res = await fetch(`${base}/v1/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  if (!res.ok) throw new Error(`login ${res.status}`);
  const pairs = res.headers.getSetCookie().map((line) => line.split(";")[0]);
  const cookie = pairs.join("; ");
  const body = await res.json();
  return { cookie, csrf: body.csrf, user: body.user };
}

/** One aborted read: the server starts a query and the client goes away. */
async function aborted(path, headerCookie, afterMs = 20) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), afterMs);
  try {
    await fetch(`${base}${path}`, {
      headers: { cookie: headerCookie },
      signal: controller.signal,
    });
    await Promise.resolve();
  } catch {
    /* the abort is the point */
  } finally {
    clearTimeout(timer);
  }
}

async function main() {
  const who = await login();
  const cookie = who.cookie;
  const csrf = who.csrf;
  log("signed in", who.user.email);

  // A project and a session to stream from. The project is throwaway.
  const projectRes = await fetch(`${base}/v1/projects`, {
    method: "POST",
    headers: { "content-type": "application/json", cookie, "x-pf-csrf": csrf },
    body: JSON.stringify({ name: `repro-${Date.now()}`, repo_url: "", default_branch: "main" }),
  });
  const project = (await projectRes.json()).project;
  if (!project) throw new Error(`create project: ${projectRes.status}`);
  const sessionRes = await fetch(`${base}/v1/projects/${project.id}/sessions`, {
    method: "POST",
    headers: { "content-type": "application/json", cookie, "x-pf-csrf": csrf },
    body: JSON.stringify({ title: "repro" }),
  });
  const session = (await sessionRes.json()).session;
  log("session", session.id);

  let stopped = false;
  // A short pause between batches keeps the loop at a rate the loopback port
  // proxy survives. At 0 the harness can saturate a Docker Desktop port forward
  // on this machine (measured: ~470% CPU in the container, then connect
  // timeouts), which ends the probe long before its deadline -- that is a
  // property of the harness, not a finding about the server.
  const abortPause = Number(args.get("--abort-pause") ?? 0);
  const abortLoop = (async () => {
    while (!stopped) {
      await Promise.all([
        aborted(`/v1/sessions/${session.id}/events?after_seq=0`, cookie, 15),
        aborted(`/v1/sessions/${session.id}/files/tree?path=&depth=12`, cookie, 8),
        aborted(`/v1/sessions/${session.id}/git/status`, cookie, 5),
        aborted(`/v1/sessions/${session.id}/git/log?limit=200`, cookie, 5),
        aborted(`/v1/projects`, cookie, 5),
      ]);
      if (abortPause > 0) await sleep(abortPause);
    }
  })();

  let probes = 0;
  // Beyond auth: the project created above is committed, so every list read
  // that is served by a frozen snapshot would hide it. Counting that turns "is
  // more than the auth lookup affected?" into a measurement instead of a guess.
  let projectReads = 0;
  let projectMisses = 0;
  async function projectVisible(cookie) {
    projectReads += 1;
    try {
      const res = await fetch(`${base}/v1/projects`, { headers: { cookie } });
      if (!res.ok) return true; // an error is not this question's business
      const body = await res.json();
      return (body.projects ?? []).some((row) => row.id === project.id);
    } catch {
      return true;
    }
  }

  let netErrors = 0;
  let consecutive = 0;
  while (!stopped) {
    // The load above is deliberately abusive: thousands of sockets a second on a
    // loopback port proxy. A connect timeout is therefore a property of the
    // harness, not a finding about the server, so it is counted and retried
    // instead of ending the run (an unhandled rejection here used to stop the
    // probe after ~2 minutes).
    let fresh;
    try {
      fresh = await login();
    } catch (error) {
      netErrors += 1;
      consecutive += 1;
      if (consecutive <= 5 || consecutive % 25 === 0) {
        log(`probe #${probes}: login failed (${error?.cause?.code ?? error?.message}); retrying`);
      }
      if (consecutive > 400) {
        stopped = true;
        log(`giving up after ${netErrors} network errors`);
        console.log(JSON.stringify({ wedged: false, seconds: Number(elapsed()), probes, netErrors }, null, 2));
        break;
      }
      await sleep(500);
      continue;
    }
    consecutive = 0;
    probes += 1;
    const me = await fetch(`${base}/v1/auth/me`, { headers: { cookie: fresh.cookie } });
    if (me.status === 401) {
      stopped = true;
      const body = await me.text();
      log(`WEDGE at probe #${probes}: /v1/auth/me -> 401 ${body}`);
      console.log(
        JSON.stringify({ wedged: true, seconds: Number(elapsed()), probes, projectReads, projectMisses, netErrors }, null, 2),
      );
      process.exitCode = 2;
      break;
    }
    if (!(await projectVisible(fresh.cookie))) {
      projectMisses += 1;
      if (projectMisses <= 5) log(`probe #${probes}: /v1/projects does not list project ${project.id}`);
    }
    if (probes % 25 === 0) {
      log(`probe #${probes}: /v1/auth/me 200 · projects listed ${projectReads - projectMisses}/${projectReads}`);
    }
    if (elapsed() > seconds) {
      stopped = true;
      log(`no wedge after ${seconds}s / ${probes} probes (project list missed ${projectMisses}/${projectReads})`);
      console.log(
        JSON.stringify({ wedged: false, seconds: Number(elapsed()), probes, projectReads, projectMisses }, null, 2),
      );
      break;
    }
    await sleep(200);
  }
  stopped = true;
  await abortLoop.catch(() => {});
}

await main();
