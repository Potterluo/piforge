// Prepares the environment for an e2e run:
//   1. (only with --fresh) wipe the previous data directory
//   2. build the backend binary
//   3. build the fake Pi peer next to it (piforged prefers a sibling binary)
//   4. build the web app the suite drives
//
// The data directory is KEPT by default. The suite has to be runnable twice in a
// row on the same `.data` — that is how state leaks between specs show up before
// a developer does. Wiping on every `pnpm test` hid exactly that, so a run that
// only passes on an empty volume looked green. Pass `--fresh` (or set
// PIFORGE_E2E_RESET=1) when a clean baseline is what you want; `pnpm test:fresh`
// does that in one command.
//
// Building instead of `go run` matters on Windows: `go run` leaves the child
// alive when the parent is killed, so the port stays bound and the next run
// fails with a confusing EADDRINUSE.
import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const backendDir = path.join(repoRoot, "backend");
const outDir = path.resolve(here, "../.bin");
const dataDir = path.resolve(here, "../.data");

const exe = (name) => (process.platform === "win32" ? `${name}.exe` : name);
const fresh = process.argv.includes("--fresh") || process.env.PIFORGE_E2E_RESET === "1";

if (fresh) {
  console.log("[e2e] --fresh: removing the runtime data (sandboxes, SQLite, skills)");
  rmSync(dataDir, { recursive: true, force: true });
} else {
  console.log(
    `[e2e] keeping the runtime data at ${dataDir} ` +
      "(the suite must pass on a reused directory; --fresh starts from empty)",
  );
}
mkdirSync(outDir, { recursive: true });

const build = (pkg, out) => {
  console.log(`[e2e] go build ${pkg} -> ${out}`);
  execFileSync("go", ["build", "-o", path.join(outDir, out), pkg], {
    cwd: backendDir,
    stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0" },
  });
};

build("./cmd/piforged", exe("piforged"));
build("./cmd/fakepi", exe("fakepi"));

// The suite drives a production build (see playwright.config.ts). Building here
// means `pnpm test` is a single command that always tests current sources.
console.log("[e2e] building the web app");
execFileSync("pnpm", ["exec", "next", "build"], {
  cwd: path.join(repoRoot, "web"),
  stdio: "inherit",
  shell: process.platform === "win32",
  env: {
    ...process.env,
    PIFORGE_BACKEND: "http://127.0.0.1:8080",
    // Next does not upgrade WebSocket requests through its rewrites, so the
    // terminal socket addresses the Go server directly.
    NEXT_PUBLIC_PIFORGE_WS_BASE: "ws://127.0.0.1:8080",
  },
});

console.log("[e2e] ready");
