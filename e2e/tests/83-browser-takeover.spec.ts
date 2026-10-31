import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { USER, apiWrite, login } from "./helpers";

/**
 * FR-11.3: taking over the sandbox browser is exclusive.
 *
 * Two sessions of one project share one sandbox and therefore one Chrome. The
 * second context must be refused with the holder's name (`browser_busy`), the
 * first panel must say who is holding it, and releasing (switch back to
 * observe) must let the other one in without waiting for the lease TTL.
 */

const artifacts = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../.artifacts/partial-batch3");

interface ModeBody {
  mode: string;
  ttl_seconds?: number;
  holder?: {
    user_name: string;
    session_id: string;
    owned_by_me: boolean;
    remaining_seconds: number;
  };
}

async function createProjectSession(page: Page, projectId: string, title: string): Promise<string> {
  const created = await apiWrite(page, "POST", `/v1/projects/${projectId}/sessions`, { title });
  expect(created.status).toBeLessThan(300);
  const sessionId = (created.body as { session: { id: string } }).session.id;
  const sent = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
    client_msg_id: `m-${title}-${Date.now()}`,
    text: "hello",
  });
  expect(sent.status).toBeLessThan(300);
  return sessionId;
}

async function openBrowserPanel(page: Page, projectId: string, sessionId: string, signIn: boolean) {
  if (signIn) await login(page, USER);
  await page.goto(`/p/${projectId}/s/${sessionId}`);
  await page.getByTestId("panel-tab-browser").click();
  await expect(page.getByTestId("browser-panel")).toHaveAttribute("data-browser-status", "open", {
    timeout: 60_000,
  });
}

test("a second context cannot take over a browser somebody else is driving", async ({
  page,
  context,
}) => {
  test.setTimeout(180_000);
  mkdirSync(artifacts, { recursive: true });

  await login(page, USER);
  const project = await apiWrite(page, "POST", "/v1/projects", {
    name: `takeover-${Date.now()}`,
    repo_url: "",
    default_branch: "main",
  });
  const projectId = (project.body as { project: { id: string } }).project.id;

  // Two sessions in the same project: same sandbox, same browser. This is the
  // contention the lock is about — two tabs of one person is the common case,
  // two users on a shared sandbox the general one.
  const first = await createProjectSession(page, projectId, "first");
  const second = await createProjectSession(page, projectId, "second");

  await openBrowserPanel(page, projectId, first, false);

  // First context takes over through the real confirmation.
  await page.getByTestId("browser-mode").click();
  await expect(page.getByText("接管浏览器？")).toBeVisible({ timeout: 10_000 });
  await page.getByRole("button", { name: "接管" }).click();
  await expect(page.getByTestId("browser-mode")).toHaveText("控制中", { timeout: 20_000 });

  const granted = (await (
    await page.request.get(`/v1/sessions/${first}/browser/mode`)
  ).json()) as ModeBody;
  expect(granted.mode).toBe("takeover");
  expect(granted.holder?.owned_by_me).toBe(true);
  expect(granted.ttl_seconds).toBeGreaterThan(0);
  await page.screenshot({ path: path.join(artifacts, "fr-11.3-controlling.png") });

  // Second context: a different session page in its own browser context, so it
  // has its own cookie jar and its own panel, like a second person.
  const otherPage = await context.newPage();
  await login(otherPage, USER);
  await otherPage.goto(`/p/${projectId}/s/${second}`);
  await otherPage.getByTestId("panel-tab-browser").click();
  await expect(otherPage.getByTestId("browser-panel")).toHaveAttribute("data-browser-status", "open", {
    timeout: 60_000,
  });

  // The panel says who is driving instead of offering a fight.
  const holderBadge = otherPage.getByTestId("browser-holder");
  await expect(holderBadge).toBeVisible({ timeout: 20_000 });
  await expect(holderBadge).toContainText("正在被");
  await expect(holderBadge).toContainText(granted.holder?.user_name ?? "");
  await expect(otherPage.getByTestId("browser-mode")).toBeDisabled();
  await otherPage.screenshot({ path: path.join(artifacts, "fr-11.3-busy.png") });

  // And the API refuses by name, with the remaining time, rather than silently
  // flipping the mode of the other session.
  const refused = await apiWrite(otherPage, "POST", `/v1/sessions/${second}/browser/mode`, {
    mode: "takeover",
  });
  expect(refused.status).toBe(409);
  const failure = refused.body as {
    error: { code: string; message: string; details?: Record<string, unknown> };
  };
  expect(failure.error.code).toBe("browser_busy");
  expect(failure.error.message).toContain(granted.holder?.user_name ?? "");
  expect(failure.error.details?.holder_session_id).toBe(first);
  expect(Number(failure.error.details?.remaining_seconds)).toBeGreaterThan(0);

  // The refused attempt did not change the holder.
  const stillHeld = (await (
    await otherPage.request.get(`/v1/sessions/${first}/browser/mode`)
  ).json()) as ModeBody;
  expect(stillHeld.mode).toBe("takeover");
  expect(stillHeld.holder?.session_id).toBe(first);

  // Releasing (switch back to observe) frees the browser for the other context.
  await page.getByTestId("browser-mode").click();
  await expect(page.getByTestId("browser-mode")).toHaveText("观察", { timeout: 10_000 });

  await expect(otherPage.getByTestId("browser-holder")).toBeHidden({ timeout: 20_000 });
  await expect(otherPage.getByTestId("browser-mode")).toBeEnabled({ timeout: 20_000 });
  await otherPage.getByTestId("browser-mode").click();
  await expect(otherPage.getByText("接管浏览器？")).toBeVisible({ timeout: 10_000 });
  await otherPage.getByRole("button", { name: "接管" }).click();
  await expect(otherPage.getByTestId("browser-mode")).toHaveText("控制中", { timeout: 20_000 });

  const swapped = (await (
    await otherPage.request.get(`/v1/sessions/${second}/browser/mode`)
  ).json()) as ModeBody;
  expect(swapped.mode).toBe("takeover");
  expect(swapped.holder?.session_id).toBe(second);
  await otherPage.screenshot({ path: path.join(artifacts, "fr-11.3-swapped.png") });

  await otherPage.close();
});
