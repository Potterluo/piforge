// Reproduction probe for the interrupt-then-message defect (HANDOFF §2 item 2).
//
//   node e2e/scripts/fix-interrupt-context-probe.mjs            # 5 runs
//   node e2e/scripts/fix-interrupt-context-probe.mjs --runs 1
//
// Against the deployed instance (default :7722). For each run it:
//   1. creates a throwaway project + session,
//   2. subscribes to the session's SSE log and timestamps every frame,
//   3. sends a long request, interrupts it, and immediately sends a distinct
//      follow-up ("Say beta."),
//   4. records the POST answers, the control-plane events (run ids, ordering),
//      the raw Pi entries (what the agent actually holds) and the agent's own
//      session JSONL (what the model was given),
//   5. deletes the project.
//
// The point is to tell three stories apart:
//   (a) the follow-up never reached the agent,
//   (b) the agent received stale context,
//   (c) the UI/event log attributed the previous turn's text to the new turn.
// It prints a verdict per run and writes raw JSON per run under
// e2e/.artifacts/fix-interrupt-context/.
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-interrupt-context");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const CONTAINER = process.env.PIFORGE_CONTAINER ?? "pf-local";
const EMAIL = process.env.PIFORGE_USER_EMAIL ?? "dev@piforge.local";
const PASSWORD = process.env.PIFORGE_USER_PASSWORD ?? "piforge-dev-123";

const args = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : fallback;
};
const RUNS = Number(flag("--runs", "5"));
// The composer decides the delivery based on its own `running` flag: while a run
// is in flight, Enter sends streaming_behavior=followUp. "auto" reproduces that
// decision by asking the server for the status right before sending; "none" is
// the same sequence through a client that knows the stop already happened.
const BEHAVIOR = flag("--behavior", "auto");
const WAIT_TEXT_MS = Number(flag("--after-text-ms", "400"));
// Extra pause between the interrupt answer and the follow-up. 0 is the reported
// "immediately"; a positive value is the control case ("the next message after a
// pause answered correctly").
const FOLLOW_DELAY_MS = Number(flag("--follow-delay-ms", "0"));
// Whether the probe asks the server for the status before sending, the way the
// composer's own `running` flag would answer that question. Off = send blind.
const ASK_STATUS = !args.includes("--no-status");
// Fire the stop and the follow-up together: the client did not wait for the
// stop's answer (the composer does not either — its stop button fires a voided
// promise, and the send button stays live while it runs).
const CONCURRENT = args.includes("--concurrent");
// "message" (default) sends the distinct follow-up; "continue" presses the
// interruption marker's 继续 action instead.
const MODE = flag("--mode", "message");
// --note prepends an interruption sentence to the follow-up text, the way a
// server-composed prompt (review comment / continue) already does.
const NOTE = args.includes("--note");
const NOTE_TEXT = flag(
  "--note-text",
  "[The previous turn was interrupted by the user. Its partial output is not a completed answer: do not continue it, and answer only the message below.]",
);
const FOLLOW_TEXT = NOTE ? `${NOTE_TEXT}\n\nSay beta.` : "Say beta.";
const tag = `intctx-${Date.now()}`;
const t0 = Date.now();
const now = () => Date.now() - t0;

// --- tiny cookie-aware HTTP client -----------------------------------------
const jar = new Map();
let csrf = "";

function absorb(res) {
  for (const raw of res.headers.getSetCookie?.() ?? []) {
    const [pair] = raw.split(";");
    const i = pair.indexOf("=");
    if (i > 0) jar.set(pair.slice(0, i), pair.slice(i + 1));
  }
  if (jar.get("pf_csrf")) csrf = jar.get("pf_csrf");
}
const cookieHeader = () => [...jar].map(([k, v]) => `${k}=${v}`).join("; ");

async function call(method, url, body) {
  const res = await fetch(BASE + url, {
    method,
    headers: {
      cookie: cookieHeader(),
      "X-PF-CSRF": csrf,
      ...(body !== undefined ? { "content-type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  absorb(res);
  const text = await res.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    /* not json */
  }
  return { status: res.status, body: json, raw: json ? undefined : text.slice(0, 500) };
}

// --- SSE recorder -----------------------------------------------------------
function subscribe(sessionId, sink) {
  const controller = new AbortController();
  const started = Date.now() - t0;
  const done = (async () => {
    const res = await fetch(`${BASE}/v1/sessions/${sessionId}/events`, {
      headers: { cookie: cookieHeader(), accept: "text/event-stream" },
      signal: controller.signal,
    });
    if (res.status !== 200) {
      sink.push({ at: Date.now() - t0, kind: "sse_error", status: res.status });
      return;
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done: eof } = await reader.read();
      if (eof) break;
      buf += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf("\n\n")) >= 0) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        const lines = frame.split("\n");
        const evLine = lines.find((l) => l.startsWith("event: "));
        const dataLine = lines.find((l) => l.startsWith("data: "));
        if (!evLine || !dataLine) continue;
        let data = null;
        try {
          data = JSON.parse(dataLine.slice(6));
        } catch {
          continue;
        }
        sink.push({ at: Date.now() - t0, event: evLine.slice(7), data });
      }
    }
  })().catch((err) => {
    if (err?.name !== "AbortError") sink.push({ at: Date.now() - t0, kind: "sse_failure", message: String(err) });
  });
  return {
    started,
    stop: () => {
      controller.abort();
      return done;
    },
  };
}

