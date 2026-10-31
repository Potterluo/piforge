import { expect, test } from "@playwright/test";

import { ADMIN, USER, apiWrite, login, openNewSession, sendPrompt } from "./helpers";

/**
 * Two P1 requirements that only exist in the browser: the quick-open palette
 * (FR-6.4) and the operator's status screen (FR-16.5).
 */
test.describe("quick open", () => {
  test("Ctrl+P finds a file the tree has never loaded and opens it", async ({ page }) => {
    await login(page, USER);
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    // Two directories deep, so a filter over the loaded rows could not find it.
    expect(
      (
        await apiWrite(
          page,
          "PUT",
          `/v1/sessions/${sessionId}/files?path=deep%2Fnested%2Fneedle.txt`,
          "found me\n",
        )
      ).status,
    ).toBeLessThan(300);

    await page.keyboard.press("Control+p");
    const input = page.getByTestId("quick-open-input");
    await expect(input).toBeVisible({ timeout: 20_000 });
    await input.fill("needle");

    const result = page.getByTestId("quick-open-result").first();
    await expect(result).toContainText("needle.txt", { timeout: 20_000 });
    await result.click();

    // The preview opened the file, which is the whole point of quick open.
    await expect(page.getByTestId("file-preview")).toContainText("found me", { timeout: 20_000 });
    await expect(input).toHaveCount(0);
  });

  test("Escape closes it without opening anything", async ({ page }) => {
    await login(page, USER);
    await openNewSession(page);
    await page.keyboard.press("Control+p");
    const input = page.getByTestId("quick-open-input");
    await expect(input).toBeVisible({ timeout: 20_000 });
    await input.fill("zzz-no-such-file");
    await page.keyboard.press("Escape");
    await expect(input).toHaveCount(0);

    // Reopening must not resurrect the dead search that was just abandoned.
    await page.keyboard.press("Control+p");
    await expect(page.getByTestId("quick-open-input")).toHaveValue("", { timeout: 20_000 });
    await expect(page.getByTestId("quick-open-result")).toHaveCount(0);
  });
});

test.describe("runtime status", () => {
  test("an admin sees real counts and a normal user does not see the page", async ({ page }) => {
    await login(page, ADMIN);
    await page.goto("/admin/status");
    await expect(page.getByTestId("admin-status")).toBeVisible({ timeout: 20_000 });

    const users = page.getByTestId("stat-users");
    // Other specs create accounts, so the number is asserted as a shape, not a
    // constant: "<active> / <total>" with at least the two seeded accounts.
    await expect(users).toContainText(/\d+ \/ \d+/, { timeout: 20_000 });
    await expect.poll(async () => {
      const text = (await users.textContent()) ?? "";
      const match = /(\d+)\s*\/\s*(\d+)/.exec(text);
      if (!match) return -1;
      const [active, total] = [Number(match[1]), Number(match[2])];
      return total >= 2 && active >= 1 && active <= total ? 1 : 0;
    }, { timeout: 20_000 }).toBe(1);
    await expect(page.getByTestId("stat-sandboxes")).toBeVisible();
    // The agent numbers are split: turns in flight are not live processes, and
    // the old single "运行中的 agent" card counted processes. Both cards must be
    // there, and the runs one must be labelled for what it counts.
    await expect(page.getByTestId("stat-runs")).toBeVisible();
    await expect(page.getByTestId("stat-runs")).toContainText("进行中的运行");
    await expect(page.getByTestId("stat-agent-processes")).toBeVisible();
    await expect(page.getByTestId("stat-agent-processes")).toContainText("agent 进程");
    await expect(page.getByTestId("stat-agents")).toHaveCount(0);
    // The runtime-directory block is the operator's view of <dataDir>: one row
    // per directory the server creates, with entries and bytes. It is the same
    // inventory the janitor sweeps, so a missing row is a lifecycle gap.
    await expect(page.getByTestId("stat-storage")).toBeVisible();
    await expect(page.getByTestId("stat-storage-pi-agents")).toBeVisible();
    await expect(page.getByTestId("stat-storage-artifacts")).toBeVisible();

    await page.request.post("/v1/auth/logout", {
      headers: { "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "" },
    });
    await login(page, USER);
    const res = await page.request.get("/v1/admin/status");
    expect(res.status()).toBe(403);
  });
});
