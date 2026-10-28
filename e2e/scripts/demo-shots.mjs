// Capture the product as a user sees it. Produces docs/screenshots/*.png.
//
// It seeds a small but real project first — files, a couple of commits, a fork —
// because an empty workbench photographs like a mockup.
import { chromium } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const OUT = path.resolve(process.env.OUT ?? "../docs/screenshots");
mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
page.on("pageerror", (e) => console.log("[pageerror]", e.message));

const shot = async (name, opts = {}) => {
  await page.waitForTimeout(opts.settle ?? 400);
  await page.screenshot({ path: path.join(OUT, `${name}.png`), fullPage: opts.full ?? false });
  console.log("shot", name);
};

const csrf = async () => {
  const cookies = await page.context().cookies();
  return cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
};
const abs = (u) => (u.startsWith("http") ? u : BASE + u);
const api = async (method, url, data) => {
  const headers = { "X-PF-CSRF": await csrf() };
  if (typeof data === "string") headers["Content-Type"] = "text/plain; charset=utf-8";
  const res = await page.request.fetch(abs(url), { method, headers, data });
  const text = await res.text();
  try {
    return { status: res.status(), body: JSON.parse(text) };
  } catch {
    return { status: res.status(), body: text };
  }
};
const runState = () =>
  page.evaluate(
    () => document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") ?? "",
  );
const waitState = (state, timeout) =>
  page.waitForFunction(
    (want) => document.querySelector('[data-testid="run-state"]')?.getAttribute("data-state") === want,
    state,
    { timeout },
  );

// ---------------------------------------------------------------- login
await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await shot("01-login");

await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 20000 });
await page.waitForTimeout(800);

// ---------------------------------------------------------------- seed a project
const listed = await (await page.request.get(abs("/v1/projects"))).json();
let projectId = (listed.projects ?? []).find((p) => p.name === "pi-forge")?.id;
if (!projectId) {
  const created = await api("POST", "/v1/projects", {
    name: "pi-forge",
    repo_url: "",
    default_branch: "main",
  });
  if (created.status >= 300) throw new Error(`create project: ${created.status}`);
  projectId = created.body.project.id;
}
const sessionsRes = await (await page.request.get(abs(`/v1/projects/${projectId}/sessions`))).json();
let sessionId = (sessionsRes.sessions ?? [])[0]?.id;
if (!sessionId) {
  const created = await api("POST", `/v1/projects/${projectId}/sessions`, {
    title: "修一下缓存淘汰的竞态",
  });
  sessionId = created.body.session.id;
}
console.log("project", projectId, "session", sessionId);

await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });

// ---------------------------------------------------------------- a run
await page.getByTestId("composer-input").fill("修一下缓存淘汰的竞态，先看看 lru 的实现");
await page.getByTestId("composer-send").click();
await waitState("running", 30000);
await shot("03-workbench-running", { settle: 600 });
await waitState("settled", 90000);
console.log("run:", await runState());

const trace = page.getByTestId("tool-trace").first();
if (await trace.count()) await trace.click().catch(() => {});
await shot("04-workbench-settled", { settle: 900 });

// ---------------------------------------------------------------- files
const files = {
  "README.md": `# pi-forge\n\nA multi-user coding-agent workbench.\n\n- Go control plane\n- Next.js + shadcn/ui workbench\n- Pi as the agent loop\n`,
  "internal/cache/lru.go": `package cache\n\nimport "sync"\n\n// LRU is a bounded cache with a doubly linked recency list.\ntype LRU struct {\n\tmu    sync.Mutex\n\titems map[string]*entry\n\tcap   int\n}\n\ntype entry struct {\n\tkey, value string\n\tprev, next *entry\n}\n\nfunc New(capacity int) *LRU {\n\treturn &LRU{items: make(map[string]*entry), cap: capacity}\n}\n\n// Get returns the value and marks it most recently used.\nfunc (c *LRU) Get(key string) (string, bool) {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\te, ok := c.items[key]\n\tif !ok {\n\t\treturn "", false\n\t}\n\treturn e.value, true\n}\n`,
  "internal/cache/lru_test.go": `package cache\n\nimport "testing"\n\nfunc TestEviction(t *testing.T) {\n\tc := New(2)\n\tc.Put("a", "1")\n\tc.Put("b", "2")\n\tif _, ok := c.Get("a"); !ok {\n\t\tt.Fatal("a should still be present")\n\t}\n}\n`,
  "docs/architecture.md": `# Architecture\n\n## Layers\n\n1. Pi runs the agent loop.\n2. The control plane owns sandboxes, worktrees and credentials.\n3. The workbench is a Next.js app using shadcn/ui agent primitives.\n\n## Sandbox granularity\n\nOne sandbox per (project, user). Worktrees inside it are per session.\n`,
  Makefile: `test:\n\tcd backend && go test ./...\n\tcd web && pnpm test\n\nlint:\n\tcd backend && go vet ./...\n`,
};
for (const [file, content] of Object.entries(files)) {
  const res = await api("PUT", `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(file)}`, content);
  if (res.status >= 300) console.log("write failed", file, res.status);
}