// --- the agent's own file ---------------------------------------------------
function readSessionFile(sessionId) {
  const file = `/tmp/.piforge/sessions/${sessionId}.jsonl`;
  try {
    const raw = execFileSync("docker", ["exec", CONTAINER, "cat", file], { encoding: "utf8" });
    return { file, ok: true, lines: raw.split("\n").filter(Boolean).map((l) => JSON.parse(l)) };
  } catch (err) {
    return { file, ok: false, error: String(err?.message ?? err).slice(0, 300) };
  }
}

function spokenText(entry) {
  const msg = entry.message ?? {};
  const content = msg.content;
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((c) => c?.type === "text")
    .map((c) => c.text ?? "")
    .join("");
}

async function waitIdle(sessionId, { from, timeout = 90_000 }) {
  const started = Date.now();
  let lastSeq = -1;
  let stable = 0;
  while (Date.now() - started < timeout) {
    const st = await call("GET", `/v1/sessions/${sessionId}/status`);
    const last = st.body?.last_seq ?? 0;
    if (!st.body?.running && !st.body?.streaming) {
      // one more poll at the same seq: the terminal event may still be in flight
      if (last === lastSeq) stable += 1;
      else stable = 0;
      lastSeq = last;
      if (stable >= 1) return { idle: true, at: Date.now() - t0, ms: Date.now() - from, status: st.body };
    } else {
      stable = 0;
      lastSeq = last;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  return { idle: false, at: Date.now() - t0, ms: Date.now() - started };
}

const results = [];

async function oneRun(i) {
  const runOut = { index: i, started_at: now() };
  const project = await call("POST", "/v1/projects", {
    name: `${tag}-${i}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = project.body?.project?.id;
  runOut.project = projectId;
  const created = await call("POST", `/v1/projects/${projectId}/sessions`, { title: `${tag}-${i}` });
  const sessionId = created.body?.session?.id;
  runOut.session = sessionId;

  const sse = [];
  const sub = subscribe(sessionId, sse);
  await new Promise((r) => setTimeout(r, 300));

  // 1. the long request
  const long = `Count from 1 to 200. One number per line, no other text. Run ${i}.`;
  const sentLongAt = now();
  const longRes = await call("POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `${tag}-${i}-a`,
    text: long,
  });
  runOut.longPost = { at: sentLongAt, status: longRes.status, body: longRes.body };

  // Wait until the model is demonstrably streaming *prose* (the reported case:
  // a long answer being written when the user stops it). A run that has not
  // started cannot be interrupted in the way the report describes.
  let sawDelta = false;
  let sawText = false;
  const streamDeadline = Date.now() + 60_000;
  while (Date.now() < streamDeadline) {
    sawDelta ||= sse.some((e) => e.event === "message.delta");
    sawText ||= sse.some(
      (e) => e.event === "message.delta" && (e.data?.payload?.kind === "text" || e.data?.payload?.kind === "thinking"),
    );
    if (sawText) break;
    if (sse.some((e) => e.event === "run.settled")) break;
    await new Promise((r) => setTimeout(r, 25));
  }
  runOut.sawStreaming = sawDelta;
  runOut.sawText = sawText;
  await new Promise((r) => setTimeout(r, WAIT_TEXT_MS));

  // 2. the interrupt
  const interruptedAt = now();
  let intRes;
  let followRes;
  let sentFollowAt = interruptedAt;
  if (CONCURRENT) {
    // The stop is in flight when the follow-up is written: this is the window a
    // user lands in whenever the stop takes longer than the typing does.
    const followText = {
      client_msg_id: `${tag}-${i}-b`,
      text: FOLLOW_TEXT,
      streaming_behavior: "follow_up",
    };
    const both = Promise.all([
      call("POST", `/v1/sessions/${sessionId}/interrupt`, {}),
      call("POST", `/v1/sessions/${sessionId}/messages`, followText),
    ]);
    const [iRes, fRes] = await both;
    intRes = iRes;
    followRes = fRes;
    runOut.concurrent = true;
  } else {
    intRes = await call("POST", `/v1/sessions/${sessionId}/interrupt`, {});
  }
  runOut.interrupt = { at: interruptedAt, status: intRes.status, body: intRes.body };

  if (MODE === "continue") {
    // The 继续 action on the interruption marker: the server composes the text.
    runOut.followBehavior = "continue_interrupted";
    await new Promise((r) => setTimeout(r, 300));
    sentFollowAt = now();
    followRes = await call("POST", `/v1/sessions/${sessionId}/messages`, {
      client_msg_id: `${tag}-${i}-b`,
      continue_interrupted: true,
    });
  } else if (!CONCURRENT) {
    // 3. the distinct follow-up, with no pause at all
    if (FOLLOW_DELAY_MS > 0) await new Promise((r) => setTimeout(r, FOLLOW_DELAY_MS));
    let behavior = "";
    let statusAtFollow = null;
    if (ASK_STATUS) {
      statusAtFollow = await call("GET", `/v1/sessions/${sessionId}/status`);
      runOut.statusAtFollow = statusAtFollow.body;
    }
    if (BEHAVIOR === "follow_up") behavior = "follow_up";
    else if (
      BEHAVIOR === "auto" &&
      (statusAtFollow?.body?.running || statusAtFollow?.body?.streaming)
    ) {
      behavior = "follow_up";
    }
    runOut.followBehavior = behavior;
    sentFollowAt = now();
    followRes = await call("POST", `/v1/sessions/${sessionId}/messages`, {
      client_msg_id: `${tag}-${i}-b`,
      text: FOLLOW_TEXT,
      ...(behavior ? { streaming_behavior: behavior } : {}),
    });
  } else if (MODE === "continue") {
    // The 继续 action on the interruption marker: the server composes the text.
    runOut.followBehavior = "continue_interrupted";
    await new Promise((r) => setTimeout(r, 300));
    sentFollowAt = now();
    followRes = await call("POST", `/v1/sessions/${sessionId}/messages`, {
      client_msg_id: `${tag}-${i}-b`,
      continue_interrupted: true,
    });
  } else {
    runOut.followBehavior = "follow_up";
  }
  runOut.followPost = { at: sentFollowAt, status: followRes.status, body: followRes.body };

  await new Promise((r) => setTimeout(r, 1200));
  const idle = await waitIdle(sessionId, { from: Date.now() });
  runOut.idle = idle;
  runOut.settleLatencyMs = idle.at - sentFollowAt;

  await new Promise((r) => setTimeout(r, 700));
  await sub.stop();

  runOut.events = sse;
  runOut.entries = (await call("GET", `/v1/sessions/${sessionId}/entries`)).body;
  const file = readSessionFile(sessionId);
  runOut.agentFile = file.ok
    ? {
        path: file.file,
        entries: file.lines
          .filter((l) => l.type === "message")
          .map((l) => ({
            id: l.id,
            parentId: l.parentId,
            role: l.message?.role,
            text: spokenText(l).slice(0, 400),
            stopReason: l.message?.stopReason,
            error: l.message?.errorMessage,
          })),
      }
    : file;
  runOut.finished_at = now();

  // --- verdict --------------------------------------------------------------
  const seqd = [...sse].filter((e) => e.data?.seq).sort((a, b) => a.data.seq - b.data.seq);
  const types = seqd.map((e) => e.event).filter(Boolean);
  const runStarts = seqd.filter((e) => e.event === "run.started").map((e) => e.data?.run_id);
  const settles = seqd.filter((e) => e.event === "run.settled").map((e) => ({
    at: e.at,
    run_id: e.data?.run_id,
    payload: e.data?.payload,
  }));
  const agentUser = (runOut.agentFile.entries ?? []).filter((e) => e.role === "user");
  const betaInAgent = agentUser.some((e) => /beta/i.test(e.text));
  const betaInEntries = (runOut.entries?.entries ?? []).some(
    (e) => e.role === "user" && /beta/i.test(e.preview ?? ""),
  );
  const betaAccepted = followRes.status === 200;

  // The assistant text the UI would render last, in event order.
  const finalTexts = seqd
    .filter((e) => e.event === "message.final" && e.data?.payload?.role === "assistant")
    .map((e) => ({
      seq: e.data.seq,
      run_id: e.data.run_id,
      at: e.at,
      text: String(e.data.payload.text ?? "").slice(0, 800),
      thinking: String(e.data.payload.thinking ?? "").slice(0, 400),
    }));
  // The assistant turn that follows the "Say beta." user message: that is what
  // the user reads as the answer to it.
  const betaUserEvent = seqd.find(
    (e) =>
      e.event === "message.final" &&
      e.data?.payload?.role === "user" &&
      /say beta/i.test(String(e.data.payload.text ?? "")),
  );
  const answerAfterBeta = betaUserEvent
    ? finalTexts.find((f) => f.seq > betaUserEvent.data.seq)
    : undefined;
  const answerMentionsCounting = /count|\b1\b.*\b2\b.*\b3\b|\b200\b/i.test(
    `${answerAfterBeta?.text ?? ""} ${answerAfterBeta?.thinking ?? ""}`,
  );

  runOut.verdict = {
    eventTypes: types,
    runStarts,
    settles,
    betaAccepted,
    betaBehavior: runOut.followBehavior,
    betaDisposition: followRes.body?.disposition ?? null,
    betaRunId: followRes.body?.run_id ?? null,
    betaInAgentFile: betaInAgent,
    betaInEntries,
    betaTurnRendered: Boolean(betaUserEvent),
    agentFileUsers: agentUser.map((e) => e.text.slice(0, 120)),
    assistantFinals: finalTexts,
    answerAfterBeta,
    answerMentionsCounting,
    settleLatencyMs: runOut.settleLatencyMs ?? null,
  };

  writeFileSync(path.join(out, `run-${i}.json`), JSON.stringify(runOut, null, 2));

  // --- cleanup --------------------------------------------------------------
  const delSession = await call("DELETE", `/v1/sessions/${sessionId}`);
  const delProject = await call("DELETE", `/v1/projects/${projectId}`);
  runOut.cleanup = { session: delSession.status, project: delProject.status };
  writeFileSync(path.join(out, `run-${i}.json`), JSON.stringify(runOut, null, 2));

  console.log(
    `[run ${i}] accepted=${betaAccepted} disposition=${runOut.verdict.betaDisposition} ` +
      `behavior=${runOut.followBehavior || "none"} run_starts=${runStarts.length} ` +
      `settles=${settles.map((s) => `${s.payload?.aborted ? "aborted" : "ok"}@${s.at}ms`).join(",")} ` +
      `beta_in_agent=${betaInAgent} settle=${runOut.settleLatencyMs}ms ` +
      `beta_answer_matches_new=${runOut.verdict.answerAfterBeta ? !runOut.verdict.answerMentionsCounting : "none"}`,
  );
  const betaFinal = finalTexts.at(-1);
  if (betaFinal) console.log(`[run ${i}] last assistant text: ${JSON.stringify(betaFinal.text.slice(0, 160))}`);
  return runOut;
}

// --- main -------------------------------------------------------------------
const login = await call("POST", "/v1/auth/login", { email: EMAIL, password: PASSWORD });
if (login.status !== 200) {
  console.error("login failed", login.status, login.body ?? login.raw);
  process.exit(1);
}
console.log(`[probe] ${BASE} as ${EMAIL}; ${RUNS} run(s), tag ${tag}`);

for (let i = 1; i <= RUNS; i += 1) {
  try {
    results.push(await oneRun(i));
  } catch (err) {
    console.error(`[run ${i}] probe failed:`, err);
    results.push({ index: i, error: String(err?.stack ?? err) });
  }
  await new Promise((r) => setTimeout(r, 500));
}

writeFileSync(path.join(out, "summary.json"), JSON.stringify({ base: BASE, tag, runs: results }, null, 2));

// A short, readable verdict table for the report.
const lines = [
  `# interrupt-then-message probe — ${tag}`,
  `base: ${BASE}`,
  `behavior: ${BEHAVIOR} (auto = follow_up whenever the server still says running, as the composer does)`,
  "",
  "run | beta accepted | behavior | disposition | run.started | settles | beta reached agent | beta answer about the new request | settle ms | rendered answer",
  "--- | --- | --- | --- | --- | --- | --- | --- | --- | ---",
];
for (const r of results) {
  if (r.error) {
    lines.push(`${r.index} | ERROR | | | | | | | | ${r.error.slice(0, 120)}`);
    continue;
  }
  const v = r.verdict;
  const answer = v.answerAfterBeta
    ? JSON.stringify(`${v.answerAfterBeta.text} ${v.answerAfterBeta.thinking}`.trim().slice(0, 160))
    : "（no assistant turn after it）";
  lines.push(
    `${r.index} | ${v.betaAccepted} | ${v.betaBehavior || "none"} | ${v.betaDisposition} | ` +
      `${v.runStarts.length} | ${v.settles.map((s) => `${s.payload?.aborted ? "aborted" : "ok"}@${s.at}ms`).join("<br>")} | ` +
      `${v.betaInAgentFile} | ${v.answerAfterBeta ? !v.answerMentionsCounting : "n/a"} | ${v.settleLatencyMs} | ${answer}`,
  );
}
writeFileSync(path.join(out, "summary.md"), lines.join("\n") + "\n");
console.log(lines.join("\n"));
