import * as React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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

vi.mock("@/lib/api/client", () => ({
  default: {
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
    },
    git: {
      status: vi.fn(async () => status),
      log: vi.fn(async () => log),
      diff: vi.fn(async () => ({ diffs: [] })),
      stage: vi.fn(async () => ({ ok: true })),
      unstage: vi.fn(async () => ({ ok: true })),
      commit: vi.fn(async () => ({ commit: "ccccccc" })),
    },
  },
}));

import { WorkspacePanels } from "@/components/workspace/workspace-panels";

describe("workspace panels smoke", () => {
  it("renders the four tabs and survives tab switching", async () => {
    render(<WorkspacePanels sessionId="s1" projectId="p1" />);

    expect(screen.getByTestId("workspace-panels")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("file-tree")).toBeInTheDocument());

    // Clicking a leaf opens it in the preview pane.
    const nodes = await screen.findAllByTestId("file-node");
    const fileNode = nodes.find((node) => node.dataset.kind === "file")!;
    fireEvent.click(fileNode);
    await waitFor(() => expect(screen.getByTestId("file-preview-body")).toBeInTheDocument());
    expect(await screen.findByText(/export const a = 1;/)).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("panel-tab-changes"));
    await waitFor(() => expect(screen.getByTestId("git-panel")).toBeInTheDocument());
    await waitFor(() => expect(screen.getAllByText(/未暂存/).length).toBeGreaterThan(0));

    fireEvent.click(screen.getByTestId("panel-tab-git"));
    await waitFor(() => expect(screen.getByTestId("git-graph")).toBeInTheDocument());
    expect(screen.getAllByTestId("git-graph-row")).toHaveLength(2);

    fireEvent.click(screen.getByTestId("panel-tab-terminal"));
    await waitFor(() => expect(screen.getByTestId("terminal-disconnected")).toBeInTheDocument());
  });
});



