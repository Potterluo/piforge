import { expect, test } from "@playwright/test";

import { ADMIN, apiWrite, login, openNewSession, sendPrompt } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * Support mode (PRD FR-16.8) is the one way an admin can read somebody else's
 * conversation. The promise is: they must say why, it expires, it cannot change
 * anything, and the owner gets a receipt naming them.
 */
// These flows sign out and back in several times, and every switch remounts the
// app shell. A 60s budget is tight for that on a machine already running the rest
// of the suite; the assertions themselves are unchanged.
test.setTimeout(120_000);

test.describe("support mode", () => {
  test("an admin reads on the record and the owner is told", async ({ page }) => {
    // The owner has a conversation worth reading. Its own account, so the
    // receipt this test creates cannot be counted by anything else.
    const owner = await freshAccount(page, "support");
    const sessionId = await openNewSession(page);
    await sendPrompt(page, "the run is stuck at 99%");

    const csrf = (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "";
    await page.request.post("/v1/auth/logout", { headers: { "X-PF-CSRF": csrf } });

    // The admin cannot read it through the ordinary endpoints.
    await login(page, ADMIN);
    expect((await page.request.get(`/v1/sessions/${sessionId}`)).status()).toBe(404);

    // With a reason, the transcript comes back.
    await page.goto("/admin/support");
    await page.getByTestId("support-session").fill(sessionId);
    await page.getByTestId("support-reason").fill("用户报告 run 卡住，看最后一次工具调用");
    await page.getByTestId("support-submit").click();
    const transcript = page.getByTestId("support-transcript");
    await expect(transcript).toBeVisible({ timeout: 20_000 });
    await expect(transcript).toContainText("the run is stuck at 99%");
    await expect(transcript).toContainText("只读");

    // Read-only still means read-only.
    const renamed = await apiWrite(page, "PATCH", `/v1/sessions/${sessionId}`, { title: "hijacked" });
    expect(renamed.status).toBe(404);

    // The owner sees the receipt, naming the admin and the reason.
    await page.request.post("/v1/auth/logout", {
      headers: { "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "" },
    });
    await login(page, owner);
    const notices = page.getByTestId("support-notices");
    await expect(notices).toContainText("1", { timeout: 20_000 });
    await notices.click();
    const list = page.getByTestId("support-notice-list");
    await expect(list).toBeVisible({ timeout: 20_000 });
    await expect(list).toContainText("admin@piforge.local");
    await expect(list).toContainText("用户报告 run 卡住");
  });

  test("a normal user cannot open support access", async ({ page }) => {
    await freshAccount(page, "support");
    const res = await page.request.post("/v1/admin/support-access", {
      data: { session_id: "ses_whatever", reason: "because" },
      headers: {
        "X-PF-CSRF": (await page.context().cookies()).find((c) => c.name === "pf_csrf")?.value ?? "",
      },
    });
    expect(res.status()).toBe(403);
  });
});
