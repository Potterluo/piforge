import { defineConfig, devices } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const backendExe = path.resolve(
  here,
  process.platform === "win32" ? ".bin/piforged.exe" : ".bin/piforged",
);
const dataDir = path.resolve(here, ".data");

/**
 * The suite runs the real Go control plane against the fake Pi peer, so no
 * model account, no network and no container runtime are required. The only
 * thing that is not real is the agent's model.
 */
export default defineConfig({
  testDir: "./tests",
  timeout: 60_000,
  expect: { timeout: 15_000 },
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
      command: `"${backendExe}" -addr 127.0.0.1:8080 -data "${dataDir}" -fake-pi -dev`,
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
