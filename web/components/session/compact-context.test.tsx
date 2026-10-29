import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@/lib/api/client", () => ({
  api: {
    sessions: {
      stats: vi.fn(),
      compact: vi.fn(),
    },
  },
}));

import { api } from "@/lib/api/client";
import { CompactContextButton } from "@/components/session/compact-context";
import { ContextMeter } from "@/components/session/context-marker";

const stats = vi.mocked(api.sessions.stats);
const compact = vi.mocked(api.sessions.compact);

function statsBody(contextPercent: number) {
  return {
    session_id: "s1",
    user_messages: 0,
    assistant_messages: 0,
    tool_calls: 0,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    cost_usd: 0,
    context_tokens: Math.round(contextPercent * 1000),
    context_window: 100_000,
    context_percent: contextPercent,
  };
}

/** The compact action and the meter it has to move, mounted together. */
function renderArea(agentAlive: boolean) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <CompactContextButton sessionId="s1" agentAlive={agentAlive} />
      <ContextMeter sessionId="s1" running={false} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  stats.mockResolvedValue(statsBody(75));
});

describe("CompactContextButton (FR-1.9)", () => {
  it("is disabled, with a visible explanation, when no agent is running", () => {
    renderArea(false);

    const button = screen.getByTestId("compact-context");
    expect(button).toBeDisabled();
    expect(screen.getByTestId("compact-disabled-reason")).toHaveTextContent("没有运行中的 agent");
    expect(compact).not.toHaveBeenCalled();
  });

  it("shows the API's own reason on screen when nothing was compacted", async () => {
    compact.mockResolvedValue({ compacted: false, reason: "没有可压缩的上下文" });
    renderArea(true);

    fireEvent.click(screen.getByTestId("compact-context"));

    await waitFor(() =>
      expect(screen.getByTestId("compact-status")).toHaveTextContent("没有可压缩的上下文"),
    );
    // The no-op is an answer, not an error: the action stays usable.
    expect(screen.getByTestId("compact-context")).toBeEnabled();
  });

  it("reports success and drops the meter without a reload", async () => {
    compact.mockResolvedValue({ compacted: true });
    // The second read is what the invalidated stats query fetches.
    stats.mockResolvedValueOnce(statsBody(75)).mockResolvedValue(statsBody(16));
    renderArea(true);

    await waitFor(() =>
      expect(screen.getByTestId("context-meter")).toHaveTextContent("75%"),
    );
    fireEvent.click(screen.getByTestId("compact-context"));

    await waitFor(() =>
      expect(screen.getByTestId("compact-status")).toHaveTextContent("上下文已压缩"),
    );
    await waitFor(() =>
      expect(screen.getByTestId("context-meter")).toHaveTextContent("16%"),
    );
  });
});
