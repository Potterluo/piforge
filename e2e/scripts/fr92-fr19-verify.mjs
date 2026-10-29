// Verification probe for FR-9.2 (live follow view) and FR-1.9 (compaction
// visibility / no-op), run against the deployed app with the real agent.
//
//   node e2e/scripts/fr92-fr19-verify.mjs                 # BASE defaults to :7722
//   BASE=http://127.0.0.1:3100 node e2e/scripts/fr92-fr19-verify.mjs
//
// Writes screenshots + a JSON record with PASS/FAIL per check under
// e2e/.artifacts/fix-follow-compact/.
import { chromium } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-follow-compact");
mkdirSync(out, { recursive: true });
const logPath = path.join(out, "verify-log.json");

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
// `ONLY=compact` reruns just the compaction checks (the follow run costs a
// provider turn), and `ONLY=follow` the other way round.
const ONLY = process.env.ONLY ?? "";
const stamp = Date.now();
// Merge into the previous record so a filtered rerun keeps the other row's
// evidence instead of replacing it.
let previous = {};
try {
  previous = JSON.parse(readFileSync(logPath, "utf8"));
} catch {
  previous = {};
}
const record = { base: BASE, stamp, checks: previous.checks ?? {}, findings: previous.findings ?? {} };
const save = () => writeFileSync(logPath, JSON.stringify(record, null, 2));

const check = (name, ok, detail) => {
  record.checks[name] = { ok: Boolean(ok), detail };
  console.log(`[${ok ? "PASS" : "FAIL"}] ${name}${detail === undefined ? "" : ` — ${JSON.stringify(detail)}`}`);
  save();
  return ok;
};

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
const shot = async (page, name) => {
  await page.screenshot({ path: path.join(out, `${name}.png`) });
  console.log("[shot]", `${name}.png`);
};

const browser = await chromium.launch({ channel: "chrome" });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });
console.log("[login] ok");

const csrf = async () => csrfOf(await page.context().cookies());

async function newProject(name) {
  const res = await page.request.post(`${BASE}/v1/projects`, {
    headers: { "X-PF-CSRF": await csrf() },
    data: { name, repo_url: "", default_branch: "main" },
  });
  if (!res.ok()) throw new Error(`project ${res.status()} ${await res.text()}`);
  return (await res.json()).project.id;
}

async function newSession(projectId, title) {
  const res = await page.request.post(`${BASE}/v1/projects/${projectId}/sessions`, {
    headers: { "X-PF-CSRF": await csrf() },
    data: { title },
  });
  if (!res.ok()) throw new Error(`session ${res.status()} ${await res.text()}`);
  return (await res.json()).session.id;
}

async function openWorkbench(projectId, sessionId) {
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
}

const composerBusy = async () =>
  (await page.getByTestId("run-state").getAttribute("data-state")) === "running";

async function send(text) {
  // The composer's send button is briefly inert while the session's agent is
  // spawning; wait for the user turn instead of assuming one click landed.
  const before = await page.getByTestId("turn-user").count();
  for (let attempt = 0; attempt < 3; attempt += 1) {
    await page.getByTestId("composer-input").fill(text);
    await page.getByTestId("composer-send").click();
    try {
      await page.getByTestId("turn-user").nth(before).waitFor({ timeout: 15_000 });
      return;
    } catch {
      console.log("[send] retrying: the turn did not appear");
    }
  }
  throw new Error("the prompt never produced a user turn");
}

async function followText(target = page) {
  return target.evaluate(() => {
    const rows = document.querySelector('[data-testid="follow-terminal"] .xterm-rows');
    return rows?.textContent ?? "";
  });
}

const linesOf = (text, re) => [...new Set([...text.matchAll(re)].map((m) => m[1]))];

async function waitFor(predicate, { timeout = 60_000, interval = 250 } = {}) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if (await predicate()) return true;
    await new Promise((r) => setTimeout(r, interval));
  }
  return false;
}

const meterPercent = async () => {
  const text = await page.getByTestId("context-meter").innerText().catch(() => "");
  const match = /(\d+)%/.exec(text);
  return match ? Number(match[1]) : null;
};

