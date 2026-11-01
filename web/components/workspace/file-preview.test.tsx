import * as React from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/client", () => {
  const api = {
    files: {
      read: vi.fn(async () => ({
        path: "blob.bin",
        size: 70,
        binary: true,
        truncated: false,
        body: "",
        language: "text",
        mime_type: "application/octet-stream",
        modified: "2025-01-01T00:00:00Z",
      })),
      downloadUrl: (sessionId: string, path: string) =>
        `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}&download=1`,
      inlineUrl: (sessionId: string, path: string) =>
        `/v1/sessions/${sessionId}/files?path=${encodeURIComponent(path)}&inline=1`,
    },
  };
  return { api, default: api };
});

import { FilePreview } from "@/components/workspace/file-preview";
import { api } from "@/lib/api/client";

describe("FilePreview binary state", () => {
  it("shows size and a download for a binary file, not an error", async () => {
    render(<FilePreview sessionId="s1" path="blob.bin" />);

    const card = await screen.findByTestId("preview-binary");
    expect(card).toHaveTextContent("二进制文件，不可预览");
    expect(card).toHaveTextContent("70 B");
    expect(screen.queryByTestId("preview-error")).toBeNull();

    const download = screen.getByTestId("preview-binary-download");
    expect(download).toHaveAttribute("href", expect.stringContaining("download=1"));
  });
});

describe("FilePreview provenance", () => {
  it("badges a file the server reached through a symlink out of the workspace", async () => {
    // The report's case: `leak` -> /etc/passwd. The server returns 200 with the
    // bytes and says where they came from; the panel must not show that as an
    // ordinary workspace file.
    vi.mocked(api.files.read).mockResolvedValueOnce({
      path: "leak",
      size: 32,
      binary: false,
      truncated: false,
      body: "root:x:0:0:root:/root:/bin/sh\n",
      language: "plaintext",
      mime_type: "text/plain",
      modified: "2025-01-01T00:00:00Z",
      outside_workspace: true,
      resolved_path: "/etc/passwd",
    });

    render(<FilePreview sessionId="s1" path="leak" />);

    const badge = await screen.findByTestId("preview-outside-workspace");
    expect(badge).toHaveTextContent("经符号链接指向工作区外");
    expect(badge).toHaveAttribute("title", expect.stringContaining("/etc/passwd"));

    const note = screen.getByTestId("preview-outside-workspace-note");
    expect(note).toHaveTextContent("/etc/passwd");
    expect(note).toHaveTextContent("不在工作区里");
  });

  it("renders no badge for a file that resolves inside the workspace", async () => {
    vi.mocked(api.files.read).mockResolvedValueOnce({
      path: "notes.txt",
      size: 6,
      binary: false,
      truncated: false,
      body: "hello\n",
      language: "plaintext",
      mime_type: "text/plain",
      modified: "2025-01-01T00:00:00Z",
    });

    render(<FilePreview sessionId="s1" path="notes.txt" />);

    await screen.findByTestId("file-preview-body");
    expect(screen.queryByTestId("preview-outside-workspace")).toBeNull();
    expect(screen.queryByTestId("preview-outside-workspace-note")).toBeNull();
  });
});
