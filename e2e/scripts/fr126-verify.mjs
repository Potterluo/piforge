// FR-12.6 verification against the deployed container.
//
// The forge is a fake HTTP server on the host; the project's forge config points
// the *container* at it through host.docker.internal, which is the same field a
// real deployment fills with https://api.github.com. The git remote is a bare
// repository in the container's /data volume, so the push is real git over real
// refs. The UI is the product's own dialog — nothing here drives the flow around
// it.
//
// Usage (deployed app on 127.0.0.1:7722, container name pf-local):
//   node e2e/scripts/fr126-verify.mjs
//
// Outputs: e2e/.artifacts/fr126/*.png and probe-log.json.
import { execFileSync } from "node:child_process";
import http from "node:http";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.resolve(here, "../.artifacts/fr126");
const APP = process.env.PIFORGE_APP ?? "http://127.0.0.1:7722";
const FORGE_PORT = Number(process.env.FR126_FORGE_PORT ?? 7799);
const CONTAINER = process.env.FR126_CONTAINER ?? "pf-local";
const EMAIL = process.env.PIFORGE_E2E_USER_EMAIL ?? "dev@piforge.local";
const PASSWORD = process.env.PIFORGE_E2E_USER_PASSWORD ?? "piforge-dev-123";

mkdirSync(outDir, { recursive: true });

const checks = [];
const forgeRequests = [];
const shots = [];
function check(ok, name, detail = "") {
  checks.push({ ok: Boolean(ok), name, detail });
  console.log(`${ok ? "PASS" : "FAIL"} ${name}${detail ? ` — ${detail}` : ""}`);
}
function json(res, status, body) {
  const raw = JSON.stringify(body);
  res.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(raw) });
  res.end(raw);
}

// --- fake forge ------------------------------------------------------------
const forge = {
  pulls: [],
  next: 1,
  failStatus: 0,
  failBody: "",
  posts: 0,
  auth: [],
};

const forgeServer = http.createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://forge.local");
  const entry = {
    method: req.method,
    path: url.pathname,
    query: url.search,
    auth: req.headers.authorization ?? "",
  };
  forgeRequests.push(entry);
  if (req.method === "GET") {
    const rawHead = url.searchParams.get("head") ?? "";
    const head = rawHead.includes(":") ? rawHead.slice(rawHead.indexOf(":") + 1) : rawHead;
    const base = url.searchParams.get("base") ?? "";
    json(
      res,
      200,
      forge.pulls.filter((pr) => pr.head === head && pr.base === base),
    );
    return;
  }
  if (req.method !== "POST") {
    json(res, 405, { message: "method not allowed" });
    return;
  }
  let raw = "";
  req.on("data", (chunk) => (raw += chunk));
  req.on("end", () => {
    forge.posts += 1;
    forge.auth.push(entry.auth);
    if (forge.failStatus) {
      json(res, forge.failStatus, { message: forge.failBody });
      return;
    }
    const body = JSON.parse(raw || "{}");
    const head = String(body.head ?? "").replace(/^[^:]+:/, "");
    const pr = {
      number: forge.next++,
      url: `http://127.0.0.1:${FORGE_PORT}/acme/widgets/pull/${forge.next - 1}`,
      title: body.title,
      head,
      base: body.base,
    };
    forge.pulls.push(pr);
    json(res, 201, {
      number: pr.number,
      html_url: pr.url,
      title: pr.title,
      head: { ref: pr.head },
      base: { ref: pr.base },
    });
  });
});

async function listen(server, port) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "0.0.0.0", resolve);
  });
}

