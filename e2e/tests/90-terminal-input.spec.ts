import { expect, test, type Page } from "@playwright/test";

import { USER, login, openNewSession } from "./helpers";

/**
 * FR-10.1: input must survive the relay at any speed.
 *
 * The acceptance pass typed `node calc.test.js; echo EXIT=$?` at 25 ms/char and
 * the shell received `t.js; echo EXIT=$?` — a lost prefix, not a mistyped
 * command. The relay itself is lossless (a direct socket probe sent all 26 bytes
 * at 25 ms/char and as a zero-delay burst, and bash ran the command); the
 * client's `send` discarded every chunk whose socket was not OPEN yet. These two
 * tests pin the two windows: a burst against an open socket, and typing that
 * starts while the socket is still connecting.
 */

/** The rows of the terminal tab that is actually on screen (inactive ones are `hidden`). */
const rows = (page: Page) =>
  page.evaluate(() => {
    const panels = [...document.querySelectorAll('[data-testid="terminal-tab-panel"]')];
    const visible = panels.find((panel) => !panel.classList.contains("hidden"));
    return (
      visible?.querySelector('[data-slot="xterm-surface"] .xterm-rows')?.textContent ?? ""
    );
  });

/**
 * Counts the binary frames the panel actually hands to the socket, split by the
 * socket's readyState at send time. This is what makes the pre-connect test
 * honest: with the handshake delayed, `notOpen` must be non-zero, otherwise the
 * test typed after the window and proves nothing.
 */
async function installSendProbe(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const probe: {
      open: number;
      notOpen: number;
      sockets: { url: string; created: number; opened: number | null }[];
    } = { open: 0, notOpen: 0, sockets: [] };
    (window as unknown as { __ptySends: typeof probe }).__ptySends = probe;
    const Original = window.WebSocket;
    window.WebSocket = class extends Original {
      constructor(...args: ConstructorParameters<typeof WebSocket>) {
        super(...args);
        const rec = { url: String(args[0]), created: performance.now(), opened: null as number | null };
        probe.sockets.push(rec);
        this.addEventListener("open", () => {
          rec.opened = performance.now();
        });
      }
      send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
        if (typeof data !== "string") {
          if (this.readyState === Original.OPEN) probe.open += 1;
          else probe.notOpen += 1;
        }
        return super.send(data);
      }
    };
  });
}

const sends = (page: Page) =>
  page.evaluate(
    () =>
      (window as unknown as { __ptySends?: { open: number; notOpen: number } }).__ptySends as {
        open: number;
        notOpen: number;
      },
  );

/** Marker printed only when the whole command ran: 42 is not in the input. */
function command(marker: string): string {
  const js = `console.log('${marker}-'+(6*7))`;
  // PowerShell's $? is True/False; the exit status of a native command is
  // $LASTEXITCODE. Bash has no $LASTEXITCODE, so the two shells need their own
  // spelling of "the same sentence".
  const exit = process.platform === "win32" ? "echo EXIT=$LASTEXITCODE" : "echo EXIT=$?";
  return `node -e "${js}"; ${exit}`;
}

