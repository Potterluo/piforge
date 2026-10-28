// Verify the three UI complaints: composer buttons, full sidebar collapse, and a
// terminal that shows a prompt.
import { chromium } from "@playwright/test";
import path from "node:path";

const BASE = process.env.BASE ?? "http://127.0.0.1:3100";
const OUT = path.resolve(process.env.OUT ?? "../docs/screenshots");

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
page.on("pageerror", (e) => console.log("[pageerror]", e.message));

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 20000 });

const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const projectId = listed.projects.find((p) => p.name === "pi-forge").id;
await page.goto(`${BASE}/p/${projectId}`);
await page.locator('a[href*="/s/"]').first().click();
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.waitForTimeout(800);

// 1. composer buttons: measure where they sit relative to the input box.
const box = await page.getByTestId("composer-input").boundingBox();
const send = await page.getByTestId("composer-send").boundingBox();
const inputGroup = await page
  .locator('[data-slot="input-group"]')
  .first()
  .boundingBox();
console.log("input box:", JSON.stringify(box));
console.log("input group:", JSON.stringify(inputGroup));
console.log("send button:", JSON.stringify(send));
if (box && send && inputGroup) {
  const insideVertically = send.y > box.y && send.y < box.y + box.height;
  const rightAligned = send.x + send.width > inputGroup.x + inputGroup.width * 0.75;
  console.log("send inside the text band:", insideVertically, "| right aligned:", rightAligned);
}
await page.screenshot({ path: path.join(OUT, "ui-composer.png") });

// 2. sidebar: collapse fully, check it is gone, then bring it back.
const sidebar = page.getByTestId("app-sidebar");
const before = await sidebar.boundingBox();
// The trigger lives in the top bar, which only exists once the sidebar is gone;
// before that, collapse it from the sidebar's own header button.
await page.getByTestId("sidebar-collapse").click();
await page.waitForTimeout(600);
const after = await sidebar.boundingBox();
console.log("sidebar width before:", before?.width, "after collapse:", after?.width ?? 0);
await page.screenshot({ path: path.join(OUT, "ui-sidebar-collapsed.png") });
const trigger = page.getByTestId("sidebar-trigger");
console.log("top-bar trigger visible after collapse:", await trigger.isVisible());
await trigger.click();
await page.waitForTimeout(600);
console.log("sidebar width reopened:", (await sidebar.boundingBox())?.width);

// 3. terminal: does it print a prompt on its own?
await page.getByTestId("panel-tab-terminal").click();
await page.getByTestId("terminal-pane").waitFor({ timeout: 20000 });
await page.waitForTimeout(3000);
await page.getByTestId("terminal-pane").click({ position: { x: 120, y: 60 } });
await page.keyboard.type("echo terminal-ok\r");
await page.waitForTimeout(1200);
await page.screenshot({ path: path.join(OUT, "ui-terminal.png") });
console.log("terminal status:", await page.getByTestId("terminal-pane").getAttribute("data-pty-status"));

await browser.close();
console.log("done");
