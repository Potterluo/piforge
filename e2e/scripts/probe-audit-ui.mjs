// Read the audit row back through the admin UI, not only the API.
//
// Invoked by scripts/probe-audit.ps1 after the API probe has already asserted
// the row's fields. This half proves the page an operator actually opens
// renders it: the row, its preview, the model/commit line and the chain id.
//
//   node e2e/scripts/probe-audit-ui.mjs <base> <cookie-file> <row-id> <artifact-dir>
import fs from "node:fs";
import path from "node:path";
import { chromium } from "@playwright/test";

const [baseArg, cookieFile, rowId, artifactDir] = process.argv.slice(2);
if (!baseArg || !cookieFile || !rowId) {
  console.error("usage: probe-audit-ui.mjs <base> <cookie-file> <row-id> [artifact-dir]");
  process.exit(2);
}
const base = baseArg.replace(/\/$/, "");
const cookies = JSON.parse(fs.readFileSync(cookieFile, "utf8"));
const host = new URL(base).hostname;

const browser = await chromium.launch();
try {
  const context = await browser.newContext();
  await context.addCookies(
    cookies.map((cookie) => ({ ...cookie, domain: host })),
  );
  const page = await context.newPage();
  await page.goto(`${base}/admin/audit`, { waitUntil: "domcontentloaded" });
  await page.waitForSelector('[data-testid="admin-audit"]', { timeout: 30000 });

  const row = page.locator(`[data-testid="audit-row-${rowId}"]`);
  await row.waitFor({ timeout: 30000 });
  const text = (await row.innerText()).replace(/\s+/g, " ").trim();
  const preview = page.locator(`[data-testid="audit-preview-${rowId}"]`);
  const previewText = (await preview.count()) > 0 ? (await preview.first().innerText()).trim() : "";

  console.log(`UI ROW ${rowId}: ${text}`);
  console.log(`UI PREVIEW ${rowId}: ${previewText}`);
  if (!text) {
    console.error("the row rendered empty");
    process.exit(1);
  }
  if (!previewText) {
    console.error("the row has no preview on screen");
    process.exit(1);
  }

  if (artifactDir) {
    fs.mkdirSync(artifactDir, { recursive: true });
    const shot = path.join(artifactDir, `audit-ui-${Date.now()}.png`);
    await page.screenshot({ path: shot, fullPage: false });
    console.log(`UI SCREENSHOT: ${shot}`);
  }
} finally {
  await browser.close();
}
