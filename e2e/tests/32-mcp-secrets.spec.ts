import { expect, test } from "@playwright/test";

import { apiWrite } from "./helpers";
import { freshAccount } from "./isolation";

/**
 * FR-15.2: a credential can be stored, is never readable back, and the UI stops
 * claiming it cannot be stored.
 */
test("an MCP credential is stored but never returned", async ({ page }) => {
  await freshAccount(page, "mcp-secrets");

  const created = await apiWrite(page, "POST", "/v1/mcp/servers", {
    name: `e2e-mcp-${Date.now()}`,
    transport: "stdio",
    command: "npx",
    args: ["-y", "server-fs"],
    secrets: "tok_e2e_secret",
  });
  expect(created.status).toBe(201);
  const server = (created.body as { server: { id: string; has_secrets: boolean; secrets?: string } })
    .server;
  expect(server.has_secrets).toBe(true);
  expect(server.secrets ?? "").toBe("");

  // Reading the list back must not leak it either.
  const list = (await (await page.request.get("/v1/mcp/servers")).json()) as {
    servers?: { id: string; has_secrets: boolean; secrets?: string }[];
  };
  const row = (list.servers ?? []).find((item) => item.id === server.id);
  expect(row?.has_secrets).toBe(true);
  expect(row?.secrets ?? "").toBe("");
});