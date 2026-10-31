// Verification probe for the skill-import expansion cap (FR-5.8), run against
// the deployed app (default :7722, the pf-local container).
//
//   node e2e/scripts/skill-cap-probe.mjs [baseURL] [container]
//
// It uploads an archive whose entries are small enough to pass the per-file and
// per-bundle caps but whose expanded total is over the deployment's 32 MiB
// import cap. The import must be refused with a message that names the cap and
// must not leave a bundle directory or a skill row behind.
//
// Evidence lands in e2e/.artifacts/fix-rows-quota/.
import { chromium } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { deflateRawSync } from "node:zlib";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../.artifacts/fix-rows-quota");
mkdirSync(out, { recursive: true });

const BASE = (process.argv[2] ?? process.env.BASE ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const CONTAINER = process.argv[3] ?? process.env.PIFORGE_CONTAINER ?? "pf-local";
const USER = { email: "dev@piforge.local", password: "piforge-dev-123" };
const stamp = Date.now();

const record = { base: BASE, container: CONTAINER, stamp, at: new Date().toISOString(), checks: {}, findings: [] };
const save = () =>
  writeFileSync(path.join(out, `probe-skill-cap.json`), JSON.stringify(record, null, 2));
const note = (key, value) => {
  record.checks[key] = value;
  console.log(`[${key}]`, JSON.stringify(value));
};
const ok = (condition, message) => {
  console.log(condition ? "[ok]" : "[BROKEN]", message);
  record.findings.push({ ok: Boolean(condition), message });
  return Boolean(condition);
};

/** A minimal store-method zip: local header, data, central directory, EOCD. */
function buildZip(entries) {
  const locals = [];
  const central = [];
  let offset = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry.name, "utf8");
    const body = Buffer.alloc(entry.size, 0x61); // 'a'
    const deflated = deflateRawSync(body);
    const crcTable = [];
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      crcTable[n] = c >>> 0;
    }
    let crc = 0xffffffff;
    for (const byte of body) crc = crcTable[(crc ^ byte) & 0xff] ^ (crc >>> 8);
    crc = (crc ^ 0xffffffff) >>> 0;

    const local = Buffer.alloc(30 + name.length);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0, 6);
    local.writeUInt16LE(8, 8); // deflate
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0, 12);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(deflated.length, 18);
    local.writeUInt32LE(body.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    name.copy(local, 30);
    locals.push(local, deflated);

    const centralHeader = Buffer.alloc(46 + name.length);
    centralHeader.writeUInt32LE(0x02014b50, 0);
    centralHeader.writeUInt16LE(20, 4);
    centralHeader.writeUInt16LE(20, 6);
    centralHeader.writeUInt16LE(0, 8);
    centralHeader.writeUInt16LE(8, 10);
    centralHeader.writeUInt32LE(crc, 16);
    centralHeader.writeUInt32LE(deflated.length, 20);
    centralHeader.writeUInt32LE(body.length, 24);
    centralHeader.writeUInt16LE(name.length, 28);
    centralHeader.writeUInt32LE(offset, 42);
    name.copy(centralHeader, 46);
    central.push(centralHeader);

    offset += local.length + deflated.length;
  }
  const centralStart = offset;
  const centralBytes = Buffer.concat(central);
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(entries.length, 8);
  eocd.writeUInt16LE(entries.length, 10);
  eocd.writeUInt32LE(centralBytes.length, 12);
  eocd.writeUInt32LE(centralStart, 16);
  return Buffer.concat([...locals, centralBytes, eocd]);
}

function listSkillsDir() {
  try {
    return execFileSync("docker", ["exec", CONTAINER, "sh", "-lc", "ls -1 /data/skills 2>/dev/null || true"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return "";
  }
}

const browser = await chromium.launch({ channel: "chrome" });
try {
  const page = await browser.newPage();
  await page.goto(`${BASE}/login`);
  await page.getByTestId("login-email").fill(USER.email);
  await page.getByTestId("login-password").fill(USER.password);
  const submit = page.getByTestId("login-submit");
  await submit.waitFor({ state: "visible", timeout: 30_000 });
  for (let i = 0; i < 40; i++) {
    if (await submit.isEnabled()) break;
    await page.waitForTimeout(250);
  }
  await submit.click();
  await page.getByTestId("app-sidebar").first().waitFor({ timeout: 30_000 });
  const csrf = (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "";

  // 40 entries of 1,000,000 bytes each: every file is under the 1 MiB per-file
  // cap, the archive is tiny on the wire (zeros deflate), and the expanded total
  // is 40,000,000 bytes — over the 32 MiB import cap.
  const archive = buildZip(
    Array.from({ length: 40 }, (_, index) => ({
      name: `bomb/part-${String(index).padStart(2, "0")}.bin`,
      size: 1_000_000,
    })),
  );
  note("archive_bytes", archive.length);
  note("expanded_bytes", 40 * 1_000_000);
  const before = listSkillsDir();
  note("skills_dir_before", before.split("\n").filter(Boolean).length);

  const res = await page.request.post(`${BASE}/v1/skills/import/zip`, {
    headers: { "X-PF-CSRF": csrf },
    multipart: {
      file: { name: "bomb.zip", mimeType: "application/zip", buffer: archive },
    },
    timeout: 120_000,
  });
  const body = await res.json().catch(() => null);
  note("import_status", res.status());
  note("import_body", body);
  const message = body?.error?.message ?? "";
  ok(res.status() === 400 && body?.error?.code === "invalid_argument",
    `the oversized import was refused (${res.status()}, ${body?.error?.code})`);
  ok(/32\.0 MiB/.test(message) && /上限/.test(message) && /没有导入任何文件/.test(message),
    "the refusal names the 32 MiB cap and says nothing was imported");

  const after = listSkillsDir();
  note("skills_dir_after", after.split("\n").filter(Boolean).length);
  ok(before === after, "no bundle directory was left behind by the refused import");
  save();
} catch (error) {
  record.error = String(error?.stack ?? error);
  console.error("[probe] failed:", error);
  save();
  process.exitCode = 1;
} finally {
  await browser.close();
}
