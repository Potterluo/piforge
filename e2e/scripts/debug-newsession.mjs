// Diagnostic: walk the exact openNewSession path and report each step so a hang
// can be attributed instead of guessed.
import { chromium } from "@playwright/test";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
const step = (name, extra = "") => console.log(`[step] ${name}`, extra);

page.on("console", (m) => {
  if (m.type() === "error") console.log("[console:error]", m.text());
});
page.on("pageerror", (e) => console.log("[pageerror]", e.message));

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 20000 });
step("logged in", page.url());

await page.goto(`${BASE}/`);
await page.waitForTimeout(1500);
step("home", `cards=${await page.getByTestId("project-card").count()} newBtn=${await page.getByTestId("new-project-button").count()}`);

let card = page.getByTestId("project-card").first();
if ((await card.count()) === 0) {
  await page.getByTestId("new-project-button").click();
  await page.getByTestId("project-name").fill("dbg-proj");
  await page.getByTestId("project-submit").click();
  await page.waitForTimeout(1500);
  step("project created", `cards=${await page.getByTestId("project-card").count()}`);
  card = page.getByTestId("project-card").first();
}

const info = await page.locator('[data-testid="project-card"]').evaluateAll((els) =>
  els.map((el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    const chain = [];
    let p = el;
    for (let i = 0; i < 6 && p; i += 1) {
      const s = getComputedStyle(p);
      chain.push(`${p.tagName}.${p.className?.toString().slice(0, 30)} display=${s.display} vis=${s.visibility} cv=${s.contentVisibility} h=${Math.round(p.getBoundingClientRect().height)}`);
      p = p.parentElement;
    }
    return { w: Math.round(r.width), h: Math.round(r.height), display: cs.display, visibility: cs.visibility, chain };
  }),
);
step("card geometry", JSON.stringify(info, null, 1));
await card.click({ force: true });
await page.waitForTimeout(1500);
step("project page", page.url());

const newSessionButtons = await page.getByTestId("new-session-button").count();
step("new-session buttons", String(newSessionButtons));

await page.getByTestId("new-session-button").first().click();
await page.waitForTimeout(1200);
const dialogVisible = await page.getByTestId("session-submit").count();
step("dialog submit present", String(dialogVisible));
if (dialogVisible > 0) {
  const disabled = await page.getByTestId("session-submit").isDisabled();
  step("submit disabled", String(disabled));
  await page.getByTestId("session-submit").click({ force: true });
  await page.waitForTimeout(3000);
  step("after submit", page.url());
  step("workbench present", String(await page.locator("[data-session-id]").count()));
  const body = await page.locator("body").innerText();
  step("body head", body.slice(0, 220).replace(/\n/g, " | "));
}

await browser.close();
