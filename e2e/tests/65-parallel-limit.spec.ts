import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { apiWrite, ensureProject, login, openSession, sendPrompt, ADMIN, USER } from "./helpers";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ARTIFACTS = path.resolve(HERE, "../.artifacts/fix-limit-compact");
mkdirSync(ARTIFACTS, { recursive: true });

/**
 * FR-3.3 against the deployment's own configuration.
 *
 * The harness starts piforged with `PIFORGE_PARALLEL_LIMIT=2` (see
 * playwright.config.ts), so the value under test arrived the way an operator
 * sets it — no test pokes Deps.ParallelLimit. The limit is then read back from
 * the admin status screen (the "what is it set to" answer), two held runs are
 * started through the API, and the third prompt is refused in the browser with
 * the server's sentence naming the cap and what is running.
 */
test("the configured parallel limit refuses a third run with an explanation", async ({
  page,
}) => {
  // 1. Enable parallel running for the dev account (FR-3.2: it is opt-in).
  await login(page, ADMIN);
  const usersRes = await page.request.get("/v1/admin/users");
  expect(usersRes.status()).toBe(200);
  const { users } = (await usersRes.json()) as { users: { id: string; email: string }[] };
  const dev = users.find((user) => user.email === USER.email);
  expect(dev, "the dev account exists").toBeTruthy();
  const enabled = await apiWrite(page, "POST", `/v1/admin/users/${dev!.id}/features`, {
    flag: "parallel",
    enabled: true,
  });
  expect(enabled.status).toBe(200);

  // 2. The admin screen answers "what is it set to": 2, from the environment.
  await page.goto("/admin/status");
  await expect(page.getByTestId("parallel-limit-value")).toHaveText("2");
  await expect(page.getByTestId("stat-parallel-limit")).toContainText("并行分支上限");
  await page.screenshot({ path: path.join(ARTIFACTS, "31-admin-parallel-limit.png") });

  // 3. A root session, plus two branches forked from it. Fork is the API that
  //    records root_id, which is what "branches of one session" means here.
  await login(page, USER);
  const projectId = await ensureProject(page, `e2e-limit-${Date.now()}`);
  const rootId = await openSession(page, projectId, "limit-root");
  await sendPrompt(page, "seed a fork point");
  const pointsRes = await page.request.get(`/v1/sessions/${rootId}/fork-points`);
  expect(pointsRes.status()).toBe(200);
  const { points } = (await pointsRes.json()) as { points: { entry_id: string }[] };
  expect(points.length).toBeGreaterThan(0);

  const fork = async (label: string): Promise<string> => {
    const res = await apiWrite(page, "POST", `/v1/sessions/${rootId}/fork`, {
      entry_id: points[0].entry_id,
      label,
    });
    expect(res.status).toBeLessThan(300);
    const { session } = res.body as { session: { id: string } };
    expect(session.id).toBeTruthy();
    return session.id;
  };
  const branchA = await fork("limit-a");
  const branchB = await fork("limit-b");

  // 4. The third branch's page is open before the two runs start, so the only
  //    wait after starting them is the prompt itself.
  await page.goto(`/p/${projectId}/s/${branchB}`);
  await expect(page.locator(`[data-session-id="${branchB}"]`)).toBeVisible({ timeout: 40_000 });

  for (const sessionId of [rootId, branchA]) {
    const started = await apiWrite(page, "POST", `/v1/sessions/${sessionId}/messages`, {
      client_msg_id: `cm_hold_${sessionId}`,
      // The peer paces this run so two are genuinely in flight.
      text: "hold this run [[hold-run]]",
    });
    expect(started.status).toBe(200);
  }

  // 5. Through the UI: the refusal is a message with the limit and the running
  //    branches, not a generic "发送失败".
  await page.getByTestId("composer-input").fill("the third one");
  await page.getByTestId("composer-send").click();
  const refusal = page.getByTestId("parallel-limit-refusal");
  await expect(refusal).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId("parallel-limit-reason")).toContainText("并行上限是 2 个分支");
  await expect(page.getByTestId("parallel-limit-reason")).toContainText("现在有 2 个在运行");
  // The branch's label is how a person recognises what is occupying the slot.
  await expect(page.getByTestId("parallel-limit-reason")).toContainText("limit-a");
  await page.screenshot({ path: path.join(ARTIFACTS, "32-parallel-limit-refusal.png") });

  // 6. The API answer carries the same sentence plus the machine-readable part.
  const apiRefusal = await apiWrite(page, "POST", `/v1/sessions/${branchB}/messages`, {
    client_msg_id: "cm_third_api",
    text: "the third one, through the API",
  });
  expect(apiRefusal.status).toBe(429);
  const body = apiRefusal.body as {
    error: { code: string; message: string; details: { limit: number; running: string[] } };
  };
  expect(body.error.code).toBe("rate_limited");
  expect(body.error.message).toContain("并行上限是 2 个分支");
  expect(body.error.details.limit).toBe(2);
  expect(body.error.details.running).toHaveLength(2);

  // Leave the suite without two held runs still streaming.
  for (const sessionId of [rootId, branchA]) {
    await apiWrite(page, "POST", `/v1/sessions/${sessionId}/interrupt`, {});
  }
});
