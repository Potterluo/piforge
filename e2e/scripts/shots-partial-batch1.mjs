/**
 * Screenshots for the three PARTIAL rows finished in this batch, taken against
 * the *deployed* instance (http://127.0.0.1:7722), not the fake-peer e2e server.
 *
 *   node scripts/shots-partial-batch1.mjs
 *
 * It creates one throw-away project/session and two imported skills, writes the
 * screenshots under e2e/.artifacts/partial-batch1/, then deletes what it created.
 * Nothing here is a test: the assertions live in tests/69-71 and run on every
 * suite. This exists so a reviewer can see the three screens.
 */
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { crc32 } from "node:zlib";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const BASE = process.env.PIFORGE_BASE ?? "http://127.0.0.1:7722";
const OUT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../.artifacts/partial-batch1");
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };

function zip(files) {
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const file of files) {
    const name = Buffer.from(file.path, "utf8");
    const data = Buffer.from(file.body, "utf8");
    const sum = crc32(data);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0x21, 12);
    local.writeUInt32LE(sum, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    locals.push(local, name, data);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt16LE(0, 12);
    central.writeUInt16LE(0x21, 14);
    central.writeUInt32LE(sum, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt16LE(0, 30);
    central.writeUInt16LE(0, 32);
    central.writeUInt16LE(0, 34);
    central.writeUInt16LE(0, 36);
    central.writeUInt32LE(0, 38);
    central.writeUInt32LE(offset, 42);
    centrals.push(central, name);
    offset += local.length + name.length + data.length;
  }
  const directory = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  end.writeUInt16LE(0, 20);
  return Buffer.concat([...locals, directory, end]);
}

async function api(page, method, url, data) {
  const cookies = await page.context().cookies();
  const csrf = cookies.find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
  const headers = { "X-PF-CSRF": csrf };
  if (typeof data === "string") headers["Content-Type"] = "text/plain; charset=utf-8";
  const response = await page.request.fetch(`${BASE}${url}`, { method, headers, data });
  const body = await response.json().catch(() => null);
  if (!response.ok()) throw new Error(`${method} ${url} -> ${response.status()} ${JSON.stringify(body)}`);
  return body;
}

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
let projectId = null;
const importedSkillIds = [];

try {
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill(USER.password);
  await page.getByTestId("login-submit").click();
  await page.getByTestId("app-sidebar").waitFor({ timeout: 30_000 });

  // A project/session of our own, so the screenshots never depend on whatever
  // work the deployment already holds.
  const created = await api(page, "POST", "/v1/projects", {
    name: `shots-batch1-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  projectId = created.project.id;
  const session = await api(page, "POST", `/v1/projects/${projectId}/sessions`, {
    model_ref: "",
  });
  const sessionId = session.session.id;
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=docs%2Fguide.md`, "");
  const body = ["# Guide", "", "this line holds piforge-content-marker", "", "the end", ""].join("\n");
  await api(page, "PUT", `/v1/sessions/${sessionId}/files?path=docs%2Fguide.md`, body);

  // --- FR-14.4: the zip upload, and a disabled skill -----------------------
  await page.goto(`${BASE}/settings/skills`);
  await page.getByTestId("skill-item").first().waitFor({ timeout: 30_000 });
  const dir = mkdtempSync(path.join(tmpdir(), "pf-shots-zip-"));
  const zipPath = path.join(dir, "skills.zip");
  writeFileSync(
    zipPath,
    zip([
      { path: "collection/pdf-tools/SKILL.md", body: "---\nname: PDF\n---\nRead and split PDFs.\n" },
      { path: "collection/pdf-tools/scripts/split.py", body: "print('split')\n" },
      { path: "collection/review/SKILL.md", body: "Review diffs carefully.\n" },
    ]),
  );
  await page.getByTestId("skill-import").click();
  await page.getByTestId("skill-import-zip").setInputFiles(zipPath);
  await page.screenshot({ path: path.join(OUT, "fr-14.4-zip-dialog.png") });
  await page.getByTestId("skill-import-zip-submit").click();

  const pdf = page.locator('[data-testid="skill-item"][data-name="pdf-tools"]');
  await pdf.waitFor({ timeout: 30_000 });
  const skills = await api(page, "GET", "/v1/skills");
  for (const skill of skills.skills) {
    // Only the two this script imported: the deployment's built-in skills must
    // survive the cleanup.
    if (skill.name === "pdf-tools" || skill.name === "review") importedSkillIds.push(skill.id);
  }
  await pdf.getByRole("switch", { name: "启用 pdf-tools" }).click();
  await pdf.getByText("已停用").waitFor({ timeout: 20_000 });
  await page.screenshot({ path: path.join(OUT, "fr-14.4-imported-and-disabled.png") });

  // --- FR-6.6: content search and the line it lands on ---------------------
  await page.goto(`${BASE}/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("workspace-panels").waitFor({ timeout: 30_000 });
  await page.keyboard.press("Control+Shift+F");
  await page.getByTestId("quick-open-input").fill("piforge-content-marker");
  const result = page.getByTestId("quick-open-result").first();
  await result.waitFor({ timeout: 20_000 });
  await page.screenshot({ path: path.join(OUT, "fr-6.6-content-results.png") });
  await result.click();
  await page.locator('[data-testid="code-preview"][data-active-line="3"]').waitFor({ timeout: 20_000 });
  await page.screenshot({ path: path.join(OUT, "fr-6.6-line-jump.png") });

  // --- FR-10.2: five terminals, one renamed -------------------------------
  await page.getByTestId("panel-tab-terminal").click();
  await page.getByTestId("terminal-pane").first().waitFor({ timeout: 30_000 });
  for (let index = 0; index < 4; index += 1) {
    await page.getByTestId("terminal-tab-add").click();
  }
  await page.locator('[data-testid="terminal-tab"][data-tab-id="t2"]').dblclick();
  await page.getByTestId("terminal-tab-rename").fill("构建");
  await page.getByTestId("terminal-tab-rename").press("Enter");
  await page.locator('[data-testid="terminal-tab"][data-tab-id="t3"]').click();
  await page.locator('[data-testid="terminal-pane"][data-pty-tab="t3"]').waitFor({ timeout: 30_000 });
  const ptyOpen = await page
    .locator('[data-testid="terminal-pane"]')
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-pty-status")));
  if (ptyOpen.filter((status) => status === "open").length < 3) {
    throw new Error(`only ${ptyOpen.filter((status) => status === "open").length} PTYs opened: ${ptyOpen}`);
  }
  await page.screenshot({ path: path.join(OUT, "fr-10.2-terminal-tabs.png") });

  console.log(`screenshots written to ${OUT}`);
} finally {
  // Clean up the throw-away project and the imported skills, whatever happened.
  try {
    if (projectId) await api(page, "DELETE", `/v1/projects/${projectId}`);
  } catch (error) {
    console.error("cleanup: project", error.message);
  }
  for (const id of importedSkillIds) {
    try {
      await api(page, "DELETE", `/v1/skills/${id}`);
    } catch {
      /* already gone */
    }
  }
  await browser.close();
}
