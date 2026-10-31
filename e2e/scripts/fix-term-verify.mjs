// Verification probe for the three terminal-input defects, run against a deployed
// instance (default :7722). It exercises the user's paths:
//
//   1. a burst typed on an open terminal, plus typing that starts while the
//      socket is still connecting (CDP network latency makes that window real);
//   2. `git log` and a large `git diff` in the shell: they must print and return
//      the prompt instead of parking in a pager;
//   3. the fork dialog's 分支名称 helper line (screenshotted separately).
//
//   node e2e/scripts/fix-term-verify.mjs [--base http://127.0.0.1:7722]
//
// Screenshots, terminal text and a JSON log land in e2e/.artifacts/fix-term/.
// The project it creates is deleted at the end; pass --keep to keep it.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-term");
mkdirSync(out, { recursive: true });

const argBase = process.argv.indexOf("--base");
const BASE = argBase > -1 ? process.argv[argBase + 1] : "http://127.0.0.1:7722";
const KEEP = process.argv.includes("--keep");
const stamp = Date.now();
const record = { base: BASE, stamp, checks: {} };
const save = () => writeFileSync(path.join(out, "verify-log.json"), JSON.stringify(record, null, 2));
const write = (name, text) => writeFileSync(path.join(out, name), text);

const failures = [];
const check = (condition, message, detail) => {
  if (condition) {
    console.log("[ok]", message);
    return true;
  }
  console.log("[BROKEN]", message, detail === undefined ? "" : JSON.stringify(detail));
  failures.push(message);
  return false;
};

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
    headers:
      typeof data === "string"
        ? { "X-PF-CSRF": csrfOf(await page.context().cookies()), "Content-Type": "text/plain; charset=utf-8" }
        : { "X-PF-CSRF": csrfOf(await page.context().cookies()) },
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

const rows = (page) =>
  page.evaluate(() => {
    const panels = [...document.querySelectorAll('[data-testid="terminal-tab-panel"]')];
    const visible = panels.find((panel) => !panel.classList.contains("hidden"));
    return visible?.querySelector('[data-slot="xterm-surface"] .xterm-rows')?.textContent ?? "";
  });

const shot = async (page, name) => {
  await page.screenshot({ path: path.join(out, `${name}.png`), animations: "disabled" });
  console.log("[shot]", `${name}.png`);
};