// Commits, so the graph has shape.
await api("POST", `/v1/sessions/${sessionId}/git/stage`, { paths: Object.keys(files) });
await api("POST", `/v1/sessions/${sessionId}/git/commit`, {
  message: "feat(cache): add an LRU with a recency list",
  paths: Object.keys(files),
});
await api(
  "PUT",
  `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("internal/cache/lru.go")}`,
  files["internal/cache/lru.go"].replace("\treturn e.value, true", "\treturn e.value, true // TODO: move to front"),
);
await api("POST", `/v1/sessions/${sessionId}/git/stage`, { paths: ["internal/cache/lru.go"] });
await api("POST", `/v1/sessions/${sessionId}/git/commit`, {
  message: "fix(cache): mark reads as most recent",
  paths: ["internal/cache/lru.go"],
});

// The tree is fetched per directory, so the open panel only knows about files
// that existed when it loaded.
await page.reload();
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.getByTestId("panel-tab-files").click();
await page.waitForTimeout(1200);
for (const dir of ["internal", "internal/cache"]) {
  const node = page.locator(`[data-testid="file-node"][data-path="${dir}"]`).first();
  if (await node.count()) {
    await node.click().catch(() => {});
    await page.waitForTimeout(400);
  }
}
await shot("05-file-tree");

const target = page.locator('[data-testid="file-node"][data-path="internal/cache/lru.go"]').first();
if (await target.count()) {
  await target.click();
  await page.waitForTimeout(1200);
}
await shot("06-file-preview");

// ---------------------------------------------------------------- git
await page.getByTestId("panel-tab-git").click();
await page.waitForTimeout(1500);
await shot("07-git-graph");

// ---------------------------------------------------------------- terminal
await page.getByTestId("panel-tab-terminal").click();
await page.waitForTimeout(1800);
await page.getByTestId("terminal-pane").click({ position: { x: 60, y: 40 } }).catch(() => {});
await page.keyboard.type("git log --oneline -3\r");
await page.waitForTimeout(1500);
await page.keyboard.type("Get-ChildItem -Name\r");
await page.waitForTimeout(1200);
await page.keyboard.type("git status --short\r");
await page.waitForTimeout(1200);
await shot("08-terminal");

// ---------------------------------------------------------------- browser
await page.getByTestId("panel-tab-browser").click();
await page.getByTestId("browser-panel").waitFor({ timeout: 20000 });
const painted = await page
  .waitForFunction(
    () => (document.querySelector('[data-testid="browser-canvas"]')?.width ?? 0) > 0,
    undefined,
    { timeout: 45000 },
  )
  .then(() => true)
  .catch(() => false);
console.log("browser painted:", painted);
// The sandbox browser starts on about:blank, which screenshots as a white
// rectangle. Point it at a real page so the panel shows what it is for.
await page.getByTestId("browser-url").fill("https://example.com");
await page.getByTestId("browser-url").press("Enter");
await page.waitForTimeout(4000);
await shot("09-browser");

// ---------------------------------------------------------------- fork
// A fork needs a live runtime, and a settled run may have been reaped, so warm
// it with one more turn before asking for fork points.
await page.getByTestId("composer-input").fill("再确认一下这个竞态");
await page.getByTestId("composer-send").click();
await waitState("settled", 90000);
const pointsRes = await page.request.get(abs(`/v1/sessions/${sessionId}/fork-points`));
const { points } = await pointsRes.json();
console.log("fork points:", points?.length ?? 0);
let forkId = null;
if (points?.length) {
  const forked = await api("POST", `/v1/sessions/${sessionId}/fork`, {
    entry_id: points[0].entry_id,
    label: "alt-approach",
  });
  console.log("fork:", forked.status);
  if (forked.status < 300) forkId = forked.body.session.id;
}

await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
await page.waitForTimeout(1500);
if (forkId) {
  await page.goto(`${BASE}/p/${projectId}/s/${forkId}`);
  await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
  await page.waitForTimeout(1800);
  await shot("10-fork-branch");
}

// ---------------------------------------------------------------- project + settings
await page.goto(`${BASE}/p/${projectId}`);
await page.waitForTimeout(1500);
await shot("02-project");

await page.goto(`${BASE}/settings/providers`);
await page.waitForTimeout(1200);
await shot("11-settings-providers");

await page.goto(`${BASE}/settings/skills`);
await page.waitForTimeout(1200);
await shot("12-settings-skills");

await page.goto(`${BASE}/settings/mcp`);
await page.waitForTimeout(1200);
await shot("13-settings-mcp");

await page.goto(`${BASE}/`);
await page.waitForTimeout(1200);
await shot("00-home");

await browser.close();
console.log("done");
