import { expect, test } from "@playwright/test";
import { apiWrite, login, openNewSession, sendPrompt, USER } from "./helpers";

/**
 * Skills and MCP are filesystem facts for the agent, so "configured" is not the
 * same as "usable". These tests assert the projection actually reached the
 * checkout, which is the only thing the agent reads.
 */
test.describe("tooling", () => {
  test("an MCP server is materialised into the session checkout", async ({ page }) => {
    await login(page, USER);

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
    expect(body.mounted.mcp_written).toBe(true);
  });

  test("removing a server clears it from the next session", async ({ page }) => {
    await login(page, USER);
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
    await login(page, USER);
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
