// Targeted cleanup for the e2e runtime data directory.
//
// The e2e suite drives one control plane against one `e2e/.data` directory, and
// it is meant to be run repeatedly on that directory (see the comment at the
// top of prepare.mjs). Two things accumulate, and neither is a leak a spec can
// be blamed for:
//
//   1. Accounts. Every spec that needs an isolated tenant calls `freshAccount`
//      (`e2e-…@example.test`), and a few specs create accounts directly
//      (`promote-…`, `last-admin-…`, `first-run-…`). The product has no
//      delete-account route on purpose, so the rows stay.
//   2. Projects, sessions and sandboxes created on the *seeded* accounts by
//      specs that are about projects, not about isolation: `openNewSession`
//      creates one project per call, 53 creates 42, 60 creates two projects
//      with 40 sessions, 61 leaves the ones it renames. Their checkout
//      directories under `sandboxes/` are the bulk of the file count.
//
// Starship-scale rot: 934 projects and ~23k files, at which point specs that
// touch none of them (login, sidebar) start timing out on a reused directory
// and the only way to get a green run is `test:fresh`'s 7.5-minute rebuild.
// That is how a suite teaches people to ignore red.
//
// What this removes — and it is deliberately narrow:
//
//   * every account that is not one of the two seeded ones (and, by the
//     schema's ON DELETE CASCADE, their providers, skills, MCP servers, API
//     keys, feature flags, auth sessions, projects, sessions, events and
//     sandbox rows);
//   * every project — seeds create none, so a project on the seeded account is
//     something a spec made;
//   * their sessions, runs, events, tool calls, test runs and snapshots;
//   * the on-disk leftovers: `sandboxes/<user>/<project>`, `sandboxes/_state/
//     <user>/<project>.json`, `pi-agents/<session>`, `artifacts/<session>` and
//     `skills/<id>` bundles no longer referenced by a row.
//
// What it deliberately leaves: the seeded admin and dev accounts (password
// hashes, roles, status), the deployment-wide shared provider, the seeded
// skills, the master/secret keys, and anything the pre-flight guard
// (`tests/preflight.setup.ts`) reads. The guard expects exactly those two
// accounts and exactly the seeded provider/skill set, so this cleanup makes the
// guard's starting point *more* true, not less.
//
// The file is a plain module so `prepare.mjs` can import it and so the cleanup
// can be exercised on its own (`node scripts/clean-data.mjs`).
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

/** Projects above this many mean a reused directory is no longer cheap. */
export const PROJECT_WARN_THRESHOLD = 300;
/** Files above this many make `.data` a slow disk to scan and a slow dir to copy. */
export const FILE_WARN_THRESHOLD = 8000;

/**
 * The two accounts a fresh deployment seeds (`store.EnsureAdminSeed`, wired by
 * `cmd/piforged`). `playwright.config.ts` pins the same values through
 * PIFORGE_SEED_*; they are read here too so a differently-named seed does not
 * get deleted as if it were a spec account.
 */
export function seededAccounts() {
  return [
    (process.env.PIFORGE_SEED_EMAIL || "admin@piforge.local").trim().toLowerCase(),
    (process.env.PIFORGE_SEED_USER_EMAIL || "dev@piforge.local").trim().toLowerCase(),
  ];
}

/** node:sqlite is experimental; its one-line warning is noise in a test log. */
function quietSqliteWarning() {
  // Node installs its own `warning` listener at startup and prints through it,
  // so adding a listener on top would leave the line in place. Replace the
  // handler instead, and re-print everything that is not the SQLite notice.
  process.removeAllListeners("warning");
  process.on("warning", (warning) => {
    if (warning.name === "ExperimentalWarning" && /SQLite/i.test(warning.message)) return;
    console.warn(`${warning.name}: ${warning.message}`);
  });
}

function countTree(target) {
  let files = 0;
  let bytes = 0;
  const stack = [target];
  while (stack.length > 0) {
    const current = stack.pop();
    let entries;
    try {
      entries = fs.readdirSync(current, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const entry of entries) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) {
        stack.push(full);
      } else if (entry.isFile()) {
        files += 1;
        try {
          bytes += fs.statSync(full).size;
        } catch {
          /* a file that vanished mid-scan is not worth failing over */
        }
      }
    }
  }
  return { files, bytes };
}

