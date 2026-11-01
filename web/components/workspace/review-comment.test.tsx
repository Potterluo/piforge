import * as React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/client", () => {
  const api = { sessions: { send: vi.fn() } };
  return { api, default: api };
});

import { ReviewCommentEditor } from "@/components/workspace/review-comment";
import type { CommentAnchor } from "@/lib/git/patch-lines";
import { api } from "@/lib/api/client";

const send = vi.mocked(api.sessions.send);

function anchorWith(lines: number, first = "line 1"): CommentAnchor {
  const quote = Array.from({ length: lines }, (_, i) => (i === 0 ? first : `line ${i + 1}`));
  return {
    path: "notes.txt",
    line: 1,
    endLine: lines,
    side: "new",
    hunk: "",
    quote,
  };
}

function editor(anchor: CommentAnchor) {
  return (
    <ReviewCommentEditor sessionId="s1" anchor={anchor} onClose={() => {}} />
  );
}

beforeEach(() => {
  send.mockReset();
  window.localStorage.clear();
});

describe("ReviewCommentEditor truncation warning", () => {
  it("warns before sending when the selection has more lines than the cap", () => {
    render(editor(anchorWith(30)));

    const warning = screen.getByTestId("review-comment-truncation-warning");
    expect(warning).toHaveTextContent("30 行");
    expect(warning).toHaveTextContent("24 行");
    expect(warning).toHaveTextContent("6 行");
  });

  it("warns when the body is over the rune cap", () => {
    render(editor(anchorWith(2)));

    expect(screen.queryByTestId("review-comment-truncation-warning")).toBeNull();
    fireEvent.change(screen.getByTestId("review-comment-input"), {
      target: { value: "字".repeat(4001) },
    });

    const warning = screen.getByTestId("review-comment-truncation-warning");
    expect(warning).toHaveTextContent("4000");
    expect(warning).toHaveTextContent("截断");
  });

  it("says nothing for a comment that fits", () => {
    render(editor(anchorWith(3)));

    expect(screen.queryByTestId("review-comment-truncation-warning")).toBeNull();
  });
});

describe("ReviewCommentEditor truncation report", () => {
  it("renders the server's report when a send was still cut", async () => {
    send.mockResolvedValueOnce({
      accepted: true,
      disposition: "started",
      queue_position: 0,
      run_id: "run_1",
      review_truncation: {
        quote_lines_kept: 24,
        quote_lines_omitted: 6,
        quote_lines_shortened: 1,
        quote_line_runes_cut: 100,
        body_runes_cut: 1000,
      },
    });
    render(editor(anchorWith(30)));

    fireEvent.change(screen.getByTestId("review-comment-input"), {
      target: { value: "这一行不要动。" },
    });
    fireEvent.click(screen.getByTestId("review-comment-send"));

    const result = await screen.findByTestId("review-comment-truncation-result");
    // The counts come from the server, not from a client-side re-estimate:
    // the transcript shows the string the server actually composed.
    expect(result).toHaveTextContent("仅前 24 行送达");
    expect(result).toHaveTextContent("省略 6 行");
    expect(result).toHaveTextContent("截断 1000 字");

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    const sent = send.mock.calls[0][1];
    expect(sent.review_comment?.quote).toHaveLength(30);
    expect(sent.review_comment?.body).toBe("这一行不要动。");
  });

  it("closes normally when nothing was cut", async () => {
    const onClose = vi.fn();
    send.mockResolvedValueOnce({
      accepted: true,
      disposition: "started",
      queue_position: 0,
      run_id: "run_2",
    });
    render(
      <ReviewCommentEditor sessionId="s1" anchor={anchorWith(2)} onClose={onClose} />,
    );

    fireEvent.change(screen.getByTestId("review-comment-input"), {
      target: { value: "这一行不要动。" },
    });
    fireEvent.click(screen.getByTestId("review-comment-send"));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(screen.queryByTestId("review-comment-truncation-result")).toBeNull();
  });
});
