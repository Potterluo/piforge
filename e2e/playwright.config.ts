import { defineConfig, devices } from "@playwright/test";
import path from "node:path";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const backendExe = path.resolve(
  here,
  process.platform === "win32" ? ".bin/piforged.exe" : ".bin/piforged",
);
const dataDir = path.resolve(here, ".data");

/**
 * The browser panel needs a real Chrome inside the sandbox. The suite already
 * requires Chrome for its own channel, so it finds it the same way and passes it
 * to the control plane; without one the panel would only ever be tested in its
 * "unavailable" state.
 */
function findChrome(): string | undefined {
  const candidates = [
    process.env.PIFORGE_CHROME,
    "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
    process.env.LOCALAPPDATA
      ? path.join(process.env.LOCALAPPDATA, "Google\\Chrome\\Application\\chrome.exe")
      : undefined,
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
  ];
  for (const candidate of candidates) {
    if (candidate && existsSync(candidate)) return candidate;
  }
  return undefined;
}

const chromeBinary = findChrome();

/**
 * The suite runs the real Go control plane against the fake Pi peer, so no
 * model account, no network and no container runtime are required. The only
 * thing that is not real is the agent's model.
 */
export default defineConfig({
  testDir: "./tests",
  timeout: 60_000,
  // One retry: this suite drives a real Chrome against a real server, and under a
  // full-suite load a settle occasionally lands outside the expect window. A retry
  // reports the flake instead of hiding it, and a genuine failure still fails twice.
  retries: 1,
  // 15s was tight when every other spec had just run; assertions that wait on a
  // streamed run now get the time they need rather than a coin flip.
  expect: { timeout: 20_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  outputDir: "./.artifacts",
  use: {
    baseURL: "http://127.0.0.1:3100",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
  },
  // The build box has no access to cdn.playwright.dev, so the suite drives the
  // locally installed Chrome instead of a downloaded Chromium. Anyone with a
  // downloadable browser can switch this to the bundled `chromium` project.
  projects: [
    {
      name: "chrome",
      use: { ...devices["Desktop Chrome"], channel: "chrome" },
    },
  ],

  webServer: [
    {
      command: `"${backendExe}" -addr 127.0.0.1:8080 -data "${dataDir}" -fake-pi -dev${chromeBinary ? ` -chrome "${chromeBinary}"` : ""}`,
      url: "http://127.0.0.1:8080/healthz",
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "pipe",
      stderr: "pipe",
      env: {
        PIFORGE_SEED_EMAIL: "admin@piforge.local",
        PIFORGE_SEED_PASSWORD: "piforge-admin-123",
        PIFORGE_SEED_USER_EMAIL: "dev@piforge.local",
        PIFORGE_SEED_USER_PASSWORD: "piforge-dev-123",
        PIFORGE_FAKE_PI_DELAY_MS: "40",
        // Eighty sessions in one run would otherwise leave eighty agent processes
        // alive, and the machine gets slower with each spec until late-suite logins
        // start timing out. Reaping is a product behaviour; the harness uses a short
        // TTL so it doubles as the thing that keeps the run honest.
        PIFORGE_AGENT_IDLE_TTL: "45s",
        // A PTY outlives the socket that opened it (that is how a reconnect
        // replays scrollback), so one long-lived server accumulates a terminal
        // per tab this suite ever opened. The node default is 64; a full run
        // opens more than that, and the 65th terminal spec then failed with
        // "term: too many sessions" depending only on test order.
        PIFORGE_TERM_MAX_SESSIONS: "256",
      },
    },
    {
      // The suite runs against a production build on purpose: `next dev`
      // compiles a route on first request, so an early click can land before
      // React hydrates and fall through to the browser's native form submit.
      // A built app hydrates immediately and is what actually ships.
      command: "node node_modules/next/dist/bin/next start -p 3100",
      cwd: path.resolve(here, "../web"),
      url: "http://127.0.0.1:3100/login",
      reuseExistingServer: false,
      timeout: 120_000,
      env: { PIFORGE_BACKEND: "http://127.0.0.1:8080" },
    },
  ],
});
