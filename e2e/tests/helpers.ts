import { expect, type Page } from "@playwright/test";

export const ADMIN = {
  email: process.env.PIFORGE_E2E_ADMIN_EMAIL ?? "admin@piforge.local",
  password: process.env.PIFORGE_E2E_ADMIN_PASSWORD ?? "piforge-admin-123",
};

export const USER = {
  email: process.env.PIFORGE_E2E_USER_EMAIL ?? "dev@piforge.local",
  password: process.env.PIFORGE_E2E_USER_PASSWORD ?? "piforge-dev-123",
};

/** Signs in through the real form and waits for the shell to render. */
export async function login(page: Page, who = USER): Promise<void> {
  await page.goto("/login");
  await page.getByTestId("login-email").fill(who.email);
  await page.getByTestId("login-password").fill(who.password);
  await page.getByTestId("login-submit").click();
  await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 20_000 });
}

/**
 * Creates a project (if the workspace is empty) and a session, then lands on
 * the workbench. Returns the session id from the workbench root.
 *
 * Every step waits on the element it needs rather than on a timer, because the
 * home page renders while its project list is still loading.
 */
export async function openNewSession(page: Page, projectName = "e2e-demo"): Promise<string> {
  await page.goto("/");

  // The project list arrives asynchronously; wait for either state to settle.
  const card = page.getByTestId("project-card").first();
  const newProject = page.getByTestId("new-project-button");

  await expect(async () => {
    const hasCard = (await card.count()) > 0;
    const hasButton = (await newProject.count()) > 0;
    expect(hasCard || hasButton).toBe(true);
  }).toPass({ timeout: 20_000 });

  if ((await card.count()) > 0) {
    await card.click();
  } else {
    await newProject.click();
    await page.getByTestId("project-name").fill(projectName);
    await page.getByTestId("project-submit").click();
    await expect(page.getByTestId("project-card").first()).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("project-card").first().click();
  }

  // Now on /p/<project>. The sidebar and the page both offer "新会话"; either
  // opens the same dialog.
  await page.getByTestId("new-session-button").first().click();
  await expect(page.getByTestId("session-submit")).toBeVisible({ timeout: 20_000 });
  await page.getByTestId("session-submit").click();

  const workbench = page.locator("[data-session-id]").first();
  await expect(workbench).toBeVisible({ timeout: 40_000 });
  const sessionId = await workbench.getAttribute("data-session-id");
  expect(sessionId).toBeTruthy();
  return sessionId!;
}

/** Sends a prompt through the composer and waits for the run to settle. */
export async function sendPrompt(page: Page, text: string): Promise<void> {
  await page.getByTestId("composer-input").fill(text);
  await page.getByTestId("composer-send").click();
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "running", {
    timeout: 30_000,
  });
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
    timeout: 60_000,
  });
}

/**
 * Headers for a state-changing API call made through `page.request`.
 *
 * The server enforces double-submit CSRF, and `page.request` does not run the
 * app's client code, so the token has to be lifted from the cookie jar by hand.
 * GET requests do not need it.
 */
export async function apiHeaders(page: Page): Promise<Record<string, string>> {
  const cookies = await page.context().cookies();
  const csrf = cookies.find((c) => c.name === "pf_csrf")?.value ?? "";
  return { "X-PF-CSRF": csrf };
}

/** POST/PUT/DELETE wrapper that adds CSRF for the current browser session. */
export async function apiWrite(
  page: Page,
  method: "POST" | "PUT" | "DELETE" | "PATCH",
  path: string,
  data?: unknown,
): Promise<{ status: number; body: unknown }> {
  const headers = await apiHeaders(page);
  const res = await page.request.fetch(path, {
    method,
    headers:
      typeof data === "string"
        ? { ...headers, "Content-Type": "text/plain; charset=utf-8" }
        : headers,
    data,
  });
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status(), body };
}

/** Count of assistant turns currently rendered. */
export async function assistantTurns(page: Page): Promise<number> {
  return page.getByTestId("turn-assistant").count();
}
