import type { MCPServer, MCPOmission } from "@/lib/types";

/**
 * The client half of stage 4's stdio decision (docs/EXECUTION-BOUNDARY.md §10.1
 * item 8).
 *
 * The server decides which servers this deployment can hand to an agent and sends
 * `unavailable` keyed by server name; the panel never re-derives the rule — a
 * second rule is a second thing that can drift, and "the agent never had that
 * tool" is exactly the kind of fact that must have one source. This module only
 * turns the answer into the sentence the panel shows, and unit tests pin that
 * sentence, because the whole point of the change is that the refusal is visible.
 */

/** The one sentence that has to appear above a stdio row in an isolated deployment. */
export const STDIO_UNAVAILABLE_HEADLINE = "stdio MCP 服务器在隔离模式下不可用";

/**
 * The panel's explanation for a server the deployment cannot stage.
 *
 * It is deliberately built from the server's own name rather than a generic
 * banner only: a user with three servers has to be able to tell which one the
 * agent did not get.
 */
export function unavailableExplanation(
  server: Pick<MCPServer, "name">,
  unavailable: Record<string, MCPOmission> | undefined,
): string | null {
  const omission = unavailable?.[server.name];
  if (!omission) return null;
  const reason = omission.reason?.trim();
  if (!reason) return `${STDIO_UNAVAILABLE_HEADLINE}（${server.name}）`;
  return `${STDIO_UNAVAILABLE_HEADLINE}：${reason}`;
}

/**
 * True when this deployment runs the agent inside a sandbox AND at least one
 * configured server is affected. That is the condition for the page-level
 * banner: a banner with nothing behind it trains people to ignore banners.
 */
export function hasUnstagedServers(
  agentIsolated: boolean | undefined,
  unavailable: Record<string, MCPOmission> | undefined,
): boolean {
  return Boolean(agentIsolated) && Object.keys(unavailable ?? {}).length > 0;
}
