// Prepares the environment for an e2e run:
//   1. (only with --fresh) wipe the previous data directory
//   2. (otherwise) prune what previous runs created and warn when the directory
//      has grown past the point where unrelated specs degrade
//   3. build the backend binary
//   4. build the fake Pi peer next to it (piforged prefers a sibling binary)
//   5. build the web app the suite drives
//
// The data directory is KEPT by default. The suite has to be runnable twice in a
// row on the same `.data` — that is how state leaks between specs show up before
// a developer does. Wiping on every `pnpm test` hid exactly that, so a run that
// only passes on an empty volume looked green. Pass `--fresh` (or set
// PIFORGE_E2E_RESET=1) when a clean baseline is what you want; `pnpm test:fresh`
// does that in one command.
//
// Keeping the directory used to mean keeping *everything*: accounts, their
// projects, sessions, and every sandbox checkout. A suite that runs often grew
// to ~934 projects / ~23k files, and at that size specs that touch none of it
// (login, the sidebar, project lists) started timing out on a reused directory.
// The suite then only passed after `test:fresh`'s 7.5-minute rebuild, which is
// how a suite teaches people to ignore red. `clean-data.mjs` removes exactly the
// leftovers the suite creates — see the comment at the top of that file for what
// is removed and what is deliberately kept.
//
// Building instead of `go run` matters on Windows: `go run` leaves the child
// alive when the parent is killed, so the port stays bound and the next run
// fails with a confusing EADDRINUSE.
import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { cleanDataDir, describeDegradation, measureDataDir } from "./clean-data.mjs";

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

  // Measure *before* pruning as well as after: the startup line the report
  // asked for is about the directory a developer actually has, and a warning
  // that only appears once the cleanup already fixed it would never be seen.
  const before = measureDataDir(dataDir);
  const warning = describeDegradation(before);
  if (warning) console.warn(warning);

  const cleaned = cleanDataDir(dataDir);
  if (!cleaned.skipped) {
    console.log(
      `[e2e] cleanup removed ${cleaned.removedAccounts} spec accounts, ` +
        `${cleaned.removedProjects} projects, ${cleaned.removedSessions} sessions, ` +
        `${cleaned.removedSandboxes} sandbox rows and ${cleaned.removedFiles} files ` +
        `(${(cleaned.removedBytes / (1024 * 1024)).toFixed(1)} MB)`,
    );
    if (cleaned.keptProjects.length > 0) {
      const names = cleaned.keptProjects;
      console.log(
        `[e2e] cleanup removed these project names: ${names.slice(0, 10).join(", ")}` +
          (names.length > 10 ? `, … (${names.length} total)` : ""),
      );
    }
    console.log(
      `[e2e] cleanup kept the seeded accounts, the shared provider and the seeded skills`,
    );
  }

  const after = measureDataDir(dataDir);
  console.log(
    `[e2e] data dir after cleanup: ${after.accounts} accounts, ${after.projects} projects, ` +
      `${after.sessions} sessions, ${after.files} files ` +
      `(${(after.bytes / (1024 * 1024)).toFixed(1)} MB)`,
  );
  if (after.projects > 300 || after.files > 8000) {
    // Reached only when the cleanup did not explain the size: something else is
    // writing to this directory, and `test:fresh` is the honest way out.
    console.warn(
      `[e2e] WARNING: still ${after.projects} projects / ${after.files} files after the cleanup; ` +
        `that growth is not something the cleanup recognises. Run \`pnpm --dir e2e test:fresh\` ` +
        `for a true rebuild, and add the new owner of that state to scripts/clean-data.mjs.`,
    );
  }
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