async function openTerminal(page: Page): Promise<void> {
  await page.getByTestId("panel-tab-terminal").click();
  const pane = page.getByTestId("terminal-pane").first();
  await expect(pane).toHaveAttribute("data-pty-status", "open", { timeout: 30_000 });
  // Open is not the same as the shell being ready to read: wait for the prompt
  // the way a person does, then for the textarea xterm reads keys from.
  await expect.poll(() => rows(page), { timeout: 30_000 }).toMatch(/[>#$]\s*$/);
}

async function focusTerminal(page: Page): Promise<void> {
  const surface = page.locator('[data-testid="terminal-tab-panel"] [data-slot="xterm-surface"]').first();
  await surface.click();
  await page.locator('[data-testid="terminal-tab-panel"] .xterm-helper-textarea').first().focus();
}

test.describe("terminal input", () => {
  test("a zero-delay burst arrives complete and the command exits 0", async ({ page }) => {
    test.setTimeout(120_000);
    await installSendProbe(page);
    await login(page, USER);
    await openNewSession(page);
    await openTerminal(page);
    await focusTerminal(page);

    const marker = `BURST-${Date.now().toString(36).toUpperCase()}`;
    const line = command(marker);
    // No delay at all: every keystroke is its own CDP event, the tightest the
    // browser can produce. A client that coalesces or throttles loses here.
    await page.keyboard.type(line, { delay: 0 });
    await page.keyboard.press("Enter");

    await expect.poll(() => rows(page), { timeout: 30_000 }).toContain(`${marker}-42`);
    expect(await rows(page)).toContain("EXIT=0");
    // The shell echoed the command it received: the exact text, not a prefix.
    expect(await rows(page)).toContain(marker);
    // Every frame went out on an open socket, so a failure above cannot be
    // blamed on this test accidentally measuring the connecting window.
    expect((await sends(page))!.notOpen).toBe(0);
  });

  test("typing before the socket opens is buffered, not dropped", async ({ page }) => {
    test.setTimeout(120_000);
    await installSendProbe(page);
    await login(page, USER);
    await openNewSession(page);

    // Deterministic pre-connect window: network latency delays the WebSocket
    // handshake (measured: 3 ms normally, 520 ms at 500 ms latency). The first
    // terminal is opened once so xterm's lazy chunk is cached — otherwise the
    // latency delays the chunk too and the panel only mounts after the socket
    // already opened, which is what made an earlier version of this test measure
    // nothing. Then a *new tab* is added: fresh pane, fresh socket, same shell
    // machinery, and the user path "open a terminal and start typing".
    await page.getByTestId("panel-tab-terminal").click();
    await expect(page.getByTestId("terminal-pane").first()).toHaveAttribute(
      "data-pty-status",
      "open",
      { timeout: 30_000 },
    );

    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Network.enable");
    await cdp.send("Network.emulateNetworkConditions", {
      offline: false,
      latency: 1500,
      downloadThroughput: -1,
      uploadThroughput: -1,
    });

    await page.getByTestId("terminal-tab-add").click();
    await expect(page.getByTestId("terminal-pane")).toHaveCount(2, { timeout: 10_000 });
    const textarea = page.locator('[data-testid="terminal-tab-panel"]:not(.hidden) .xterm-helper-textarea');
    await textarea.waitFor({ state: "attached", timeout: 30_000 });
    // The overlay covers the pane while it connects, but xterm's textarea still
    // holds focus across a reconnect; focusing it is what a user whose cursor is
    // already in the terminal does.
    await textarea.focus();

    const marker = `PRECONNECT-${Date.now().toString(36).toUpperCase()}`;
    const line = command(marker);
    const typedAt = await page.evaluate(() => performance.now());
    await page.keyboard.type(line, { delay: 10 });
    await page.keyboard.press("Enter");
    // The window is real only if the new socket had not opened while typing ran.
    const during = await page.evaluate(() => {
      const probe = (
        window as unknown as {
          __ptySends: {
            open: number;
            notOpen: number;
            sockets: { url: string; created: number; opened: number | null }[];
          };
        }
      ).__ptySends;
      return { sockets: probe.sockets.slice(), now: performance.now() };
    });
    const newest = during.sockets[during.sockets.length - 1];
    expect(newest?.url).toContain("tab=t2");
    expect(newest.opened === null || newest.opened > typedAt).toBe(true);

    // Let the handshake finish at normal speed; the buffer must flush in order.
    await cdp.send("Network.emulateNetworkConditions", {
      offline: false,
      latency: 0,
      downloadThroughput: -1,
      uploadThroughput: -1,
    });

    await expect.poll(() => rows(page), { timeout: 40_000 }).toContain(`${marker}-42`);
    expect(await rows(page)).toContain("EXIT=0");
    // Every keystroke reached the socket as its own frame — nothing coalesced,
    // nothing silently dropped. Enter is the extra frame.
    const total = await page.evaluate(() => {
      const probe = (window as unknown as { __ptySends: { open: number; notOpen: number } }).__ptySends;
      return probe.open + probe.notOpen;
    });
    expect(total).toBeGreaterThanOrEqual(line.length + 1);
  });
});
