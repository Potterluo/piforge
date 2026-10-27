// Builds the backend once so Playwright can launch a real binary instead of
// `go run`. `go run` spawns a child process that survives the parent on
// Windows, which leaves a port bound between runs — a binary does not.
import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const backendDir = path.resolve(here, "../../backend");
const outDir = path.resolve(here, "../.bin");
const exe = process.platform === "win32" ? "piforged.exe" : "piforged";

mkdirSync(outDir, { recursive: true });

console.log(`[e2e] building backend -> ${path.join(outDir, exe)}`);
execFileSync("go", ["build", "-o", path.join(outDir, exe), "./cmd/piforged"], {
  cwd: backendDir,
  stdio: "inherit",
  env: { ...process.env, CGO_ENABLED: "0" },
});
console.log("[e2e] backend built");
