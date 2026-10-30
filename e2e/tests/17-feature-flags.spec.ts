import { expect, test } from "@playwright/test";

import { ADMIN, USER, login, openNewSession } from "./helpers";
import { adminSession, findUser, withFeatureRestore } from "./isolation";

/**
 * Per-user feature flags (PRD FR-16.7) are only worth having if the server
 * enforces them. Export is the sharpest case: "this tenant may not download
 * transcripts" is a governance decision, so the API has to refuse — a hidden
 * button is not a decision.
 */
// These flows sign out and back in several times, and every switch remounts the
// app shell. A 60s budget is tight for that on a machine already running the rest
// of the suite; the assertions themselves are unchanged.
test.setTimeout(120_000);

test.describe("feature flags", () => {
  test("an admin turns export off and the server refuses it", async ({ page }) => {
    // Export is a per-account switch whose default is ON. The demo account is
    // shared by the read-only specs, so this test restores the default in a
    // finally (and refuses to start from a leaked OFF, which is what a previous
    // half-finished run used to leave behind).
    const probe = await adminSession();
    let target: string;
    try {
      target = (await findUser(probe, USER.email)).id;
    } finally {
      await probe.dispose();
    }

    await withFeatureRestore(target, "export", true, async () => {
      const csrf = async () =>
        (await page.context().cookies()).find((cookie) => cookie.name === "pf_csrf")?.value ?? "";

      await login(page, USER);
      const sessionId = await openNewSession(page);
      const projectId = /\/p\/([^/]+)\//.exec(page.url())?.[1] ?? "";

      expect((await page.request.get(`/v1/sessions/${sessionId}/export?format=md`)).status()).toBe(200);
      const me = (await (await page.request.get("/v1/auth/me")).json()) as { user: { id: string } };

      await page.request.post("/v1/auth/logout", { headers: { "X-PF-CSRF": await csrf() } });
      await login(page, ADMIN);

      await page.goto("/admin/users");
      const row = page.locator("tr").filter({ hasText: USER.email });
      await row.getByTestId("user-features").click();

      const toggle = page.getByTestId("feature-export");
      await expect(toggle).toBeVisible({ timeout: 20_000 });
      await toggle.click();
      await expect(toggle).toHaveAttribute("aria-checked", "false", { timeout: 20_000 });

      // The API refuses now, and names the switch — checked as the owner, because
      // the admin still cannot reach that session at all (which is the point).
      await page.request.post("/v1/auth/logout", { headers: { "X-PF-CSRF": await csrf() } });
      await login(page, USER);
      const denied = await page.request.get(`/v1/sessions/${sessionId}/export?format=md`);
      expect(denied.status()).toBe(403);
      expect(await denied.text()).toContain("export");

      // For the user the menu item is absent rather than failing on click.
      await page.goto(`/p/${projectId}/s/${sessionId}`);
      await page.getByTestId("composer-input").waitFor({ timeout: 40_000 });
      await page.getByTestId("session-link").first().hover();
      await page.getByTestId("session-actions").first().click();
      await expect(page.getByTestId("session-export")).toHaveCount(0);

      // A user cannot flip their own switch, and the admin can put it back on.
      const refused = await page.request.post(`/v1/admin/users/${me.user.id}/features`, {
        data: { flag: "export", enabled: true },
        headers: { "X-PF-CSRF": await csrf() },
      });
      expect(refused.status()).toBe(403);

      await page.request.post("/v1/auth/logout", { headers: { "X-PF-CSRF": await csrf() } });
      await login(page, ADMIN);
      const restored = await page.request.post(`/v1/admin/users/${me.user.id}/features`, {
        data: { flag: "export", enabled: true },
        headers: { "X-PF-CSRF": await csrf() },
      });
      expect(restored.status()).toBe(200);
    });
  });
});
