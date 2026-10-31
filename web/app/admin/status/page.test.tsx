import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { DeploymentStatus } from "@/lib/types";

import AdminStatusPage from "./page";

/**
 * The admin page's tenant block is the operator's answer to "what is this
 * account allowed to use, and how close is it". The API block is tested in Go
 * (`httpapi/admin_tenant_limits_test.go`); this pins that the page renders it —
 * the limits, the per-tenant override badge and the sandbox count against the cap.
 */

const status: DeploymentStatus = {
  users: 3,
  users_active: 3,
  projects: 2,
  sessions: 4,
  sessions_live: 1,
  agents_running: 1,
  runs_running: 1,
  agents_live: 1,
  sandboxes: 2,
  sandboxes_up: 1,
  providers: 1,
  skills: 0,
  mcp_servers: 0,
  events: 10,
  parallel_limit: 3,
  parallel_limit_max: 8,
  browsers_running: 0,
  browser_sandboxes: [],
  browser_idle_after: "1m0s",
  storage: [],
  storage_total_bytes: 0,
  sandbox_usage: [],
  sandbox_disk_quota: 0,
  tenant_limits: {
    cpus: "1",
    memory: "2g",
    pids: 512,
    max_sandboxes: 2,
    source: "/etc/piforge/tenant-limits.json",
    network_mode: "tenant",
    enforced: true,
    tenants: [
      {
        user_id: "usr_1",
        email: "dev@piforge.local",
        cpus: "1",
        memory: "2g",
        pids: 512,
        max_sandboxes: 2,
        sandboxes: 1,
        effective: true,
        override: false,
      },
      {
        user_id: "usr_2",
        email: "heavy@piforge.local",
        cpus: "2",
        memory: "3g",
        pids: 1024,
        max_sandboxes: 2,
        sandboxes: 2,
        effective: true,
        override: true,
      },
    ],
  },
};

const statusSpy = vi.fn(async () => status);

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return { ...actual, api: { ...actual.api, admin: { ...actual.api.admin, status: () => statusSpy() } } };
});

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AdminStatusPage />
    </QueryClientProvider>,
  );
}

describe("admin status: tenant limits", () => {
  beforeEach(() => statusSpy.mockClear());

  it("shows the network mode, the policy source and each tenant's effective limits", async () => {
    renderPage();
    await waitFor(() => expect(screen.getByTestId("stat-tenant-limits")).toBeInTheDocument());

    // The network mode is the difference between "tenants cannot reach each
    // other" and "an API key separates them", so the page names it.
    expect(screen.getByTestId("tenant-limits-network")).toHaveTextContent("每租户一个网络");

    const dev = screen.getByTestId("tenant-limit-usr_1");
    expect(dev).toHaveTextContent("dev@piforge.local");
    expect(dev).toHaveTextContent("1 CPU");
    expect(dev).toHaveTextContent("2g");
    expect(dev).toHaveTextContent("512 进程");
    expect(dev).toHaveTextContent("1 / 2 个沙箱");
    expect(dev).not.toHaveTextContent("单独配置");

    const heavy = screen.getByTestId("tenant-limit-usr_2");
    expect(heavy).toHaveTextContent("2 CPU");
    expect(heavy).toHaveTextContent("3g");
    expect(heavy).toHaveTextContent("单独配置");
    // At the cap: the row is flagged, which is the whole point of showing both
    // numbers next to each other.
    expect(heavy).toHaveAttribute("data-at-cap", "true");
    expect(heavy).toHaveTextContent("已达上限");
  });

  it("says so when the deployment configured no quota at all", async () => {
    statusSpy.mockImplementationOnce(async () => ({
      ...status,
      tenant_limits: {
        max_sandboxes: 0,
        source: "none",
        network_mode: "shared",
        enforced: false,
        tenants: [],
      },
    }));
    renderPage();
    await waitFor(() => expect(screen.getByTestId("stat-tenant-limits")).toBeInTheDocument());
    expect(screen.getByTestId("stat-tenant-limits")).toHaveTextContent("没有配置任何租户配额");
    expect(screen.getByTestId("tenant-limits-network")).toHaveTextContent("共享网络");
  });
});
