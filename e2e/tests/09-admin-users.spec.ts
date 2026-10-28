import { expect, test } from "@playwright/test";

import { ADMIN, USER, apiWrite, clickWhenSettled, login } from "./helpers";

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

  test("the last admin cannot demote or disable themselves", async ({ page }) => {
    await login(page, ADMIN);
    await page.goto("/admin/users");
    // The seeded admin is the only admin in a fresh e2e database. Both controls
    // are the guards that keep the deployment from locking itself out.
    const row = page.getByTestId(`admin-user-${ADMIN.email}`);
    await expect(row).toBeVisible({ timeout: 20_000 });
    await expect(row.getByRole("button", { name: "降为普通" })).toBeDisabled();
    await expect(row.getByRole("button", { name: "停用" })).toBeDisabled();
  });

  test("a normal user is refused the user list", async ({ page }) => {
    await login(page, USER);
    await page.goto("/admin/users");
    await expect(page.getByText("需要管理员权限")).toBeVisible({ timeout: 20_000 });
  });
});