function remove(target, log) {
  if (!fs.existsSync(target)) return { files: 0, bytes: 0 };
  const counted = countTree(target);
  try {
    fs.rmSync(target, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 });
  } catch (error) {
    log(`[e2e] cleanup: could not remove ${target}: ${error.message}`);
    return { files: 0, bytes: 0 };
  }
  return counted;
}

function formatBytes(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

function openDatabase(dbPath) {
  quietSqliteWarning();
  // Resolved lazily so the module can be imported on a machine whose Node
  // predates `node:sqlite` without crashing at import time.
  const { DatabaseSync } = process.getBuiltinModule("node:sqlite");
  const db = new DatabaseSync(dbPath);
  db.exec("PRAGMA foreign_keys = ON");
  // The previous run's server is gone by the time prepare runs; a short wait
  // covers a lingering handle while still failing loudly instead of silently
  // skipping the cleanup.
  db.exec("PRAGMA busy_timeout = 5000");
  const enabled = db.prepare("PRAGMA foreign_keys").get();
  if (!enabled || Object.values(enabled)[0] !== 1) {
    throw new Error("[e2e] cleanup: SQLite refused to enable foreign keys; cascades would not run");
  }
  return db;
}

function tableCounts(db) {
  const count = (table) => db.prepare(`SELECT COUNT(*) AS c FROM ${table}`).get().c;
  return {
    accounts: count("users"),
    projects: count("projects"),
    sessions: count("sessions"),
    sandboxes: count("sandboxes"),
  };
}

/**
 * Measures a data directory without changing it. Used for the startup warning,
 * and for the "not materially larger" comparison an operator wants to see.
 *
 * A missing database is not an error: a directory that has never been booted
 * has nothing to measure.
 */
export function measureDataDir(dataDir) {
  const dbPath = path.join(dataDir, "piforge.db");
  const measured = {
    dir: dataDir,
    hasDatabase: fs.existsSync(dbPath),
    accounts: 0,
    projects: 0,
    sessions: 0,
    sandboxes: 0,
    files: 0,
    bytes: 0,
  };
  if (measured.hasDatabase) {
    let db;
    try {
      db = openDatabase(dbPath);
      const counts = tableCounts(db);
      measured.accounts = counts.accounts;
      measured.projects = counts.projects;
      measured.sessions = counts.sessions;
      measured.sandboxes = counts.sandboxes;
    } catch (error) {
      // A corrupt or locked database is exactly the situation where a warning
      // still helps; report the directory size and let the caller decide.
      measured.error = error.message;
    } finally {
      db?.close();
    }
  }
  if (fs.existsSync(dataDir)) {
    const tree = countTree(dataDir);
    measured.files = tree.files;
    measured.bytes = tree.bytes;
  }
  return measured;
}

/** The startup line: says the counts out loud and names the way out. */
export function describeDegradation(measured) {
  const lines = [];
  if (measured.projects > PROJECT_WARN_THRESHOLD) {
    lines.push(
      `[e2e] WARNING: the e2e data dir holds ${measured.projects} projects ` +
        `(past the ${PROJECT_WARN_THRESHOLD}-project line).`,
    );
  }
  if (measured.files > FILE_WARN_THRESHOLD) {
    lines.push(
      `[e2e] WARNING: the e2e data dir holds ${measured.files} files ` +
        `(${formatBytes(measured.bytes)}; past the ${FILE_WARN_THRESHOLD}-file line).`,
    );
  }
  if (lines.length === 0) return "";
  return (
    `${lines.join("\n")}\n` +
    `[e2e]          A directory this large slows specs that never touch it (login, sidebar, ` +
    `project lists),\n` +
    `[e2e]          which reads as random flake. The targeted cleanup below removes the ` +
    `suite's own leftovers;\n` +
    `[e2e]          when the counts stay high after it, the remainder is not something the ` +
    `cleanup recognises:\n` +
    `[e2e]          run \`pnpm --dir e2e test:fresh\` for a true rebuild.`
  );
}

/**
 * Removes the suite's leftovers from `dataDir` and reports what went.
 *
 * The two accounts from `seededAccounts()` are never touched, and the function
 * refuses to act on a database that does not contain both of them — that is the
 * check that keeps a mistyped `--data` from deleting a real deployment.
 */
export function cleanDataDir(dataDir, log = console.log) {
  const dbPath = path.join(dataDir, "piforge.db");
  const summary = {
    skipped: false,
    removedAccounts: 0,
    removedProjects: 0,
    removedSessions: 0,
    removedSandboxes: 0,
    removedFiles: 0,
    removedBytes: 0,
    keptProjects: [],
  };

  if (!fs.existsSync(dbPath)) {
    log(`[e2e] cleanup: ${dataDir} has no piforge.db yet; nothing to remove`);
    summary.skipped = true;
    return summary;
  }

  const db = openDatabase(dbPath);
  try {
    const before = tableCounts(db);
    if (before.accounts === 0) {
      log(`[e2e] cleanup: ${dataDir} has no accounts yet; nothing to remove`);
      summary.skipped = true;
      return summary;
    }

    const seeded = new Set(seededAccounts());
    const accounts = db.prepare("SELECT id, email FROM users ORDER BY email").all();
    const present = new Set(accounts.map((row) => row.email.trim().toLowerCase()));
    const missing = [...seeded].filter((email) => !present.has(email));
    if (missing.length > 0) {
      throw new Error(
        `[e2e] cleanup: refusing to prune ${dataDir}: the seeded account(s) ${missing.join(", ")} ` +
          `are missing, so this does not look like an e2e data directory. ` +
          `Seeded accounts are read from PIFORGE_SEED_EMAIL / PIFORGE_SEED_USER_EMAIL ` +
          `(default admin@piforge.local, dev@piforge.local).`,
      );
    }

    const doomedAccounts = accounts.filter(
      (row) => !seeded.has(row.email.trim().toLowerCase()),
    );
    const deleteAccount = db.prepare("DELETE FROM users WHERE id = ?");
    const sessionCount = db.prepare("SELECT COUNT(*) AS c FROM sessions").get().c;
    const sandboxCount = db.prepare("SELECT COUNT(*) AS c FROM sandboxes").get().c;

    // Deleting an account cascades to everything it owns: providers, skills,
    // MCP servers, API keys, flags, auth sessions, projects, sessions, runs,
    // events, tool calls, test runs, snapshots and sandbox rows.
    for (const row of doomedAccounts) deleteAccount.run(row.id);

    const remainingProjects = db
      .prepare(
        "SELECT p.name, u.email FROM projects p JOIN users u ON u.id = p.user_id ORDER BY p.created_at",
      )
      .all();
    db.exec("DELETE FROM projects");

    summary.removedAccounts = doomedAccounts.length;
    summary.removedProjects = before.projects;
    summary.removedSessions = sessionCount;
    summary.removedSandboxes = sandboxCount;
    summary.keptProjects = remainingProjects.map((row) => row.name);

    // The WAL is where a killed server parks its pages; checkpoint and compact
    // so the file count and the file size after the cleanup describe the live
    // database rather than its journal.
    db.exec("PRAGMA wal_checkpoint(TRUNCATE)");
    db.exec("VACUUM");
  } finally {
    db.close();
  }

  // --- the on-disk side ---------------------------------------------------
  const live = liveIds(dbPath);
  const removed = { files: 0, bytes: 0 };
  const add = (counted) => {
    removed.files += counted.files;
    removed.bytes += counted.bytes;
  };

  const sandboxes = path.join(dataDir, "sandboxes");
  if (live.projects.size === 0) {
    // Nothing is left to keep, so the whole tree goes in one call instead of
    // 900 recursive deletes.
    add(remove(sandboxes, log));
  } else {
    add(removeDead(sandboxes, live.users, "usr_", log));
    add(removeDeadAssignments(path.join(sandboxes, "_state"), live.users, live.projects, log));
  }

  add(removeDead(path.join(dataDir, "pi-agents"), live.sessions, "ses_", log));
  add(removeDead(path.join(dataDir, "artifacts"), live.sessions, "ses_", log));
  add(removeDead(path.join(dataDir, "skills"), live.skills, "skl_", log));

  summary.removedFiles = removed.files;
  summary.removedBytes = removed.bytes;
  return summary;
}

/** The ids still referenced by the database, after the deletes. */
function liveIds(dbPath) {
  quietSqliteWarning();
  const { DatabaseSync } = process.getBuiltinModule("node:sqlite");
  const db = new DatabaseSync(dbPath, { readOnly: true });
  try {
    const ids = (table) =>
      new Set(db.prepare(`SELECT id FROM ${table}`).all().map((row) => row.id));
    return {
      users: ids("users"),
      projects: ids("projects"),
      sessions: ids("sessions"),
      skills: ids("skills"),
    };
  } finally {
    db.close();
  }
}

/**
 * Removes `prefix<id>` entries of `dir` whose id is not live. One level deep:
 * that is the shape of `pi-agents/`, `artifacts/` and `skills/`.
 */
function removeDead(dir, live, prefix, log) {
  const removed = { files: 0, bytes: 0 };
  let entries;
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true });
  } catch {
    return removed;
  }
  for (const entry of entries) {
    if (!entry.isDirectory() || !entry.name.startsWith(prefix)) continue;
    if (live.has(entry.name)) continue;
    const counted = remove(path.join(dir, entry.name), log);
    removed.files += counted.files;
    removed.bytes += counted.bytes;
  }
  return removed;
}

