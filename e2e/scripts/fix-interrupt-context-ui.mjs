// UI reproduction of "interrupt, then immediately send": what a person sees.
//
//   node e2e/scripts/fix-interrupt-context-ui.mjs [--runs 1]
//
// It drives the deployed app (default :7722) in a real Chrome, performs the
// reported sequence with no artificial pauses — send a long request, press 停止,
// type "Say beta." and press Enter at once — and records:
//   - what the composer offered to do at that moment (发送 vs 下一轮执行),
//   - the toasts, the run-state transitions, and the transcript as rendered,
//   - the raw Pi entries, the agent's session JSONL and the event log afterwards,
// so the answer can be matched to the turn it was rendered under.
import { chromium } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-interrupt-context");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const CONTAINER = process.env.PIFORGE_CONTAINER ?? "pf-local";
const args = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : fallback;
};
const RUNS = Number(flag("--runs", "1"));
const tag = `intctx-ui-${Date.now()}`;
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
    /* empty */
  }
  return { status: res.status(), body };
}

function agentFile(sessionId) {
  try {
    const raw = execFileSync(
      "docker",
      ["exec", CONTAINER, "cat", `/tmp/.piforge/sessions/${sessionId}.jsonl`],
      { encoding: "utf8" },
    );
    return raw
      .split("\n")
      .filter(Boolean)
      .map((l) => JSON.parse(l))
      .filter((l) => l.type === "message")
      .map((l) => {
        const content = l.message?.content;
        const text = Array.isArray(content)
          ? content.filter((c) => c?.type === "text").map((c) => c.text ?? "").join("")
          : typeof content === "string"
            ? content
            : "";
        return { role: l.message?.role, text, stopReason: l.message?.stopReason };
      });
  } catch (err) {
    return { error: String(err?.message ?? err).slice(0, 300) };
  }
}

async function eventsOf(cookieHeader, sessionId) {
  // The events endpoint is a live SSE stream: read it for a bounded window and
  // parse the frames, then hang up.
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 1_500);
  let text = "";
  try {
    const res = await fetch(`${BASE}/v1/sessions/${sessionId}/events`, {
      headers: { cookie: cookieHeader, accept: "text/event-stream" },
      signal: controller.signal,
    });
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      text += decoder.decode(value, { stream: true });
    }
  } catch {
    /* aborted: what arrived is what we keep */
  } finally {
    clearTimeout(timer);
  }
  return text
    .split("\n\n")
    .map((frame) => frame.split("\n").find((l) => l.startsWith("data: ")))
    .filter(Boolean)
    .map((l) => JSON.parse(l.slice(6)))
    .map((e) => ({ seq: e.seq, ts: e.ts, type: e.type, run_id: e.run_id, payload: e.payload }));
}

const browser = await chromium.launch({ channel: "chrome" });
const results = [];

