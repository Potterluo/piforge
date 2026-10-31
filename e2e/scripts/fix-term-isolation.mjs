// Is the loss in the browser client or in the server relay?
//
// Opens the PTY socket directly from a logged-in page (no xterm, no usePtySession)
// and sends the same bytes the panel would. Two rates: one frame per char at
// 25 ms, and a single zero-delay burst of one-byte frames. Then reads what the
// shell echoed. Any missing character here is server-side.
//
// It also reports how long a WebSocket handshake takes with and without CDP
// network latency, which is the lever the e2e uses to make the pre-connect
// window deterministic.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-term");
mkdirSync(out, { recursive: true });

const BASE = process.env.BASE ?? "http://127.0.0.1:7722";
const stamp = Date.now();
const result = { base: BASE, stamp };
const save = () => writeFileSync(path.join(out, "server-isolation.json"), JSON.stringify(result, null, 2));

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
  return { status: res.status(), body: await res.json().catch(() => null) };
}

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
try {
  await login(page);
  const project = await api(page, "POST", "/v1/projects", {
    name: `fixterm-iso-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = project.body.project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "iso" });
  const sessionId = session.body.session.id;
  result.sessionId = sessionId;

  // How long does the handshake take with 500 ms of emulated latency?
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Network.enable");
  const handshake = async () => {
    const t = await page.evaluate(async (sid) => {
      const url = `${location.origin.replace("http", "ws")}/v1/sessions/${sid}/pty?tab=iso-handshake`;
      const t0 = performance.now();
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      const opened = await new Promise((resolve) => {
        ws.addEventListener("open", () => resolve(performance.now() - t0));
        ws.addEventListener("close", () => resolve(-1));
        ws.addEventListener("error", () => resolve(-2));
      });
      ws.close(1000, "done");
      return opened;
    }, sessionId);
    return t;
  };
  result.handshakeNormalMs = await handshake();
  await cdp.send("Network.emulateNetworkConditions", {
    offline: false,
    latency: 500,
    downloadThroughput: -1,
    uploadThroughput: -1,
  });
  result.handshakeLatencyMs = await handshake();
  await cdp.send("Network.emulateNetworkConditions", {
    offline: false,
    latency: 0,
    downloadThroughput: -1,
    uploadThroughput: -1,
  });
  save();

  // Direct relay test: type through the socket, no client code involved.
  const relay = await page.evaluate(
    async ({ sid, mode }) => {
      const url = `${location.origin.replace("http", "ws")}/v1/sessions/${sid}/pty?tab=${mode}`;
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      const decoder = new TextDecoder();
      let output = "";
      const frames = [];
      ws.addEventListener("message", (event) => {
        if (typeof event.data === "string") {
          frames.push(JSON.parse(event.data));
        } else {
          output += decoder.decode(new Uint8Array(event.data), { stream: true });
        }
      });
      await new Promise((resolve, reject) => {
        ws.addEventListener("open", resolve);
        ws.addEventListener("error", reject);
      });
      const ready = await new Promise((resolve) => {
        const tick = setInterval(() => {
          if (frames.some((f) => f.type === "ready")) {
            clearInterval(tick);
            resolve(true);
          }
        }, 10);
      });
      const command = mode === "burst" ? "echo BURST-ISO-1234; echo RC=$?" : "echo FAST-ISO-5678; echo RC=$?";
      const t0 = performance.now();
      if (mode === "burst") {
        for (const ch of command) ws.send(new TextEncoder().encode(ch));
        ws.send(new TextEncoder().encode("\r"));
      } else {
        for (const ch of command) {
          ws.send(new TextEncoder().encode(ch));
          await new Promise((r) => setTimeout(r, 25));
        }
        ws.send(new TextEncoder().encode("\r"));
      }
      const sentMs = performance.now() - t0;
      await new Promise((r) => setTimeout(r, 2500));
      ws.close(1000, "done");
      return { ready, command, sentMs: Math.round(sentMs), output };
    },
    { sid: sessionId, mode: "fast" },
  );
  result.relayFast = {
    command: relay.command,
    sentMs: relay.sentMs,
    echoed: relay.output.includes(relay.command),
    ran: relay.output.includes("FAST-ISO-5678") && relay.output.includes("RC=0"),
    tail: relay.output.slice(-400),
  };
  const burst = await page.evaluate(
    async ({ sid }) => {
      const url = `${location.origin.replace("http", "ws")}/v1/sessions/${sid}/pty?tab=burst2`;
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      const decoder = new TextDecoder();
      let output = "";
      const frames = [];
      ws.addEventListener("message", (event) => {
        if (typeof event.data === "string") frames.push(JSON.parse(event.data));
        else output += decoder.decode(new Uint8Array(event.data), { stream: true });
      });
      await new Promise((resolve, reject) => {
        ws.addEventListener("open", resolve);
        ws.addEventListener("error", reject);
      });
      await new Promise((resolve) => {
        const tick = setInterval(() => {
          if (frames.some((f) => f.type === "ready")) {
            clearInterval(tick);
            resolve(true);
          }
        }, 10);
      });
      const command = "echo BURST-ISO-1234; echo RC=$?";
      const encoder = new TextEncoder();
      for (const ch of command) ws.send(encoder.encode(ch));
      ws.send(encoder.encode("\r"));
      await new Promise((r) => setTimeout(r, 2500));
      ws.close(1000, "done");
      return { command, output };
    },
    { sid: sessionId },
  );
  result.relayBurst = {
    command: burst.command,
    ran: burst.output.includes("BURST-ISO-1234") && burst.output.includes("RC=0"),
    tail: burst.output.slice(-400),
  };
} finally {
  save();
  await browser.close();
}
console.log(JSON.stringify(result, null, 2));