// --- container helpers -----------------------------------------------------
function containerShell(script) {
  return execFileSync("docker", ["exec", CONTAINER, "sh", "-lc", script], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
}

/**
 * A bare repository with main plus one feature branch, checked out by default.
 * The deployed control plane clones this path from inside the container, so the
 * push in the dialog is a real push to a real remote.
 */
function makeOrigin(slug, branch) {
  const dir = `/data/fr126/${slug}`;
  containerShell(
    [
      `rm -rf ${dir}`,
      `mkdir -p ${dir}/seed`,
      `cd ${dir}`,
      `git init -q --bare -b main origin.git`,
      `git clone -q origin.git seed`,
      `cd seed`,
      `printf 'seed\\n' > README.md`,
      `git add -A`,
      `git -c user.name=Seed -c user.email=seed@piforge.local commit -qm seed`,
      `git push -q origin main`,
      `git checkout -qb ${branch}`,
      `printf '${branch}\\n' > ${branch}.txt`,
      `git add -A`,
      `git -c user.name=Seed -c user.email=seed@piforge.local commit -qm 'add ${branch}'`,
      `git push -q -u origin ${branch}`,
      `git --git-dir=${dir}/origin.git symbolic-ref HEAD refs/heads/${branch}`,
    ].join(" && "),
  );
  return `${dir}/origin.git`;
}

// --- browser / API helpers -------------------------------------------------
async function api(page, method, route, data) {
  const cookies = await page.context().cookies();
  const csrf = cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
  const res = await page.request.fetch(`${APP}${route}`, {
    method,
    headers: {
      "X-PF-CSRF": csrf,
      ...(typeof data === "string" ? { "content-type": "text/plain; charset=utf-8" } : {}),
    },
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

async function login(page) {
  await page.goto(`${APP}/login`);
  await page.getByTestId("login-email").fill(EMAIL);
  await page.getByTestId("login-password").fill(PASSWORD);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ state: "visible", timeout: 30_000 });
}

async function shot(page, name) {
  const file = path.join(outDir, `${name}.png`);
  await page.screenshot({ path: file });
  shots.push(path.relative(path.resolve(here, "../.."), file).replaceAll("\\", "/"));
  return file;
}

async function waitForWorktree(page, sessionId) {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const res = await api(page, "GET", `/v1/sessions/${sessionId}`);
    const session = res.body?.session;
    if (session?.worktree_id) return session;
    await page.waitForTimeout(500);
  }
  throw new Error(`session ${sessionId} never got a worktree`);
}

/**
 * One project on its own bare origin, on a branch that is not the PR base.
 * `title` becomes the session title, which is the dialog's prefill fallback
 * (there is no model turn in this probe, so no first message exists).
 */
async function provisionProject(page, { name, slug, branch, title, docsText, commitMessage }) {
  const repoURL = makeOrigin(slug, branch);
  const project = await api(page, "POST", "/v1/projects", {
    name,
    repo_url: repoURL,
    default_branch: branch,
  });
  if (project.status >= 300) throw new Error(`create project: ${project.status} ${JSON.stringify(project.body)}`);
  const projectId = project.body.project.id;

  const created = await api(page, "POST", `/v1/projects/${projectId}/sessions`, { title });
  if (created.status >= 300) throw new Error(`create session: ${created.status} ${JSON.stringify(created.body)}`);
  const sessionId = created.body.session.id;
  await waitForWorktree(page, sessionId);

  const file = "NOTES.md";
  const wrote = await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=${file}`, `${docsText}\n`);
  if (wrote.status >= 300) throw new Error(`write file: ${wrote.status} ${JSON.stringify(wrote.body)}`);
  const commit = await api(page, "POST", `/v1/sessions/${sessionId}/git/commit`, {
    message: commitMessage,
    paths: [file],
  });
  if (commit.status !== 200) throw new Error(`commit: ${commit.status} ${JSON.stringify(commit.body)}`);

  const forgeConfig = await api(page, "PUT", `/v1/projects/${projectId}/forge`, {
    kind: "github",
    api_base: `http://host.docker.internal:${FORGE_PORT}`,
    repo: "acme/widgets",
    base_branch: "main",
    token: "fr126-token",
  });
  if (forgeConfig.status !== 200) {
    throw new Error(`configure forge: ${forgeConfig.status} ${JSON.stringify(forgeConfig.body)}`);
  }

  return { projectId, sessionId, branch, repoURL };
}

