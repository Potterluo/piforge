import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { expect, test, type Page } from "@playwright/test";

import { login, openNewSession, USER } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-follow-compact");
mkdirSync(ARTIFACTS, { recursive: true });

/** The fake peer's `[[slow-lines]]` marker: seven lines, 700ms apart. */
const SLOW = "[[slow-lines]]";
const LINES = 7;

/**
 * The numbers of the SLOW-LINE-N lines xterm is currently showing.
 *
 * The assertion has to read what the user reads — the terminal rows — not the
 * panel container. The container also holds the toolbar (whose title is
 * "跟随终端"), so a non-empty check there is true even when the mirror is dead;
 * that is exactly the vacuous assertion this spec used to make.
 */
async function followLines(page: Page): Promise<number[]> {
  const text = await page.evaluate(() => {
    const rows = document.querySelector('[data-testid="follow-terminal"] .xterm-rows');
    return rows?.textContent ?? "";
  });
  const seen = new Set<number>();
  for (const match of text.matchAll(/SLOW-LINE-(\d+)/g)) seen.add(Number(match[1]));
  return [...seen].sort((a, b) => a - b);
}

/** How many times the view printed each line; cumulative snapshots would double them. */
async function followOccurrences(page: Page): Promise<number> {
  const text = await page.evaluate(() => {
    const rows = document.querySelector('[data-testid="follow-terminal"] .xterm-rows');
    return rows?.textContent ?? "";
  });
  return [...text.matchAll(/SLOW-LINE-\d+/g)].length;
}

async function startSlowCommand(page: Page): Promise<void> {
  await page.getByTestId("composer-input").fill(`run a loop ${SLOW}`);
  await page.getByTestId("composer-send").click();
}

/**
 * FR-9.2: the follow view is a live mirror of the agent's bash output.
 *
 * The assertion that matters is made *while the command is still running*: the
 * old view rendered the first snapshot of a tool block and never advanced,
 * because it tracked array indexes and the array only ever had one element whose
 * contents grew.
 */
test("the follow view grows while the agent's command is still running", async ({ page }) => {
  test.setTimeout(120_000);
  await login(page, USER);
  await openNewSession(page);

  // Opened before the command starts: the panel must keep up, not freeze on the
  // first chunk that happens to arrive after it mounted.
  await page.getByTestId("panel-tab-follow").click();
  await expect(page.getByTestId("follow-terminal")).toBeVisible();

  await startSlowCommand(page);

  await expect
    .poll(async () => (await followLines(page)).length, { timeout: 40_000 })
    .toBeGreaterThanOrEqual(3);

  // The proof that this is live output and not the transcript rendered afterwards.
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "running");
  const midRun = await followLines(page);
  expect(midRun.length).toBeLessThan(LINES);
  await page.screenshot({ path: path.join(ARTIFACTS, "follow-live-mid-run.png") });

  // The run settles, and the mirror keeps what the command actually printed:
  // every line once, in order, with nothing duplicated by cumulative snapshots.
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
    timeout: 60_000,
  });
  await expect
    .poll(async () => (await followLines(page)).length, { timeout: 20_000 })
    .toBe(LINES);
  expect(await followLines(page)).toEqual([1, 2, 3, 4, 5, 6, 7]);
  expect(await followOccurrences(page)).toBe(LINES);
  await page.screenshot({ path: path.join(ARTIFACTS, "follow-live-final.png") });
});

/**
 * The other half of the requirement: a follow view opened *after* the command
 * started still has the earlier output, and a second browser tab following the
 * same terminal shows the same content.
 */
test("a follow view opened after the start catches up, and a second tab mirrors it", async ({
  page,
  context,
}) => {
  test.setTimeout(120_000);
  await login(page, USER);
  await openNewSession(page);
  const url = page.url();

  // The first tab does not open 跟随 before the run; the second tab opens the
  // same session *while* the command is printing.
  await startSlowCommand(page);

  const second = await context.newPage();
  await second.goto(url);
  await second.getByTestId("panel-tab-follow").click();
  await expect(second.getByTestId("follow-terminal")).toBeVisible();

  // It caught up on output produced before it existed (the first lines were
  // printed before this page was opened) and keeps growing while the run lasts.
  await expect
    .poll(async () => (await followLines(second)).length, { timeout: 40_000 })
    .toBeGreaterThanOrEqual(3);
  await expect(second.getByTestId("run-state")).toHaveAttribute("data-state", "running");
  expect(await followLines(second)).toContain(1);
  await second.screenshot({ path: path.join(ARTIFACTS, "follow-second-tab-mid-run.png") });

  // Opening 跟随 late in the first tab gives the same terminal, not an empty one.
  await page.getByTestId("panel-tab-follow").click();
  await expect(page.getByTestId("follow-terminal")).toBeVisible();

  await expect(second.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
    timeout: 60_000,
  });
  await expect
    .poll(async () => (await followLines(second)).length, { timeout: 20_000 })
    .toBe(LINES);
  await expect
    .poll(async () => (await followLines(page)).length, { timeout: 20_000 })
    .toBe(LINES);

  expect(await followLines(page)).toEqual(await followLines(second));
  expect(await followOccurrences(page)).toBe(LINES);
  await page.screenshot({ path: path.join(ARTIFACTS, "follow-late-and-second-tab.png") });
});
