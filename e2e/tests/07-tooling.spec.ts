import { expect, test } from "@playwright/test";
import { apiWrite, openNewSession, sendPrompt } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * Skills and MCP are filesystem facts for the agent, so "configured" is not the
 * same as "usable". These tests assert the projection actually reached the
 * checkout, which is the only thing the agent reads.
 */
test.describe("tooling", () => {
  test("an MCP server is materialised into the session checkout", async ({ page }) => {
    await freshAccount(page, "tooling");

    const name = `mcp-${Date.now()}`;
    const created = await apiWrite(page, "POST", "/v1/mcp/servers", {
      name,
      transport: "stdio",
      command: "npx",
      args: ["-y", "@playwright/mcp"],
      exposure: "codemode",
      enabled: true,
    });
    expect(created.status).toBeLessThan(300);

    const sessionId = await openNewSession(page);
    // The first prompt provisions the sandbox and the checkout.
    await sendPrompt(page, "hello");

    const tooling = await page.request.get(`/v1/sessions/${sessionId}/tooling`);
    expect(tooling.status()).toBe(200);
    const body = (await tooling.json()) as {
      mounted: { skills: string[]; mcp_servers: string[]; mcp_written: boolean };
      mounted_at: string;
    };

    expect(body.mounted_at).not.toBe("");
    expect(body.mounted.mcp_servers).toContain(name);
    // `mcp_written` now means "the agent has a config", and that file lives in the
    // session's agent directory where Pi reads it — not in the checkout, where the
    // old `.mcp.json` was written and ignored.
    expect(body.mounted.mcp_written).toBe(true);
  });

  test("removing a server clears it from the next session", async ({ page }) => {
    await freshAccount(page, "tooling");
    const name = `tmp-${Date.now()}`;

    const created = await apiWrite(page, "POST", "/v1/mcp/servers", {
      name,
      transport: "stdio",
      command: "true",
      enabled: true,
    });
    expect(created.status).toBeLessThan(300);
    const serverId = (created.body as { server: { id: string } }).server.id;

    const first = await openNewSession(page);
    await sendPrompt(page, "hello");
    const firstTooling = await (await page.request.get(`/v1/sessions/${first}/tooling`)).json();
    expect(firstTooling.mounted.mcp_servers).toContain(name);

    const removed = await apiWrite(page, "DELETE", `/v1/mcp/servers/${serverId}`);
    expect(removed.status).toBeLessThan(300);

    // A new session must not inherit the deleted server: the config file is
    // rewritten from the current configuration, not appended to.
    const second = await openNewSession(page);
    await sendPrompt(page, "hello");
    const secondTooling = await (await page.request.get(`/v1/sessions/${second}/tooling`)).json();
    expect(secondTooling.mounted.mcp_servers).not.toContain(name);
  });

  test("a skill with no content is reported as configured but not mounted", async ({ page }) => {
    await freshAccount(page, "tooling");
    const name = `ghost-${Date.now()}`;

    const created = await apiWrite(page, "POST", "/v1/skills", {
      name,
      version: "1.0.0",
      content_hash: "deadbeef",
      description: "no bundle uploaded",
      enabled: true,
    });
    expect(created.status).toBeLessThan(300);

    const sessionId = await openNewSession(page);
    await sendPrompt(page, "hello");

    const tooling = await (await page.request.get(`/v1/sessions/${sessionId}/tooling`)).json();
    const configured = (tooling.skills as { name: string }[]).some((s) => s.name === name);
    const mounted = (tooling.mounted.skills as string[]).includes(name);
    // Configured, and honestly not mounted — the UI depends on this distinction.
    expect(configured).toBe(true);
    expect(mounted).toBe(false);
  });
});

/**
 * The loop the user actually cares about: type a skill into the UI, and the
 * agent gets it. The API test above proves the projection; this one proves the
 * whole path, because "configured" and "mounted" were two different things
 * until this round.
 */
test("a skill written in the UI reaches the next session's checkout", async ({ page }) => {
  await freshAccount(page, "tooling");
  const name = `ui-skill-${Date.now()}`;

  await page.goto("/settings/skills");
  await expect(page.getByTestId("skill-add")).toBeVisible({ timeout: 20_000 });
  await page.getByTestId("skill-add").click();
  await expect(page.getByTestId("skill-name")).toBeVisible({ timeout: 20_000 });
  await page.getByTestId("skill-name").fill(name);
  // The description is required because it is what the agent's skill index
  // shows; a skill nobody can recognise is a skill nobody uses.
  await page.getByTestId("skill-description").fill("run the tests before claiming done");
  await page.getByTestId("skill-body").fill(
    "---\nname: ui-skill\n---\nAlways run the tests before claiming a task is done.\n",
  );
  await page.getByTestId("skill-submit").click();

  // The row appears and reports that content is on file.
  const row = page.locator(`[data-testid="skill-item"][data-name="${name}"]`);
  await expect(row).toBeVisible({ timeout: 20_000 });

  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");

  const tooling = await (await page.request.get(`/v1/sessions/${sessionId}/tooling`)).json();
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  expect(tooling.mounted.skills).toContain(slug);
});

/**
 * A skill is a directory, not a paragraph: scripts and references next to
 * SKILL.md are what make one useful, so the upload has to carry them.
 */
test("assets picked alongside SKILL.md land in the bundle", async ({ page }) => {
  await freshAccount(page, "tooling");
  const name = `asset-skill-${Date.now()}`;

  await page.goto("/settings/skills");
  await expect(page.getByTestId("skill-add")).toBeVisible({ timeout: 20_000 });
  await page.getByTestId("skill-add").click();
  await page.getByTestId("skill-name").fill(name);
  await page.getByTestId("skill-description").fill("has a script");
  await page.getByTestId("skill-body").fill("---\nname: asset-skill\n---\nRun the script.\n");
  await page.getByTestId("skill-assets").setInputFiles([
    { name: "run.sh", mimeType: "text/x-shellscript", buffer: Buffer.from("echo from-the-bundle\n") },
  ]);
  await page.getByTestId("skill-submit").click();

  const row = page.locator(`[data-testid="skill-item"][data-name="${name}"]`);
  await expect(row).toBeVisible({ timeout: 20_000 });

  // Read it back through the API: the bundle must contain both files.
  const listed = await (await page.request.get("/v1/skills")).json();
  const skill = (listed.skills as { id: string; name: string }[]).find((s) => s.name === name);
  expect(skill).toBeTruthy();
  const bundle = await (await page.request.get(`/v1/skills/${skill!.id}/bundle`)).json();
  const paths = (bundle.files as { path: string }[]).map((f) => f.path).sort();
  expect(paths).toEqual(["SKILL.md", "run.sh"]);

  // And the mounted checkout carries the script, which is what the agent reads.
  const sessionId = await openNewSession(page);
  await sendPrompt(page, "hello");
  const tooling = await (await page.request.get(`/v1/sessions/${sessionId}/tooling`)).json();
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  expect(tooling.mounted.skills).toContain(slug);
});