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

/** Returns the id of `name`, creating the project through the API if needed. */
export async function ensureProject(page: Page, name = "e2e"): Promise<string> {
  const listed = await page.request.get("/v1/projects");
  expect(listed.status()).toBe(200);
  const { projects } = (await listed.json()) as { projects: { id: string; name: string }[] };
  const existing = projects.find((project) => project.name === name);
  if (existing) return existing.id;

  const created = await apiWrite(page, "POST", "/v1/projects", {
    name,
    repo_url: "",
    default_branch: "main",
  });
  expect(created.status).toBeLessThan(300);
  return (created.body as { project: { id: string } }).project.id;
}

/**
 * Creates a session and opens its workbench.
 *
 * Setup goes through the API on purpose: the project and session dialogs are
 * covered by their own spec, and repeating a four-click flow in every test only
 * makes unrelated tests fail together. Each test then asserts the behaviour it
 * is actually about, against a real page.
 */
export async function openSession(
  page: Page,
  projectId: string,
  title = "e2e session",
): Promise<string> {
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, { title });
  expect(created.status).toBeLessThan(300);
  const sessionId = (created.body as { session: { id: string } }).session.id;

  await page.goto(`/p/${projectId}/s/${sessionId}`);
  await expect(page.locator(`[data-session-id="${sessionId}"]`)).toBeVisible({ timeout: 40_000 });
  return sessionId;
}

let projectSeq = 0;

/**
 * Convenience: create an isolated project, then open a fresh session in it.
 *
 * Each call gets its own project so a test that writes files cannot disturb the
 * next one — sessions in the same project share the primary checkout, which is
 * the whole point of the `(project, user)` sandbox granularity and a terrible
 * thing to share between tests.
 */
export async function openNewSession(page: Page, projectName?: string): Promise<string> {
  projectSeq += 1;
  const name = projectName ?? `e2e-${Date.now()}-${projectSeq}`;
  const projectId = await ensureProject(page, name);
  return openSession(page, projectId);
}

/**
 * Clicks an element that may be replaced by a re-render between resolution and
 * the click (React Query results, optimistic lists). Playwright's documented
 * remedy is to retry the whole interaction.
 */
export async function clickWhenSettled(locator: ReturnType<Page["getByTestId"]>): Promise<void> {
  await expect(async () => {
    await locator.click({ timeout: 3_000 });
  }).toPass({ timeout: 20_000 });
}

/** Sends a prompt through the composer and waits for the run to settle. */
export async function sendPrompt(page: Page, text: string): Promise<void> {
  await page.getByTestId("composer-input").fill(text);
  await page.getByTestId("composer-send").click();
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "running", {
    timeout: 30_000,
  });
  await expect(page.getByTestId("run-state")).toHaveAttribute("data-state", "settled", {
    timeout: 90_000,
  });
}

/** Count of assistant turns currently rendered. */
export async function assistantTurns(page: Page): Promise<number> {
  return page.getByTestId("turn-assistant").count();
}
