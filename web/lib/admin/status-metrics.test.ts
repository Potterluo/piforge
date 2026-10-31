import { describe, expect, it } from "vitest";

import { agentMetrics } from "@/lib/admin/status-metrics";

/**
 * The admin metric split.
 *
 * The page used to print the live-process count under 「运行中的 agent」. Opening
 * an idle fork started a process and moved it 1 → 2 with no run in flight, which
 * is a metric whose label promises something its value does not measure.
 */
describe("agentMetrics", () => {
  it("reports turns in flight separately from live processes", () => {
    const { runs, processes } = agentMetrics({
      runs_running: 0,
      agents_running: 2,
      sessions_live: 7,
    });

    // Nobody is working, even though two agent processes are warm.
    expect(runs.value).toBe(0);
    expect(processes.value).toBe(2);

    // The label is the fix: a process is not a run, and neither label says the
    // other's number.
    expect(runs.label).toBe("进行中的运行");
    expect(processes.label).toBe("agent 进程");
    expect(runs.hint).toContain("空闲的 agent 进程不算");
    expect(processes.hint).toContain("空闲");
    expect(runs.testId).not.toBe(processes.testId);
  });

  it("a busy turn and an idle process do not collapse into one number", () => {
    const { runs, processes } = agentMetrics({
      runs_running: 3,
      agents_running: 1,
      sessions_live: 2,
    });
    expect(runs.value).toBe(3);
    expect(processes.value).toBe(1);
    // The page must be able to render both at once: three runs can share one
    // process when a session is steered, and one process can idle with zero runs.
    expect(`${runs.value}/${processes.value}`).toBe("3/1");
  });
});
