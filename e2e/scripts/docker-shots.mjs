import { chromium } from "@playwright/test";
import path from "node:path";

const BASE = process.env.BASE ?? "http://127.0.0.1:7700";
const OUT = path.resolve(process.env.OUT ?? "../docs/screenshots");

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });

await page.goto(`${BASE}/login`);
await page.getByTestId("login-email").fill("dev@piforge.local");
await page.getByTestId("login-password").fill("piforge-dev-123");
await page.screenshot({ path: path.join(OUT, "20-docker-login.png") });
await page.getByTestId("login-submit").click();
await page.getByTestId("app-sidebar").waitFor({ timeout: 30000 });

const listed = await (await page.request.get(`${BASE}/v1/projects`)).json();
const project = listed.projects.find((p) => p.name === "docker-demo");
await page.goto(`${BASE}/p/${project.id}`);
await page.locator('a[href*="/s/"]').first().click();
await page.getByTestId("composer-input").waitFor({ timeout: 40000 });
await page.waitForTimeout(2500);
await page.screenshot({ path: path.join(OUT, "21-docker-workbench.png") });

await page.getByTestId("panel-tab-terminal").click();
await page.waitForTimeout(3500);
await page.getByTestId("terminal-pane").click({ position: { x: 140, y: 70 } });
await page.keyboard.type("cat docker.txt\r");
await page.waitForTimeout(1500);
await page.keyboard.type("git log --oneline -2 2>/dev/null || echo no-commits-yet\r");
await page.waitForTimeout(1500);
await page.screenshot({ path: path.join(OUT, "22-docker-terminal.png") });
console.log("terminal status:", await page.getByTestId("terminal-pane").getAttribute("data-pty-status"));

await browser.close();
console.log("done");