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
