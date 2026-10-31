import { describe, expect, it } from "vitest";

import {
  STDIO_UNAVAILABLE_HEADLINE,
  hasUnstagedServers,
  unavailableExplanation,
} from "@/lib/mcp";

/**
 * Stage 4 made the stdio refusal part of the product. These assertions are the
 * product requirement written down: the panel says the sentence, it names the
 * server, it carries the reason, and it stays quiet when nothing is affected.
 */
describe("MCP isolation copy", () => {
  const unavailable = {
    fs: {
      code: "stdio_needs_credentials_in_the_sandbox",
      reason: "a stdio MCP server is a process Pi starts inside the sandbox",
    },
  };

  it("says stdio servers are unavailable in isolation, and why", () => {
    const text = unavailableExplanation({ name: "fs" }, unavailable);
    expect(text).toContain(STDIO_UNAVAILABLE_HEADLINE);
    expect(text).toContain("Pi starts inside the sandbox");
  });

  it("does not invent an explanation for a server that can be staged", () => {
    expect(unavailableExplanation({ name: "docs" }, unavailable)).toBeNull();
    expect(unavailableExplanation({ name: "fs" }, undefined)).toBeNull();
  });

  it("falls back to the headline when the server sent no reason", () => {
    const text = unavailableExplanation({ name: "fs" }, { fs: { code: "x", reason: "  " } });
    expect(text).toBe(`${STDIO_UNAVAILABLE_HEADLINE}（fs）`);
  });

  it("only raises the page-level banner when something is actually affected", () => {
    expect(hasUnstagedServers(true, unavailable)).toBe(true);
    expect(hasUnstagedServers(false, unavailable)).toBe(false);
    expect(hasUnstagedServers(true, {})).toBe(false);
    expect(hasUnstagedServers(undefined, unavailable)).toBe(false);
  });
});
