// Measure where fast typing loses bytes in the terminal panel.
//
//   node e2e/scripts/fix-term-repro.mjs [--base http://127.0.0.1:7722]
//
// The page's WebSocket is wrapped before the app loads. Every `send` records the
// socket's readyState at call time and every socket records created/opened/
// closed, so the log separates:
//   - a keystroke xterm never delivered (no send call at all),
//   - a send the hook dropped because the socket was still CONNECTING,
//   - a send the browser accepted but the server never wrote to the PTY,
//   - a socket that closed mid-typing.
//
// Cases: type 25 ms/char immediately after clicking the 终端 tab, both on a
// fresh session and after the panel was used and switched away; type only once
// the pane says open (control); a zero-delay burst; and 120 ms/char.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-term");
mkdirSync(out, { recursive: true });

const argBase = process.argv.indexOf("--base");
const BASE = argBase > -1 ? process.argv[argBase + 1] : "http://127.0.0.1:7722";
const stamp = Date.now();
const log = { base: BASE, stamp, cases: [] };
const save = () => writeFileSync(path.join(out, "repro-log.json"), JSON.stringify(log, null, 2));

const csrfOf = (cookies) => cookies.find((c) => c.name === "pf_csrf")?.value ?? "";

async function login(page) {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill("dev@piforge.local");
  await page.getByTestId("login-password").fill("piforge-dev-123");
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

const xtermText = (page) =>
  page.evaluate(
    () =>
      document.querySelector('[data-testid="terminal-tab-panel"] [data-slot="xterm-surface"] .xterm-rows')
        ?.textContent ?? "",
  );

const paneStatus = (page) =>
  page.evaluate(
    () => document.querySelector('[data-testid="terminal-pane"]')?.getAttribute("data-pty-status") ?? "",
  );

async function waitOpen(page) {
  await page.waitForFunction(
    () => document.querySelector('[data-testid="terminal-pane"]')?.getAttribute("data-pty-status") === "open",
    undefined,
    { timeout: 30_000 },
  );
}

async function typeAndRead(page, command, delay) {
  const surface = page.locator('[data-testid="terminal-tab-panel"] [data-slot="xterm-surface"]').first();
  await surface.waitFor({ state: "visible", timeout: 30_000 });
  await surface.click();
  await page.waitForTimeout(40);
  const before = await page.evaluate(() => performance.now());
  await page.keyboard.type(command, { delay });
  const after = await page.evaluate(() => performance.now());
  await page.keyboard.press("Enter");
  await page.waitForTimeout(2500);
  const text = await xtermText(page);
  const ws = await page.evaluate(() => window.__wslog);
  return { typedMs: Math.round(after - before), ws, text, status: await paneStatus(page) };
}

function summarise(name, command, typedMs, ws, text, status) {
  const openFrames = ws.reduce((n, w) => n + w.framesOpen, 0);
  const droppedFrames = ws.reduce((n, w) => n + w.framesNotOpen, 0);
  const chars = ws.reduce((n, w) => n + w.charsOpen, 0);
  const rec = {
    name,
    command,
    typedChars: command.length,
    typedMs,
    status,
    sockets: ws.map((w) => ({
      created: Math.round(w.created),
      opened: w.opened === null ? null : Math.round(w.opened),
      closed: w.closed === null ? null : Math.round(w.closed),
      closeCode: w.closeCode,
      framesOpen: w.framesOpen,
      framesNotOpen: w.framesNotOpen,
      charsOpen: w.charsOpen,
    })),
    openFrames,
    droppedFrames,
    chars,
    // What the shell printed: the last 400 chars of the terminal.
    tail: text.slice(-400),
  };
  log.cases.push(rec);
  save();
  console.log(JSON.stringify(rec, null, 2));
  return rec;
}

async function attachLog(page) {
  await page.addInitScript(() => {
    window.__wslog = [];
    const Original = window.WebSocket;
    window.WebSocket = class extends Original {
      constructor(...args) {
        super(...args);
        const rec = {
          url: String(args[0]),
          created: performance.now(),
          opened: null,
          closed: null,
          closeCode: null,
          framesOpen: 0,
          framesNotOpen: 0,
          charsOpen: 0,
        };
        window.__wslog.push(rec);
        this.addEventListener("open", () => {
          rec.opened = performance.now();
        });
        this.addEventListener("close", (event) => {
          rec.closed = performance.now();
          rec.closeCode = event.code;
        });
        const send = this.send.bind(this);
        this.send = (data) => {
          const binary = typeof data !== "string";
          if (binary && this.readyState === Original.OPEN) {
            rec.framesOpen += 1;
            rec.charsOpen += data.byteLength;
          } else if (binary) {
            rec.framesNotOpen += 1;
          }
          return send(data);
        };
      }
    };
  });
}

async function newSessionPage(browser, label) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await attachLog(page);
  await login(page);
  const project = await api(page, "POST", "/v1/projects", {
    name: `fixterm-${label}-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = project.body.project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    title: `fixterm-${label}`,
  });
  const sessionId = session.body.session.id;
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 40_000 });
  return page;
}

const browser = await chromium.launch({ channel: "chrome" });
try {
  // A: type the instant the terminal panel is clicked on a fresh session.
  {
    const page = await newSessionPage(browser, "immediate");
    await page.getByTestId("panel-tab-terminal").click();
    const r = await typeAndRead(page, "echo AAAA-BBBB; echo RC=$?", 25);
    summarise("immediate-25ms", "echo AAAA-BBBB; echo RC=$?", r.typedMs, r.ws, r.text, r.status);
    await page.screenshot({ path: path.join(out, "repro-immediate-25ms.png"), animations: "disabled" });
    await page.close();
  }

  // B: the panel was used, switched away (which unmounts it), then clicked again
  // and typed without waiting for the socket — the user's second visit.
  {
    const page = await newSessionPage(browser, "revisit");
    await page.getByTestId("panel-tab-terminal").click();
    await waitOpen(page);
    const surface = page.locator('[data-testid="terminal-tab-panel"] [data-slot="xterm-surface"]').first();
    await surface.click();
    await page.keyboard.type("echo WARMUP", { delay: 20 });
    await page.keyboard.press("Enter");
    await page.waitForTimeout(800);
    await page.getByTestId("panel-tab-files").click();
    await page.waitForTimeout(300);
    await page.getByTestId("panel-tab-terminal").click();
    const r = await typeAndRead(page, "echo CCCC-DDDD; echo RC=$?", 25);
    summarise("revisit-25ms", "echo CCCC-DDDD; echo RC=$?", r.typedMs, r.ws, r.text, r.status);
    await page.screenshot({ path: path.join(out, "repro-revisit-25ms.png"), animations: "disabled" });
    await page.close();
  }

  // C: control — type only once the pane reports open.
  {
    const page = await newSessionPage(browser, "ready");
    await page.getByTestId("panel-tab-terminal").click();
    await waitOpen(page);
    const r = await typeAndRead(page, "echo EEEE-FFFF; echo RC=$?", 25);
    summarise("ready-25ms", "echo EEEE-FFFF; echo RC=$?", r.typedMs, r.ws, r.text, r.status);
    await page.screenshot({ path: path.join(out, "repro-ready-25ms.png"), animations: "disabled" });
    await page.close();
  }

  // D: zero-delay burst once open.
  {
    const page = await newSessionPage(browser, "burst");
    await page.getByTestId("panel-tab-terminal").click();
    await waitOpen(page);
    const r = await typeAndRead(page, "echo GGGG-HHHH; echo RC=$?", 0);
    summarise("ready-0ms", "echo GGGG-HHHH; echo RC=$?", r.typedMs, r.ws, r.text, r.status);
    await page.close();
  }
} finally {
  await browser.close();
}
writeFileSync(path.join(out, "repro-log.json"), JSON.stringify(log, null, 2));
console.log("[done]", path.join(out, "repro-log.json"));