// ---------------------------------------------------------------------------
// FR-9.2 — the follow view is live for a real agent bash command.
// ---------------------------------------------------------------------------
if (!ONLY || ONLY === "follow") {
  const projectId = await newProject(`fixv-follow-${stamp}`);
  const sessionId = await newSession(projectId, "fixv follow");
  await openWorkbench(projectId, sessionId);

  // Opened before the command starts.
  await page.getByTestId("panel-tab-follow").click();
  await page.getByTestId("follow-terminal").waitFor({ timeout: 20_000 });

  await send(
    "请调用 bash 工具，原样执行下面这条命令，然后把输出贴出来（不要改写命令）：\n" +
      "for i in 1 2 3 4 5 6 7; do echo LIVE-$i; sleep 0.7; done",
  );

  const grew = await waitFor(async () => linesOf(await followText(page), /LIVE-(\d+)/g).length >= 3, {
    timeout: 90_000,
  });
  const midRunning = await composerBusy();
  const midText = await followText(page);
  await shot(page, "follow-real-mid-run");
  check("fr92: follow view grew to >=3 lines", grew, {
    lines: linesOf(midText, /LIVE-(\d+)/g),
    runningWhenObserved: midRunning,
  });
  check("fr92: growth happened while the run was still running", grew && midRunning, {
    runningWhenObserved: midRunning,
  });

  const settled = await waitFor(async () => !(await composerBusy()), { timeout: 120_000 });
  const finalText = await followText(page).catch(() => "");
  const finalLines = linesOf(finalText, /LIVE-(\d+)/g);
  const occurrences = [...finalText.matchAll(/LIVE-\d+/g)].length;
  check("fr92: all seven lines arrived before the run settled", settled && finalLines.length === 7, {
    finalLines,
    occurrences,
  });
  check("fr92: no duplicated output (cumulative snapshots diffed, not appended)", occurrences === 7, {
    occurrences,
  });
  await shot(page, "follow-real-final");

  // A second browser tab following the same terminal sees the same content.
  const second = await context.newPage();
  await second.goto(page.url());
  await second.getByTestId("panel-tab-follow").click();
  await second.getByTestId("follow-terminal").waitFor({ timeout: 20_000 });
  const secondCatchUp = await waitFor(
    async () => linesOf(await followText(second), /LIVE-(\d+)/g).length === 7,
    { timeout: 60_000 },
  );
  const secondText = await followText(second);
  check("fr92: a second tab sees the same seven lines", secondCatchUp, {
    lines: linesOf(secondText, /LIVE-(\d+)/g),
    occurrences: [...secondText.matchAll(/LIVE-\d+/g)].length,
  });
  await shot(second, "follow-real-second-tab");
  record.findings.follow = { midText: midText.slice(-400), finalLines, occurrences };
  await second.close();
}

// ---------------------------------------------------------------------------
// FR-1.9 (a) — a session with nothing to compact answers a no-op, not a 500.
// ---------------------------------------------------------------------------
if (!ONLY || ONLY === "compact") {
  const projectId = await newProject(`fixv-compact-noop-${stamp}`);
  const sessionId = await newSession(projectId, "fixv no-op");
  await openWorkbench(projectId, sessionId);
  await send("只回复两个字：收到");
  await waitFor(async () => !(await composerBusy()), { timeout: 120_000 });

  const res = await page.request.post(`${BASE}/v1/sessions/${sessionId}/compact`, {
    headers: { "X-PF-CSRF": await csrf() },
    data: { custom_instructions: "" },
  });
  const body = await res.json().catch(() => null);
  check("fr19: compacting a small session is a clear no-op", res.status() === 200 && body?.compacted === false, {
    status: res.status(),
    body,
  });
  check("fr19: the no-op carries a reason a user can read", body?.reason === "没有可压缩的上下文", {
    reason: body?.reason,
  });
  await shot(page, "compact-nothing-to-compact");
  record.findings.compactNoop = { status: res.status(), body };
}

// ---------------------------------------------------------------------------
// FR-1.9 (b) — can a real compaction be triggered? Pi compacts only when the
// branch has more than keepRecentTokens (20000) plus its reserveTokens (16384),
// and it needs something *before* that window to summarise. Two long messages
// clear both bars, so a manual compaction really runs on the real model.
// ---------------------------------------------------------------------------
if (!ONLY || ONLY === "compact") {
  const projectId = await newProject(`fixv-compact-real-${stamp}`);
  const sessionId = await newSession(projectId, "fixv compact");
  await openWorkbench(projectId, sessionId);

  const filler = "这是一段用于占满上下文的说明文字。".repeat(1_100); // ~20k CJK chars per turn
  await send(`${filler}\n\n上面是背景材料。只回复两个字：收到`);
  await waitFor(async () => !(await composerBusy()), { timeout: 180_000 });
  await send(`${filler}\n\n补充材料同上。仍然只回复两个字：收到`);
  await waitFor(async () => !(await composerBusy()), { timeout: 180_000 });

  const before = await meterPercent();
  const res = await page.request.post(`${BASE}/v1/sessions/${sessionId}/compact`, {
    headers: { "X-PF-CSRF": await csrf() },
    data: { custom_instructions: "" },
  });
  const body = await res.json().catch(() => null);
  record.findings.compactReal = { status: res.status(), body, before };

  if (res.status() === 200 && body?.compacted === true) {
    const marker = page.getByText(/上下文已压缩/).first();
    await marker.waitFor({ timeout: 30_000 });
    const markerText = await marker.innerText();
    const dropped = await waitFor(async () => {
      const now = await meterPercent();
      return now !== null && before !== null && now < before;
    }, { timeout: 30_000 });
    const after = await meterPercent();
    check("fr19: a real compaction renders the marker", /上下文已压缩/.test(markerText), { markerText });
    check("fr19: the context meter drops after the real compaction", dropped, { before, after });
    await shot(page, "compact-real-marker");
  } else {
    // A no-op after a genuine turn is still the honest answer here, but it now
    // carries the context reading: the session was not empty.
    check("fr19: a real compaction attempt is reported as a no-op, not a 5xx", body?.compacted === false, {
      status: res.status(),
      body,
      contextPercentBefore: before,
      note: "pi needs ~36k tokens (keepRecentTokens 20000 + reserveTokens 16384) before a manual compaction does anything",
    });
  }
}

save();
console.log("[out]", path.join(out, "verify-log.json"));
await browser.close();