for (let run = 1; run <= RUNS; run += 1) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const log = [];
  const mark = (what, extra = {}) => log.push({ at: Date.now(), what, ...extra });

  try {
    await page.goto(`${BASE}/login`);
    await page.getByTestId("login-email").fill("dev@piforge.local");
    await page.getByTestId("login-password").fill("piforge-dev-123");
    await page.getByTestId("login-submit").click();
    await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });

    const project = await api(page, "POST", "/v1/projects", {
      name: `${tag}-${run}`,
      repo_url: "",
      default_branch: "main",
    });
    const projectId = project.body.project.id;
    const created = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
      title: `${tag}-${run}`,
    });
    const sessionId = created.body.session.id;

    await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
    await page.locator(`[data-session-id="${sessionId}"]`).waitFor({ timeout: 40_000 });
    await page.getByTestId("composer-input").waitFor({ timeout: 20_000 });

    // 1. the long request
    await page.getByTestId("composer-input").fill(
      "Count from 1 to 200. One number per line, no other text.",
    );
    const sentLongAt = Date.now();
    await page.getByTestId("composer-send").click();
    mark("sent long", { at: sentLongAt });

    // Wait for the run to be visibly streaming, then let it write for a couple
    // of seconds: that is the moment a human presses 停止.
    await page.getByTestId("composer-stop").waitFor({ timeout: 30_000 });
    await page.waitForTimeout(2_500);

    // 2. the stop
    const stopAt = Date.now();
    await page.getByTestId("composer-stop").click();
    mark("clicked stop", { at: stopAt });

    // 3. the follow-up, immediately, the way the report describes it: type and
    // Enter with no pause. Record which affordance the composer offered.
    const sendLabel = await page.getByTestId("composer-send").innerText().catch(() => "");
    const sendTestId = await page
      .getByTestId("composer-send")
      .evaluate((el) => el.getAttribute("title"))
      .catch(() => "");
    mark("send affordance after stop", { label: sendLabel, title: sendTestId });

    await page.getByTestId("composer-input").fill("Say beta.");
    const sentFollowAt = Date.now();
    await page.getByTestId("composer-send").click();
    mark("sent follow-up", { at: sentFollowAt });

    // What the composer says it did, and what the transcript looks like as it
    // arrives. Poll every 150 ms for 12 s.
    const polls = [];
    const startedAt = Date.now();
    while (Date.now() - startedAt < 12_000) {
      polls.push({
        at: Date.now(),
        runState: await page
          .getByTestId("run-state")
          .first()
          .getAttribute("data-state")
          .catch(() => null),
        userTurns: await page.getByTestId("turn-user").count(),
        assistantTurns: await page.getByTestId("turn-assistant").count(),
        toasts: await page
          .locator("[data-sonner-toast]")
          .allInnerTexts()
          .catch(() => []),
        stopVisible: await page.getByTestId("composer-stop").isVisible().catch(() => false),
      });
      await page.waitForTimeout(150);
    }
    // Give the model time to finish if it is still going.
    await page
      .waitForFunction(
        () =>
          document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") ===
          "settled",
        undefined,
        { timeout: 60_000 },
      )
      .catch(() => mark("never settled"));

    const transcript = await page.getByTestId("session-stream").innerText().catch(() => "");
    const finalUserTurns = await page.getByTestId("turn-user").allInnerTexts().catch(() => []);
    const finalAssistantTurns = await page
      .getByTestId("turn-assistant")
      .allInnerTexts()
      .catch(() => []);

    const entries = await api(page, "GET", `/v1/sessions/${sessionId}/entries`);
    const cookies = await page.context().cookies();
    const events = await eventsOf(
      cookies.map((c) => `${c.name}=${c.value}`).join("; "),
      sessionId,
    );
    const file = agentFile(sessionId);

    results.push({
      run,
      sessionId,
      projectId,
      log,
      polls,
      transcript,
      finalUserTurns,
      finalAssistantTurns,
      entries: entries.body,
      events,
      agentFile: file,
    });
    writeFileSync(path.join(out, `ui-run-${run}.json`), JSON.stringify(results.at(-1), null, 2));

    console.log(`[ui ${run}] send affordance after stop: ${JSON.stringify(sendLabel)} / ${sendTestId}`);
    console.log(`[ui ${run}] user turns: ${finalUserTurns.length}, assistant turns: ${finalAssistantTurns.length}`);
    console.log(`[ui ${run}] transcript tail:\n${transcript.slice(-800)}`);

    await api(page, "DELETE", `/v1/sessions/${sessionId}`);
    await api(page, "DELETE", `/v1/projects/${projectId}`);
  } catch (err) {
    console.error(`[ui ${run}] failed:`, err);
    results.push({ run, error: String(err?.stack ?? err) });
    writeFileSync(path.join(out, `ui-run-${run}.json`), JSON.stringify(results.at(-1), null, 2));
  } finally {
    await page.close();
  }
}

writeFileSync(path.join(out, "ui-summary.json"), JSON.stringify({ base: BASE, tag, results }, null, 2));
await browser.close();
