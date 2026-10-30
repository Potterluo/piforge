import { request as apiFactory, expect, test } from "@playwright/test";

import { ADMIN, USER, apiWrite, clickWhenSettled, login } from "./helpers";
import { API, adminSession, apiCall, findUser } from "./isolation";

/**
 * User management is the only way to onboard a tenant: there is no public
 * signup. It is also the one place where a mistake can lock the deployment out of
 * its own admin area, so both the happy path and the two refusals are covered
 * here against the real page.
 */
test.describe("admin users", () => {
  test("an admin creates an account and it can sign in", async ({ page, browser }) => {
    await login(page, ADMIN);
    await page.goto("/admin/users");

    const email = `e2e-${Date.now()}@example.com`;
    const password = "e2e-password-1";

    await clickWhenSettled(page.getByTestId("admin-create-user"));
    await page.getByTestId("new-user-email").fill(email);
    await page.getByTestId("new-user-password").fill(password);
    await clickWhenSettled(page.getByTestId("new-user-submit"));

    const row = page.getByTestId(`admin-user-${email}`);
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row).toContainText("普通用户");

    // The account is not just a row: it can actually sign in, in its own browser
    // context, with the password that was typed here.
    const other = await browser.newContext();
    const otherPage = await other.newPage();
    await login(otherPage, { email, password });
    await expect(otherPage.getByTestId("app-sidebar")).toBeVisible();
    await other.close();
  });

  test("a role can be changed and the change survives a reload", async ({ page }) => {
    await login(page, ADMIN);
    const email = `promote-${Date.now()}@example.com`;
    const created = await apiWrite(page, "POST", "/v1/admin/users", {
      email,
      password: "promote-me-1",
    });
    expect(created.status).toBeLessThan(300);

    await page.goto("/admin/users");
    const row = page.getByTestId(`admin-user-${email}`);
    await expect(row).toBeVisible({ timeout: 20_000 });

    await clickWhenSettled(row.getByRole("button", { name: "设为管理员" }));
    await expect(row).toContainText("管理员", { timeout: 20_000 });

    await page.reload();
    await expect(page.getByTestId(`admin-user-${email}`)).toContainText("管理员", {
      timeout: 20_000,
    });

    // Put the role back: a promoted throwaway left behind makes the next run of
    // this suite start with two admins, and "the last admin" is no longer one.
    const users = (await (await page.request.get("/v1/admin/users")).json()) as {
      users: { id: string; email: string }[];
    };
    const promoted = users.users.find((user) => user.email === email);
    expect(promoted).toBeTruthy();
    const demoted = await apiWrite(page, "PATCH", `/v1/admin/users/${promoted!.id}`, {
      role: "user",
    });
    expect(demoted.status).toBe(200);
  });

  test("an account can be disabled and enabled again", async ({ page }) => {
    await login(page, ADMIN);
    const email = `disable-${Date.now()}@example.com`;
    const created = await apiWrite(page, "POST", "/v1/admin/users", {
      email,
      password: "disable-me-1",
    });
    expect(created.status).toBeLessThan(300);

    await page.goto("/admin/users");
    const row = page.getByTestId(`admin-user-${email}`);
    await expect(row).toBeVisible({ timeout: 20_000 });

    // Disabling asks first: it logs the account out mid-session.
    await clickWhenSettled(row.getByRole("button", { name: "停用" }));
    await clickWhenSettled(page.getByRole("button", { name: "停用", exact: true }).last());
    await expect(row).toContainText("已停用", { timeout: 20_000 });
    await clickWhenSettled(row.getByRole("button", { name: "启用" }));
    await expect(row).toContainText("正常", { timeout: 20_000 });
  });

  test("the user list disables self-demotion and self-disable controls", async ({ page }) => {
    await login(page, ADMIN);
    await page.goto("/admin/users");
    // What this actually asserts: the controls are disabled for *the signed-in
    // row*, whatever the number of admins. It used to be named "the last admin
    // cannot demote or disable themselves", which this cannot show — the guard
    // being exercised here is the self guard. The last-admin rule is asserted
    // against the API in the test below.
    const row = page.getByTestId(`admin-user-${ADMIN.email}`);
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row.getByRole("button", { name: "降为普通" })).toBeDisabled();
    await expect(row.getByRole("button", { name: "停用" })).toBeDisabled();
  });

  /**
   * The rule the product promises is about the count, not about the caller: the
   * last admin cannot be demoted, so the deployment cannot lock itself out. The
   * old test could not tell the two apart because the seeded admin was both the
   * caller and the only admin, and the buttons are disabled for *self* even when
   * ten admins exist.
   *
   * The data directory is reused, so the deployment may already hold admins left
   * by earlier runs. This test builds the count it needs: it demotes every other
   * admin (restoring them in `finally`), refuses the last one, then promotes a
   * second admin and shows the very same request succeeding. Nothing about the
   * caller changes between the two attempts — only the number of admins.
   */
  test("the API refuses to demote the last admin and allows the same call with two", async () => {
    const seededJar = await adminSession();
    const secondJar = await apiFactory.newContext({ baseURL: API });
    let seededID = "";
    let secondID = "";
    let secondEmail = "";
    let others: { id: string; email: string }[] = [];
    try {
      const seeded = await findUser(seededJar, ADMIN.email);
      seededID = seeded.id;

      const listed = await seededJar.get("/v1/admin/users");
      expect(listed.status()).toBe(200);
      const { users } = (await listed.json()) as {
        users: { id: string; email: string; role: string }[];
      };
      others = users
        .filter((user) => user.role === "admin" && user.id !== seededID)
        .map((user) => ({ id: user.id, email: user.email }));

      // Exactly one admin: the seeded account. Leftovers from earlier runs would
      // otherwise make "the last admin" a different account than the one this
      // test controls.
      for (const other of others) {
        const demoted = await apiCall(seededJar, "PATCH", `/v1/admin/users/${other.id}`, {
          role: "user",
        });
        expect(demoted.status, `could not park ${other.email}: ${JSON.stringify(demoted.body)}`).toBe(
          200,
        );
      }

      // One admin: refuse. This is the last-admin rule, not the self guard — the
      // API has no self rule for roles (only for status), and the next half
      // shows the same self-demotion being allowed when a second admin exists.
      const lastAdmin = await apiCall(seededJar, "PATCH", `/v1/admin/users/${seededID}`, {
        role: "user",
      });
      expect(lastAdmin.status, JSON.stringify(lastAdmin.body)).toBe(409);
      expect((lastAdmin.body as { error: { message: string } }).error.message).toContain(
        "last admin",
      );
      // Refused, not applied.
      expect((await findUser(seededJar, ADMIN.email)).role).toBe("admin");

      // Two admins: a new one is promoted, and the seeded admin demotes *itself*
      // — the same request the API just refused.
      secondEmail = `last-admin-${Date.now()}@example.test`;
      const password = "last-admin-123";
      const created = await apiCall(seededJar, "POST", "/v1/admin/users", {
        email: secondEmail,
        password,
        display_name: "second admin",
        role: "user",
      });
      expect(created.status, JSON.stringify(created.body)).toBe(201);
      secondID = (created.body as { users: { id: string }[] }).users[0].id;
      const promoted = await apiCall(seededJar, "PATCH", `/v1/admin/users/${secondID}`, {
        role: "admin",
      });
      expect(promoted.status).toBe(200);

      const signIn = await secondJar.post("/v1/auth/login", {
        data: { email: secondEmail, password },
      });
      expect(signIn.ok(), "the second admin must be able to sign in").toBeTruthy();

      const demoteSelf = await apiCall(seededJar, "PATCH", `/v1/admin/users/${seededID}`, {
        role: "user",
      });
      expect(demoteSelf.status, JSON.stringify(demoteSelf.body)).toBe(200);

      // Disabling yourself is a separate guard and still refuses at the API.
      const disableSelf = await apiCall(secondJar, "PATCH", `/v1/admin/users/${secondID}`, {
        status: "disabled",
      });
      expect(disableSelf.status).toBe(400);
      expect((disableSelf.body as { error: { message: string } }).error.message).toContain(
        "cannot disable your own account",
      );
    } finally {
      // Restore the deployment even on a failed assertion: every other spec signs
      // in as the seeded admin, and any admin parked above must be an admin again.
      try {
        const login = await secondJar.post("/v1/auth/login", {
          data: { email: secondEmail, password: "last-admin-123" },
        });
        if (login.ok()) {
          await apiCall(secondJar, "PATCH", `/v1/admin/users/${seededID}`, { role: "admin" });
        }
      } catch {
        // No second admin was created; the seeded jar is still the admin.
      }
      if (seededID !== "") {
        await apiCall(seededJar, "PATCH", `/v1/admin/users/${seededID}`, { role: "admin" });
      }
      for (const other of others) {
        await apiCall(seededJar, "PATCH", `/v1/admin/users/${other.id}`, { role: "admin" });
      }
      if (secondID !== "") {
        await apiCall(seededJar, "PATCH", `/v1/admin/users/${secondID}`, { role: "user" });
      }
      await secondJar.dispose();
      await seededJar.dispose();
    }
  });

  test("a normal user is refused the user list", async ({ page }) => {
    await login(page, USER);
    await page.goto("/admin/users");
    await expect(page.getByText("需要管理员权限")).toBeVisible({ timeout: 20_000 });
  });
});