async function waitPrompt(page) {
  await page.waitForFunction(
    () => {
      const panels = [...document.querySelectorAll('[data-testid="terminal-tab-panel"]')];
      const visible = panels.find((panel) => !panel.classList.contains("hidden"));
      const text = visible?.querySelector('[data-slot="xterm-surface"] .xterm-rows')?.textContent ?? "";
      return /[#$>]\s*$/.test(text);
    },
    undefined,
    { timeout: 30_000 },
  );
}

/** Clears xterm's scrollback so a pager marker in this output is unambiguous. */
async function clearTerminal(page) {
  await page.getByRole("button", { name: "清屏" }).first().click();
  await page.waitForTimeout(200);
}

async function type(page, text, delay) {
  await page.keyboard.type(text, { delay });
  await page.keyboard.press("Enter");
}

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
try {
  await login(page);
  const project = await api(page, "POST", "/v1/projects", {
    name: `fixterm-verify-${stamp}`,
    repo_url: "",
    default_branch: "main",
  });
  check(project.status < 300, "probe project created", project.status);
  const projectId = project.body.project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title: "fix-term" });
  const sessionId = session.body.session.id;
  record.projectId = projectId;
  record.sessionId = sessionId;
  record.projectUrl = `${BASE}/p/${projectId}/s/${sessionId}`;
  save();

  // A repo with commits: `git log` has something to page through.
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=note.txt`, "one\n");
  await api(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "first commit",
    paths: ["note.txt"],
  });
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=note.txt`, "one\ntwo\n");
  const second = await api(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: "second commit",
    paths: ["note.txt"],
  });
  check(second.status === 200, "seed commit created", second.status);

  // The fork dialog is only offered for a user message, so the probe needs one.
  // The agent may run for a while; it is stopped when the probe deletes the
  // project, and nothing here waits for the reply.
  const sent = await api(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `fixterm-${stamp}`,
    text: "reply with the single word ok",
    streaming_behavior: "follow_up",
  });
  check(sent.status < 300, "probe turn accepted", sent.status);

  await page.goto(record.projectUrl);
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 40_000 });
  await page.getByTestId("panel-tab-terminal").click();
  await page
    .getByTestId("terminal-pane")
    .first()
    .waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForFunction(
    () => document.querySelector('[data-testid="terminal-pane"]')?.getAttribute("data-pty-status") === "open",
    undefined,
    { timeout: 30_000 },
  );
  await waitPrompt(page);
  await page.locator('[data-testid="terminal-tab-panel"]:not(.hidden) .xterm-helper-textarea').focus();

  // ---- 1a. zero-delay burst on an open socket -------------------------------
  await type(page, "echo AAAA-BBBB; echo EXIT=$?", 0);
  await page.waitForTimeout(1500);
  {
    const text = await rows(page);
    record.checks.burst = { ok: text.includes("AAAA-BBBB") && text.includes("EXIT=0"), text: text.slice(-600) };
    write("verify-burst.txt", text);
    check(record.checks.burst.ok, "zero-delay burst arrived complete (AAAA-BBBB, EXIT=0)", record.checks.burst.text);
    await shot(page, "verify-1-burst");
  }

  // ---- 1b. typing that starts before the socket opens ------------------------
  {
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Network.enable");
    await cdp.send("Network.emulateNetworkConditions", {
      offline: false,
      latency: 1500,
      downloadThroughput: -1,
      uploadThroughput: -1,
    });
    await page.getByTestId("terminal-tab-add").click();
    const textarea = page.locator('[data-testid="terminal-tab-panel"]:not(.hidden) .xterm-helper-textarea');
    await textarea.waitFor({ state: "attached", timeout: 30_000 });
    await textarea.focus();
    const marker = `PRECONNECT-${stamp.toString(36).toUpperCase()}`;
    await page.keyboard.type(`echo ${marker}; echo EXIT=$?`, { delay: 10 });
    await page.keyboard.press("Enter");
    await cdp.send("Network.emulateNetworkConditions", {
      offline: false,
      latency: 0,
      downloadThroughput: -1,
      uploadThroughput: -1,
    });
    await page.waitForTimeout(5000);
    const text = await rows(page);
    record.checks.preConnect = { marker, ok: text.includes(marker) && text.includes("EXIT=0"), text: text.slice(-800) };
    write("verify-preconnect.txt", text);
    check(record.checks.preConnect.ok, "typing before the socket opened was buffered and ran", record.checks.preConnect.text);
    await shot(page, "verify-2-preconnect");
  }

  // ---- 2. git log must print and return the prompt --------------------------
  {
    await page.getByTestId("terminal-tab").first().click();
    await waitPrompt(page);
    await clearTerminal(page);
    await page.locator('[data-testid="terminal-tab-panel"]:not(.hidden) .xterm-helper-textarea').focus();
    await type(page, "git log --oneline", 10);
    await page.waitForTimeout(2000);
    const paged = await rows(page);
    write("verify-git-log-paged.txt", paged);
    await shot(page, "verify-3-git-log");
    const showsPager = paged.includes("(END)");
    // A command typed now must reach the shell, not the pager.
    await type(page, "echo AFTER-GIT-LOG", 0);
    await page.waitForTimeout(2500);
    const after = await rows(page);
    write("verify-git-log-after.txt", after);
    record.checks.gitLog = {
      printed: after.includes("first commit") && after.includes("second commit"),
      pager: showsPager,
      returned: after.includes("AFTER-GIT-LOG") && !after.includes("(END)"),
      text: after.slice(-1200),
    };
    check(record.checks.gitLog.printed, "git log printed both commits", record.checks.gitLog.text.slice(-300));
    check(!record.checks.gitLog.pager, "git log did not start a pager", record.checks.gitLog.text.slice(-300));
    check(record.checks.gitLog.returned, "git log returned to the prompt (AFTER-GIT-LOG ran)");
    await shot(page, "verify-4-git-log-after");
  }

  // ---- 2b. a large git diff must not paginate -------------------------------
  {
    const big = Array.from({ length: 300 }, (_, i) => `line ${i + 1}`).join("\n") + "\n";
    await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=big.txt`, big);
    await api(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
      message: "big file",
      paths: ["big.txt"],
    });
    const changed = Array.from({ length: 420 }, (_, i) => `line ${i + 1} changed`).join("\n") + "\n";
    await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=big.txt`, changed);
    await waitPrompt(page);
    await clearTerminal(page);
    await page.locator('[data-testid="terminal-tab-panel"]:not(.hidden) .xterm-helper-textarea').focus();
    await type(page, "git diff --stat; git diff | tail -3", 5);
    await page.waitForTimeout(2500);
    const text = await rows(page);
    write("verify-git-diff.txt", text);
    await type(page, "echo AFTER-GIT-DIFF", 0);
    await page.waitForTimeout(2000);
    const after = await rows(page);
    record.checks.gitDiff = {
      pager: text.includes("(END)"),
      returned: after.includes("AFTER-GIT-DIFF") && !after.includes("(END)"),
      text: after.slice(-1200),
    };
    check(!record.checks.gitDiff.pager, "a large git diff did not start a pager", record.checks.gitDiff.text.slice(-300));
    check(record.checks.gitDiff.returned, "git diff returned to the prompt (AFTER-GIT-DIFF ran)");
    await shot(page, "verify-5-git-diff");
  }

  // ---- 3. the fork dialog explains the required name ------------------------
  try {
    // Fork points come from user messages; wait until the server lists one
    // (the transcript and the points endpoint are written before the reply).
    await page.waitForFunction(
      async (sid) => {
        const res = await fetch(`/v1/sessions/${sid}/fork-points`, { credentials: "same-origin" });
        if (!res.ok) return false;
        const body = await res.json();
        return Array.isArray(body.points) && body.points.length > 0;
      },
      sessionId,
      { timeout: 60_000 },
    );
    await page.getByRole("button", { name: "分支", exact: true }).click();
    const fork = page.getByRole("button", { name: "从这里分叉" }).first();
    await fork.waitFor({ timeout: 30_000 });
    await fork.click();
    const hint = page.getByTestId("fork-label-hint");
    const submit = page.getByTestId("fork-submit");
    await hint.waitFor({ timeout: 10_000 });
    await shot(page, "verify-6-fork-hint");
    record.checks.forkHint = { text: (await hint.textContent()) ?? "", disabled: await submit.isDisabled() };
    check(
      record.checks.forkHint.text.includes("必填") && record.checks.forkHint.disabled,
      "the fork dialog says the name is required while 创建分支 is disabled",
      record.checks.forkHint,
    );
    await page.getByTestId("fork-label").fill(`verify-${stamp}`);
    check(!(await submit.isDisabled()), "the button enables once a name is typed");
    await page.keyboard.press("Escape");
  } catch (error) {
    check(false, "the fork dialog hint is present", String(error));
    await shot(page, "verify-6-fork-hint-missing");
  }
} finally {
  save();
  if (!KEEP && record.projectId) {
    const removed = await api(page, "DELETE", `/v1/projects/${record.projectId}`);
    console.log("[cleanup] project delete", removed.status);
  }
  await browser.close();
}
record.failures = failures;
save();
console.log(failures.length === 0 ? "[PASS] all terminal-input checks" : `[FAIL] ${failures.length} check(s)`);
process.exitCode = failures.length === 0 ? 0 : 1;
