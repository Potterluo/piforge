import { expect, test } from "@playwright/test";
import { ADMIN, USER, apiWrite, login } from "./helpers";

test.describe("authentication and isolation", () => {
  test("rejects a wrong password without signing in", async ({ page }) => {
    await page.goto("/login");
    await page.getByTestId("login-email").fill(USER.email);
    await page.getByTestId("login-password").fill("definitely-not-the-password");
    await page.getByTestId("login-submit").click();

    await expect(page.getByTestId("login-error")).toBeVisible();
    await expect(page.getByTestId("app-sidebar")).toHaveCount(0);
    // Still on the login route: the redirect must not have happened.
    await expect(page).toHaveURL(/\/login/);
  });

  test("signs the demo user in and shows their own identity", async ({ page }) => {
    await login(page, USER);
    await expect(page.getByTestId("user-email")).toContainText(USER.email);
  });

  test("a signed-in user can reload without losing the session", async ({ page }) => {
    await login(page, USER);
    await page.reload();
    await expect(page.getByTestId("app-sidebar")).toBeVisible();
  });

  test("anonymous API access is rejected with a typed error", async ({ request }) => {
    // `request` is a fresh context with its own cookie jar, so this really is
    // an unauthenticated call.
    const res = await request.get("/v1/projects");
    expect(res.status()).toBe(401);
    const body = await res.json();
    expect(body.error.code).toBe("unauthenticated");
  });

  test("admin can list users, a normal user cannot", async ({ page }) => {
    // page.request shares the browser cookie jar; the bare `request` fixture
    // does not, which would make every call anonymous.
    await login(page, USER);
    const denied = await page.request.get("/v1/admin/users");
    expect(denied.status()).toBe(403);
    expect((await denied.json()).error.code).toBe("forbidden");

    await login(page, ADMIN);
    const allowed = await page.request.get("/v1/admin/users");
    expect(allowed.status()).toBe(200);
    const body = await allowed.json();
    expect(Array.isArray(body.users)).toBe(true);
    expect(body.users.length).toBeGreaterThanOrEqual(2);
  });

  test("projects are scoped to their owner", async ({ page }) => {
    await login(page, USER);
    const created = await apiWrite(page, "POST", "/v1/projects", {
      name: `isolation-${Date.now()}`,
      repo_url: "",
      default_branch: "main",
    });
    expect(created.status).toBeLessThan(300);
    const project = (created.body as { project: { id: string; user_id: string } }).project;
    expect(project.user_id).toBeTruthy();

    const asUser = await page.request.get(`/v1/projects/${project.id}`);
    expect(asUser.status()).toBe(200);

    // A different account must not see it and must not be told whether it
    // exists: not_found, never a 403 that confirms the id.
    await login(page, ADMIN);
    const asAdmin = await page.request.get(`/v1/projects/${project.id}`);
    expect(asAdmin.status()).toBe(404);
    expect((await asAdmin.json()).error.code).toBe("not_found");
  });
});