async function main() {
  await listen(forgeServer, FORGE_PORT);

  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

  try {
    await login(page);

    // ---- happy path -------------------------------------------------------
    const stamp = Date.now();
    const happy = await provisionProject(page, {
      name: `fr126-happy-${stamp}`,
      slug: "happy",
      branch: "feature",
      title: "给 feature 分支加一个说明文件",
      docsText: "这些改动来自 PiForge 会话",
      commitMessage: "add notes",
    });

    await page.goto(`${APP}/p/${happy.projectId}/s/${happy.sessionId}`);
    await page.locator(`[data-session-id="${happy.sessionId}"]`).waitFor({ timeout: 40_000 });
    await page.getByTestId("panel-tab-changes").click();
    const openButton = page.getByTestId("git-create-pr");
    await openButton.waitFor({ state: "visible", timeout: 30_000 });
    await openButton.click();

    await page.getByTestId("create-pr-title").waitFor({ state: "visible", timeout: 30_000 });
    const branchText = await page.getByTestId("create-pr-branch").innerText();
    const commitsText = await page.getByTestId("create-pr-commits").innerText();
    const files = await page.getByTestId("create-pr-file").allInnerTexts();
    const titleValue = await page.getByTestId("create-pr-title").inputValue();
    const bodyValue = await page.getByTestId("create-pr-body").inputValue();
    check(branchText.includes("feature") && branchText.includes("main"), "preview names head and base", branchText);
    check(commitsText.includes("2"), "preview counts the commits", commitsText);
    check(
      files.some((line) => line.includes("feature.txt")) && files.some((line) => line.includes("NOTES.md")),
      "preview lists the files the PR would carry",
      files.join(" | "),
    );
    check(titleValue === "给 feature 分支加一个说明文件", "title prefilled from the session", titleValue);
    check(bodyValue === "add notes", "body prefilled from the latest commit", bodyValue);
    await shot(page, "fr126-preview");

    const newTitle = "FR-12.6: 给 feature 分支加一个说明文件";
    await page.getByTestId("create-pr-title").fill(newTitle);
    await page.getByTestId("create-pr-submit").click();

    const link = page.getByTestId("create-pr-link");
    await link.waitFor({ state: "visible", timeout: 60_000 });
    const href = await link.getAttribute("href");
    check(Boolean(href && href.includes("/pull/")), "the confirmation shows the PR URL as a link", href ?? "");
    check(
      (await page.getByTestId("create-pr-status").innerText()).includes("已创建"),
      "the confirmation says the PR was created",
    );
    await shot(page, "fr126-created");

    check(forge.posts === 1, "the forge received exactly one create call", `posts=${forge.posts}`);
    check(forge.auth.at(-1) === "Bearer fr126-token", "the forge saw the token as a bearer header", forge.auth.at(-1) ?? "");
    check(
      forgeRequests.every((r) => !r.query.includes("fr126-token")),
      "the token never appears in a query string",
      forgeRequests.map((r) => r.query).join(" | "),
    );
    const createdPull = forge.pulls[0];
    check(createdPull?.title === newTitle, "the forge got the edited title", createdPull?.title ?? "");
    check(createdPull?.head === "feature" && createdPull?.base === "main", "the forge got head/base", `${createdPull?.head} → ${createdPull?.base}`);

    const remoteLog = containerShell(
      `git --git-dir=/data/fr126/happy/origin.git log --format=%s feature`,
    );
    check(remoteLog.includes("add notes"), "the branch really exists on the remote", remoteLog.trim().split("\n")[0] ?? "");

    // Idempotency: reopen and the dialog shows the existing PR, no second POST.
    await page.getByTestId("create-pr-close").click();
    await page.getByTestId("git-create-pr").click();
    await page.getByTestId("create-pr-existing").waitFor({ state: "visible", timeout: 30_000 });
    const againHref = await page.getByTestId("create-pr-link").getAttribute("href");
    check(againHref === href, "reopening shows the same PR instead of creating a second", againHref ?? "");
    check(forge.posts === 1, "no second create call was made", `posts=${forge.posts}`);
    await shot(page, "fr126-existing");
    await page.getByTestId("create-pr-close").click();

    // ---- the forge refuses ------------------------------------------------
    const failing = await provisionProject(page, {
      name: `fr126-422-${stamp}`,
      slug: "rejected",
      branch: "feature2",
      title: "会被代码托管平台拒绝的 PR",
      docsText: "第二次尝试",
      commitMessage: "second attempt",
    });
    forge.failStatus = 422;
    forge.failBody = "Validation Failed: base branch main is protected";

    await page.goto(`${APP}/p/${failing.projectId}/s/${failing.sessionId}`);
    await page.locator(`[data-session-id="${failing.sessionId}"]`).waitFor({ timeout: 40_000 });
    await page.getByTestId("panel-tab-changes").click();
    await page.getByTestId("git-create-pr").click();
    await page.getByTestId("create-pr-title").waitFor({ state: "visible", timeout: 30_000 });
    await page.getByTestId("create-pr-submit").click();

    const errorBox = page.getByTestId("create-pr-error");
    await errorBox.waitFor({ state: "visible", timeout: 60_000 });
    const step = await page.getByTestId("create-pr-error-step").innerText();
    const message = await page.getByTestId("create-pr-error-message").innerText();
    const forgeStatus = await page.getByTestId("create-pr-forge-status").innerText();
    check(step.includes("第 3 步") && step.includes("创建"), "the failure names the create step", step);
    check(forgeStatus.includes("422"), "the forge's HTTP status is shown", forgeStatus);
    check(
      message.includes("Validation Failed: base branch main is protected"),
      "the forge's own message is preserved",
      message,
    );
    check((await page.getByTestId("create-pr-link").count()) === 0, "no PR link is shown for a failed create");
    await shot(page, "fr126-error");

    const log = {
      app: APP,
      forge: `http://host.docker.internal:${FORGE_PORT}`,
      checks,
      forgeRequests,
      forgePulls: forge.pulls,
      screenshots: shots,
      ok: checks.every((c) => c.ok),
    };
    writeFileSync(path.join(outDir, "probe-log.json"), JSON.stringify(log, null, 2));
    const failed = checks.filter((c) => !c.ok);
    if (failed.length > 0) {
      throw new Error(`${failed.length} check(s) failed: ${failed.map((f) => f.name).join("; ")}`);
    }
    console.log(`\nFR-12.6 probe passed (${checks.length} checks). Evidence in ${outDir}`);
  } finally {
    await browser.close();
    forgeServer.close();
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
