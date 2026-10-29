import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@/lib/api/client", () => ({
  api: {
    sessions: {
      get: vi.fn(),
      stats: vi.fn(),
      send: vi.fn(),
      interrupt: vi.fn(),
      update: vi.fn(),
      entries: vi.fn(),
    },
    projects: { get: vi.fn() },
    providers: { list: vi.fn() },
  },
  eventsUrl: () => "/v1/sessions/s1/events",
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/p/p1/s/s1",
}));

vi.mock("@/components/workspace/workspace-panels", () => ({
  WorkspacePanels: () => <div data-testid="workspace-panels" />,
}));

import { api } from "@/lib/api/client";
import { Workbench } from "@/components/session/workbench";

function renderWorkbench() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <Workbench sessionId="s1" projectId="p1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.sessions.get).mockResolvedValue({
    session: {
      id: "s1",
      user_id: "u1",
      project_id: "p1",
      worktree_id: "wt1",
      kind: "main",
      title: "修缓存竞态",
      status: "idle",
      created_at: "2026-01-01T00:00:00.000Z",
      last_active_at: "2026-01-01T00:00:00.000Z",
    },
  });
  vi.mocked(api.sessions.stats).mockResolvedValue({
    session_id: "s1",
    user_messages: 0,
    assistant_messages: 0,
    tool_calls: 0,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    cost_usd: 0,
    context_tokens: 0,
    context_window: 100_000,
    context_percent: 0,
  });
  vi.mocked(api.projects.get).mockResolvedValue({
    project: {
      id: "p1",
      user_id: "u1",
      name: "cache-service",
      repo_url: "",
      default_branch: "main",
      created_at: "2026-01-01T00:00:00.000Z",
      sandbox_state: "READY",
      session_count: 1,
    },
  });
  vi.mocked(api.providers.list).mockResolvedValue({ providers: [] });
});

describe("Workbench", () => {
  it("mounts the transcript, composer and workspace panels", async () => {
    renderWorkbench();

    await waitFor(() => expect(screen.getByTestId("session-stream")).toBeInTheDocument());
    expect(screen.getByTestId("composer-input")).toBeInTheDocument();
    expect(screen.getByTestId("composer-send")).toBeInTheDocument();
    expect(screen.getByTestId("workspace-panels")).toBeInTheDocument();
    expect(screen.getByTestId("sidecar-open")).toBeInTheDocument();
  });

  it("exposes the run state for e2e and starts settled without a stream", async () => {
    renderWorkbench();

    await waitFor(() =>
      expect(screen.getByTestId("run-state")).toHaveAttribute("data-state", "settled"),
    );
    await waitFor(() => expect(screen.getByTestId("context-meter")).toBeInTheDocument());
  });

  it("renders the session title once the session query resolves", async () => {
    renderWorkbench();
    await waitFor(() => expect(screen.getByText("修缓存竞态")).toBeInTheDocument());
  });

  it("replaces the workspace panels with recovery actions when there is no worktree", async () => {
    vi.mocked(api.sessions.get).mockResolvedValue({
      session: {
        id: "s1",
        user_id: "u1",
        project_id: "p1",
        kind: "main",
        title: "没有工作区",
        status: "idle",
        created_at: "2026-01-01T00:00:00.000Z",
        last_active_at: "2026-01-01T00:00:00.000Z",
      },
    });
    renderWorkbench();

    await waitFor(() => expect(screen.getByTestId("no-worktree-state")).toBeInTheDocument());
    expect(screen.queryByTestId("workspace-panels")).not.toBeInTheDocument();
    expect(screen.getByTestId("no-worktree-fix")).toBeInTheDocument();
    expect(screen.getByTestId("no-worktree-delete")).toBeInTheDocument();
    // The old state offered a 重试 button whose request could never succeed.
    expect(screen.queryByText("重试")).not.toBeInTheDocument();
  });
});
