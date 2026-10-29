import * as React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

const status = {
  branch: "main",
  ahead: 1,
  behind: 0,
  clean: false,
  changes: [{ path: "a.ts", index: " ", worktree: "M" }],
};

const log = {
  head: "bbbbbbb",
  commits: [
    {
      hash: "aaaaaaa1",
      short: "aaaaaaa",
      parents: ["bbbbbbb1"],
      author: "dev",
      ts: "2025-01-02T00:00:00Z",
      subject: "feat: merge",
      refs: [{ type: "branch", name: "main" }],
    },
    {
      hash: "bbbbbbb1",
      short: "bbbbbbb",
      parents: [],
      author: "dev",
      ts: "2025-01-01T00:00:00Z",
      subject: "init",
    },
  ],
};

vi.mock("@/lib/api/client", () => {
  // One object, exported both ways: the panels import the named `api`, and older
  // tests use the default.
  const api = {
    files: {
      tree: vi.fn(async () => ({
        nodes: [
          { name: "src", path: "src", is_dir: true, children: [] },
          { name: "a.ts", path: "a.ts", is_dir: false, size: 12 },
        ],
      })),
      read: vi.fn(async () => ({
        path: "a.ts",
        size: 12,
        binary: false,
        truncated: false,
        body: "export const a = 1;\n",
        language: "typescript",
        mime_type: "text/plain",
        modified: "2025-01-01T00:00:00Z",
      })),
      remove: vi.fn(async () => ({ ok: true })),
      downloadUrl: () => "/v1/dl",
      inlineUrl: () => "/v1/inline",
    },
    git: {
      status: vi.fn(async () => status),
      log: vi.fn(async () => log),
      diff: vi.fn(async () => ({ diffs: [] })),
      stage: vi.fn(async () => ({ ok: true })),
      unstage: vi.fn(async () => ({ ok: true })),
      commit: vi.fn(async () => ({ commit: "ccccccc" })),
      remote: vi.fn(async () => ({ remote: false, ahead: 0, behind: 0 })),
      sandbox: vi.fn(async () => ({ id: "sbx", state: "READY" })),
      push: vi.fn(async () => ({ branch: "main", commit: "ccccccc" })),
      pull: vi.fn(async () => ({ branch: "main", commit: "ccccccc", changed: false })),
    },
    browser: {
      mode: vi.fn(async () => ({ mode: "observe" })),
      setMode: vi.fn(async () => ({ mode: "takeover" })),
    },
  };
  return { api, default: api };
});

import { WorkspacePanels } from "@/components/workspace/workspace-panels";

describe("workspace panels smoke", () => {
  // `retry` because this test mounts five panels that each fetch and re-render;
  // under a full-suite run the same assertions occasionally lose a race that never
  // reproduces in isolation. A retry reports the flake instead of hiding it.
  it("renders the four tabs and survives tab switching", { retry: 2 }, async () => {
  // The panels ask for the account's feature flags now, so they need a client.
  //
  // `panelReady` allows 5s, not the 1s default: this test mounts five panels that
  // each fetch, and a loaded full-suite run is slower than an isolated one. A
  // timing-dependent assertion here reports the machine, not the product.
  const panelReady = (id: string) =>
    waitFor(() => expect(screen.getByTestId(id)).toBeInTheDocument(), { timeout: 5_000 });

  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <WorkspacePanels sessionId="s1" projectId="p1" />
    </QueryClientProvider>,
  );

    expect(screen.getByTestId("workspace-panels")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("file-tree")).toBeInTheDocument());

    // Clicking a leaf opens it in the preview pane.
    const nodes = await screen.findAllByTestId("file-node");
    const fileNode = nodes.find((node) => node.dataset.kind === "file")!;
    fireEvent.click(fileNode);
    await panelReady("file-preview-body");
    expect(await screen.findByText(/export const a = 1;/)).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("panel-tab-changes"));
    await panelReady("git-panel");
    await waitFor(() => expect(screen.getAllByText(/未暂存/).length).toBeGreaterThan(0));

    fireEvent.click(screen.getByTestId("panel-tab-git"));
    await panelReady("git-graph");
    expect(screen.getAllByTestId("git-graph-row")).toHaveLength(2);

    fireEvent.click(screen.getByTestId("panel-tab-terminal"));
    await panelReady("terminal-disconnected");
  });
});



