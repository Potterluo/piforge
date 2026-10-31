import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@/lib/api/client", () => ({
  api: {
    sessions: {
      send: vi.fn(),
      interrupt: vi.fn(),
      update: vi.fn(),
    },
  },
}));

import { api } from "@/lib/api/client";
import { ApiError } from "@/lib/types";
import { Composer } from "@/components/session/composer";

const send = vi.mocked(api.sessions.send);
const interrupt = vi.mocked(api.sessions.interrupt);

/**
 * The composer refreshes the session and status queries after a re-resolved
 * model, so it needs the same provider the app gives it. A fresh client per
 * render keeps one test's cache out of another's.
 */
function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

function renderComposer(running: boolean) {
  renderWithClient(<Composer sessionId="s1" running={running} queue={[]} />);
  const textarea = screen.getByLabelText("消息输入") as HTMLTextAreaElement;
  fireEvent.change(textarea, { target: { value: "修一下缓存" } });
  return textarea;
}

beforeEach(() => {
  vi.clearAllMocks();
  send.mockResolvedValue({
    accepted: true,
    disposition: "started",
    queue_position: 0,
    run_id: "run-1",
  });
  interrupt.mockResolvedValue({ cleared_steering: [], cleared_follow_up: [] });
});

describe("Composer", () => {
  it("sends on Enter while idle without a streaming behaviour", async () => {
    const textarea = renderComposer(false);

    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    const [, input] = send.mock.calls[0];
    expect(input.text).toBe("修一下缓存");
    expect(input).not.toHaveProperty("streaming_behavior");
  });

  it("queues with follow_up on Enter while a run is in flight", async () => {
    const textarea = renderComposer(true);

    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    expect(send.mock.calls[0][1].streaming_behavior).toBe("follow_up");
  });

  it("steers with ⌘/Ctrl+Enter while a run is in flight", async () => {
    const textarea = renderComposer(true);

    fireEvent.keyDown(textarea, { key: "Enter", metaKey: true });

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    expect(send.mock.calls[0][1].streaming_behavior).toBe("steer");
  });

  it("does not send on Shift+Enter", async () => {
    const textarea = renderComposer(false);

    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: true });

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(send).not.toHaveBeenCalled();
  });

  it("interrupts on Esc and restores the cleared queue text", async () => {
    interrupt.mockResolvedValue({
      cleared_steering: ["改口：别动那个函数"],
      cleared_follow_up: ["顺便把测试修了"],
    });
    const textarea = renderComposer(true);

    fireEvent.keyDown(textarea, { key: "Escape" });

    await waitFor(() => expect(interrupt).toHaveBeenCalledWith("s1"));
    await waitFor(() => expect(textarea.value).toContain("改口：别动那个函数"));
    expect(textarea.value).toContain("顺便把测试修了");
  });

  it("offers the two queued actions as distinct labelled buttons", async () => {
    renderComposer(true);

    const followUp = screen.getByRole("button", { name: /下一轮执行/ });
    const steer = screen.getByRole("button", { name: /立即打断并改口/ });

    fireEvent.click(followUp);
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    expect(send.mock.calls[0][1].streaming_behavior).toBe("follow_up");

    // The textarea is cleared after a successful send, so type again.
    const textarea = screen.getByLabelText("消息输入");
    fireEvent.change(textarea, { target: { value: "改成另一种写法" } });

    fireEvent.click(steer);
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    expect(send.mock.calls[1][1].streaming_behavior).toBe("steer");
  });

  it("calls interrupt from the stop button", async () => {
    renderComposer(true);

    fireEvent.click(screen.getByRole("button", { name: /停止/ }));

    await waitFor(() => expect(interrupt).toHaveBeenCalledTimes(1));
  });

  it("renders queued prompts above the input", () => {
    renderWithClient(
      <Composer
        sessionId="s1"
        running
        queue={[
          { text: "下一轮要跑的", behavior: "follow_up" },
          { text: "马上改口", behavior: "steer" },
        ]}
      />,
    );

    const queue = screen.getByTestId("composer-queue");
    expect(queue).toHaveTextContent("下一轮要跑的");
    expect(queue).toHaveTextContent("马上改口");
    expect(queue).toHaveTextContent("立即改口");
  });

  it("shows a parallel-limit refusal as the server's sentence, keeping the draft", async () => {
    const message = "并行上限是 1 个分支，现在有 1 个在运行（hello），先让其中一个跑完";
    send.mockRejectedValue(
      new ApiError(429, { error: { code: "rate_limited", message } }),
    );
    const textarea = renderComposer(false);

    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() =>
      expect(screen.getByTestId("parallel-limit-reason")).toHaveTextContent(message),
    );
    expect(screen.getByTestId("parallel-limit-reason")).toHaveTextContent(
      "并行上限是 1 个分支",
    );
    // A refusal is not a sent message: the draft must still be editable.
    expect(textarea.value).toBe("修一下缓存");
  });
});
