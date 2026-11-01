import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@/lib/api/client", () => ({
  api: {
    sessions: {
      sandbox: vi.fn(),
      recoverSandbox: vi.fn(),
    },
  },
}));

import { api } from "@/lib/api/client";
import { SandboxHealthBar } from "./sandbox-health-bar";

const sandbox = vi.mocked(api.sessions.sandbox);
const recoverSandbox = vi.mocked(api.sessions.recoverSandbox);

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("SandboxHealthBar", () => {
  /**
   * The banner is the visible half of the watchdog. Its defect, from adversarial
   * acceptance #2: a fork storm left the sandbox unable to fork and the product
   * said nothing at all — the user sent a prompt into a container that could not
   * start its agent and got silence.
   */
  it("says 沙箱无响应，正在恢复 when the watchdog's probe failed", async () => {
    sandbox.mockResolvedValue({
      id: "sbx",
      state: "READY",
      responsive: false,
      can_recover: true,
      probe: { ok: false, error: "unable to spawn stage-1: Resource temporarily unavailable" },
    });
    renderWithClient(<SandboxHealthBar sessionId="s1" />);
    const bar = await screen.findByTestId("sandbox-health-bar");
    expect(bar).toHaveTextContent("沙箱无响应，正在恢复");
    expect(screen.getByTestId("sandbox-health-detail")).toHaveTextContent("unable to spawn stage-1");
    expect(screen.getByTestId("sandbox-recover-button")).toBeEnabled();
  });

  it("runs the recovery the button promises and shows the server's answer", async () => {
    sandbox.mockResolvedValue({
      id: "sbx",
      state: "READY",
      responsive: false,
      can_recover: true,
    });
    recoverSandbox.mockResolvedValue({
      state: "READY",
      recovered: true,
      restarted: true,
      elapsed_ms: 5900,
      message: "沙箱已恢复（用时 5.9 秒）。对话和工作区都还在，可以继续。",
    });
    renderWithClient(<SandboxHealthBar sessionId="s1" />);
    fireEvent.click(await screen.findByTestId("sandbox-recover-button"));
    await waitFor(() => expect(recoverSandbox).toHaveBeenCalledWith("s1"));
    expect(await screen.findByText(/沙箱已恢复/)).toBeInTheDocument();
  });

  it("stays out of the way when the sandbox is healthy, and when nobody probed it", async () => {
    sandbox.mockResolvedValue({ id: "sbx", state: "READY", responsive: true, can_recover: true });
    const first = renderWithClient(<SandboxHealthBar sessionId="s1" />);
    await waitFor(() => expect(sandbox).toHaveBeenCalled());
    expect(screen.queryByTestId("sandbox-health-bar")).not.toBeInTheDocument();
    first.unmount();

    // `responsive: null` is "not probed yet", not "broken": a banner on a session
    // that was never asked about is noise.
    sandbox.mockResolvedValue({ id: "sbx", state: "READY", responsive: null });
    renderWithClient(<SandboxHealthBar sessionId="s1" />);
    await waitFor(() => expect(sandbox).toHaveBeenCalledTimes(2));
    expect(screen.queryByTestId("sandbox-health-bar")).not.toBeInTheDocument();
  });

  it("says the recovery is already running instead of offering a second one", async () => {
    sandbox.mockResolvedValue({
      id: "sbx",
      state: "READY",
      responsive: false,
      recovering: true,
      can_recover: true,
    });
    renderWithClient(<SandboxHealthBar sessionId="s1" />);
    const button = await screen.findByTestId("sandbox-recover-button");
    expect(button).toBeDisabled();
    expect(button).toHaveTextContent("恢复中…");
  });
});
