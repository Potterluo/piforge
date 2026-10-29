import * as React from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { TestResultsPanel, TestTabBadge } from "@/components/workspace/test-panel";
import type { TestRun } from "@/lib/types";

function run(overrides: Partial<TestRun>): TestRun {
  return {
    id: "run_1",
    session_id: "s1",
    source: "agent",
    runner: "go test",
    command: "go test ./...",
    status: "passed",
    total: 2,
    passed: 2,
    failed: 0,
    skipped: 0,
    duration_ms: 31,
    failures: [],
    started_at: "2025-03-01T10:00:00Z",
    ...overrides,
  };
}

function renderPanel(runs: TestRun[], onOpenLocation = vi.fn()) {
  render(
    <TestResultsPanel
      sessionId="s1"
      runs={runs}
      loading={false}
      error={null}
      onRetry={() => {}}
      onOpenLocation={onOpenLocation}
    />,
  );
  return onOpenLocation;
}

describe("TestResultsPanel", () => {
  it("renders a passing run with its counts and duration", () => {
    renderPanel([run({})]);
    const row = screen.getByTestId("test-run");
    expect(row).toHaveAttribute("data-status", "passed");
    expect(row).toHaveTextContent("go test ./...");
    expect(row).toHaveTextContent("✅ 2");
    expect(row).toHaveTextContent("通过");
    expect(row).toHaveTextContent("31ms");
  });

  it("does not render a fabricated zero when the runner omitted totals", () => {
    renderPanel([run({ total: 0, passed: 0, failed: 0, skipped: 0 })]);
    const row = screen.getByTestId("test-run");
    expect(row).toHaveTextContent("通过");
    expect(row).not.toHaveTextContent("✅ 0");
  });

  it("expands a failed run to its failure, and opens file:line", () => {
    const onOpen = renderPanel([
      run({
        status: "failed",
        total: 3,
        passed: 2,
        failed: 1,
        failures: [
          {
            name: "TestAdd",
            file: "calc_test.go",
            line: 12,
            message: "1 + 2 = 3, want 4",
          },
        ],
      }),
    ]);

    const row = screen.getByTestId("test-run");
    expect(row).toHaveAttribute("data-status", "failed");
    expect(screen.queryByTestId("test-failure")).toBeNull();

    fireEvent.click(within(row).getAllByRole("button")[0]);

    const failure = screen.getByTestId("test-failure");
    expect(failure).toHaveTextContent("TestAdd");
    expect(failure).toHaveTextContent("1 + 2 = 3, want 4");
    expect(failure).toHaveAttribute("data-file", "calc_test.go");
    expect(failure).toHaveAttribute("data-line", "12");

    fireEvent.click(screen.getByTestId("test-failure-location"));
    expect(onOpen).toHaveBeenCalledWith("calc_test.go", 12);
  });

  it("shows 未识别格式 and the raw tail instead of inventing counts", () => {
    renderPanel([
      run({
        id: "run_bad",
        status: "unrecognized",
        runner: "go test",
        command: "go test ./missing",
        total: 0,
        passed: 0,
        failed: 0,
        output_tail: "no Go files in /work/missing",
      }),
    ]);

    const row = screen.getByTestId("test-run");
    expect(row).toHaveAttribute("data-status", "unrecognized");
    expect(row).toHaveTextContent("未识别格式");

    fireEvent.click(within(row).getAllByRole("button")[0]);
    expect(screen.getByTestId("test-raw-tail")).toHaveTextContent("no Go files in /work/missing");
  });

  it("shows a running run with its command", () => {
    renderPanel([run({ status: "running", total: 0, passed: 0, duration_ms: 0 })]);
    const row = screen.getByTestId("test-run");
    expect(row).toHaveAttribute("data-status", "running");
    expect(row).toHaveTextContent("运行中");
    expect(row).toHaveTextContent("go test ./...");
  });

  it("says what will appear and how to trigger it when empty", () => {
    renderPanel([]);
    const empty = screen.getByTestId("test-empty");
    expect(empty).toHaveTextContent("还没有测试运行");
    expect(empty).toHaveTextContent("go test");
    expect(empty).toHaveTextContent("pytest");
    expect(empty).toHaveTextContent("终端");
  });
});

describe("TestTabBadge", () => {
  it("summarises the latest run without opening the panel", () => {
    const { rerender } = render(<TestTabBadge runs={[run({ status: "failed", failed: 1 })]} />);
    expect(screen.getByTestId("test-tab-badge")).toHaveAttribute("data-state", "fail");
    expect(screen.getByTestId("test-tab-badge")).toHaveTextContent("1");

    rerender(<TestTabBadge runs={[run({ status: "passed", passed: 4 })]} />);
    expect(screen.getByTestId("test-tab-badge")).toHaveAttribute("data-state", "pass");

    rerender(<TestTabBadge runs={[run({ status: "running" })]} />);
    expect(screen.getByTestId("test-tab-badge")).toHaveAttribute("data-state", "running");

    // A pass with no reported count stays a check without a "0".
    rerender(<TestTabBadge runs={[run({ status: "passed", total: 0, passed: 0 })]} />);
    expect(screen.getByTestId("test-tab-badge")).toHaveAttribute("data-state", "pass");
    expect(screen.getByTestId("test-tab-badge")).not.toHaveTextContent("0");

    rerender(<TestTabBadge runs={[]} />);
    expect(screen.queryByTestId("test-tab-badge")).toBeNull();
  });
});