/**
 * `sandboxes/<usr>/<prj>` and `sandboxes/_state/<usr>/<prj>.json` both nest a
 * user level and a project level; a dead user takes the whole directory, a dead
 * project takes one entry inside it.
 */
function removeDeadAssignments(dir, liveUsers, liveProjects, log) {
  const removed = { files: 0, bytes: 0 };
  let users;
  try {
    users = fs.readdirSync(dir, { withFileTypes: true });
  } catch {
    return removed;
  }
  for (const user of users) {
    if (!user.isDirectory()) continue;
    const userDir = path.join(dir, user.name);
    if (!liveUsers.has(user.name)) {
      const counted = remove(userDir, log);
      removed.files += counted.files;
      removed.bytes += counted.bytes;
      continue;
    }
    let entries;
    try {
      entries = fs.readdirSync(userDir, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const entry of entries) {
      const projectId = entry.isDirectory() ? entry.name : entry.name.replace(/\.json$/, "");
      if (liveProjects.has(projectId)) continue;
      const counted = remove(path.join(userDir, entry.name), log);
      removed.files += counted.files;
      removed.bytes += counted.bytes;
    }
  }
  return removed;
}

/** `node scripts/clean-data.mjs [--data <dir>] [--measure]` — inspect, or clean. */
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const here = path.dirname(fileURLToPath(import.meta.url));
  const dataFlag = process.argv.indexOf("--data");
  const dataDir =
    dataFlag > -1 ? path.resolve(process.argv[dataFlag + 1]) : path.resolve(here, "../.data");

  if (process.argv.includes("--measure")) {
    // Read-only: the numbers a report needs before and after a run.
    const measured = measureDataDir(dataDir);
    console.log(
      `${measured.accounts} accounts, ${measured.projects} projects, ${measured.sessions} sessions, ` +
        `${measured.sandboxes} sandboxes, ${measured.files} files (${formatBytes(measured.bytes)})`,
    );
    const warning = describeDegradation(measured);
    if (warning) console.warn(warning);
    process.exit(0);
  }

  const before = measureDataDir(dataDir);
  const warning = describeDegradation(before);
  if (warning) console.warn(warning);
  console.log(
    `[e2e] before: ${before.accounts} accounts, ${before.projects} projects, ` +
      `${before.sessions} sessions, ${before.files} files (${formatBytes(before.bytes)})`,
  );
  const summary = cleanDataDir(dataDir);
  console.log(
    `[e2e] removed: ${summary.removedAccounts} accounts, ${summary.removedProjects} projects, ` +
      `${summary.removedSessions} sessions, ${summary.removedSandboxes} sandbox rows, ` +
      `${summary.removedFiles} files (${formatBytes(summary.removedBytes)})`,
  );
  if (summary.keptProjects.length > 0) {
    console.log(
      `[e2e] removed project names: ${summary.keptProjects.slice(0, 12).join(", ")}` +
        (summary.keptProjects.length > 12 ? `, … (${summary.keptProjects.length} total)` : ""),
    );
  }
  const after = measureDataDir(dataDir);
  console.log(
    `[e2e] after: ${after.accounts} accounts, ${after.projects} projects, ` +
      `${after.sessions} sessions, ${after.files} files (${formatBytes(after.bytes)})`,
  );
}
