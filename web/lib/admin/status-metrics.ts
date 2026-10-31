import type { DeploymentStatus } from "@/lib/types";

/**
 * The two numbers an operator needs to tell apart.
 *
 * 「运行中的 agent」 used to be the process count. A live Pi process is not a
 * run: it is kept warm between turns, and merely opening an idle fork starts
 * one — the acceptance pass watched the number move 1 → 2 with nobody working.
 * The metric is therefore split, and each number says what it measures:
 *
 *  - `runs` counts turns in flight (the runtime's own running/streaming state);
 *  - `processes` counts live agent children, busy or not.
 *
 * Exported separately from the page so the labels can be tested without a
 * browser: a metric whose label drifts from its value is the whole defect.
 */
export interface AgentMetric {
  label: string;
  value: number;
  hint: string;
  testId: string;
}

export function agentMetrics(
  status: Pick<DeploymentStatus, "runs_running" | "agents_running" | "sessions_live">,
): { runs: AgentMetric; processes: AgentMetric } {
  return {
    runs: {
      label: "进行中的运行",
      value: status.runs_running,
      hint: "正在跑一轮的会话；空闲的 agent 进程不算",
      testId: "stat-runs",
    },
    processes: {
      label: "agent 进程",
      value: status.agents_running,
      hint: "已启动的 agent 进程，空闲等下一轮的也算",
      testId: "stat-agent-processes",
    },
  };
}
