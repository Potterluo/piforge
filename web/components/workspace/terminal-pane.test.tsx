import * as React from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

/**
 * What the terminal pane says when there is no live shell.
 *
 * The isolated-deployment acceptance measured the worst version of this: the
 * relay closed cleanly after the sandbox shell failed, the hook reported
 * `closed`, and the pane still rendered 「正在连接终端」 — a spinner that never
 * ends over a terminal that was already gone. These cases pin the mapping from
 * state to sentence, and the retry button that makes a dead tab recoverable.
 */
const pty = {
  status: "connecting",
  error: null as string | null,
  notice: null as string | null,
  onData: () => () => {},
  send: () => {},
  resize: () => {},
  reconnect: vi.fn(),
  terminate: () => {},
};

vi.mock("@/lib/terminal/use-pty", () => ({
  usePtySession: () => pty,
}));

import { TerminalPane } from "@/components/workspace/terminal-pane";

const setState = (next: Partial<typeof pty>) => {
  Object.assign(pty, { status: "connecting", error: null, notice: null }, next);
};

describe("TerminalPane connection states", () => {
  it("says it is connecting while the socket is still coming up", () => {
    setState({ status: "connecting" });
    render(<TerminalPane sessionId="ses_connecting" />);
    expect(screen.getByText("正在连接终端")).toBeInTheDocument();
    expect(screen.queryByTestId("terminal-reconnect")).toBeNull();
  });

  it("says the terminal ended when the shell exited, and offers a retry", () => {
    setState({ status: "closed", notice: "shell 已退出" });
    render(<TerminalPane sessionId="ses_closed" />);
    expect(screen.getByText("终端已断开")).toBeInTheDocument();
    expect(screen.getByText("shell 已退出")).toBeInTheDocument();
    expect(screen.getByTestId("terminal-reconnect")).toBeInTheDocument();
  });

  it("shows the relay's failure message instead of a connecting spinner", () => {
    setState({
      status: "error",
      error: "term: sandbox view for tab t1 failed after 7 attempt(s): 500: session not found",
    });
    render(<TerminalPane sessionId="ses_error" />);
    expect(screen.getByText("终端连接失败")).toBeInTheDocument();
    expect(screen.getByText(/session not found/)).toBeInTheDocument();
    expect(screen.queryByText("正在连接终端")).toBeNull();
    expect(screen.getByTestId("terminal-reconnect")).toBeInTheDocument();
  });

  it("says it is reconnecting, with the reason, while the shell is retried", () => {
    setState({ status: "reconnecting", notice: "终端无响应，正在重连…" });
    render(<TerminalPane sessionId="ses_retry" />);
    expect(screen.getByText("正在重连终端")).toBeInTheDocument();
    expect(screen.getByText("终端无响应，正在重连…")).toBeInTheDocument();
  });
});
