"use client";

import * as React from "react";

import api from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import type { TestRun } from "@/lib/types";

/**
 * The test-results panel's data source (PRD FR-12.5).
 *
 * It polls rather than subscribing to the session SSE stream for one reason:
 * the run rows are written by the *server's* tool-event pump, but the panel has
 * to reflect runs the transcript never carries (a command typed in the
 * terminal) as well. A small GET every few seconds is one source of truth for
 * both, and it makes "settles without a reload" true for either path.
 *
 * The cadence is the point: 1.2s while something is running, 4s otherwise, so
 * 运行中 turns into a result promptly without turning an idle page into a
 * stream of requests.
 */
export interface TestRunsState {
  runs: TestRun[];
  loading: boolean;
  error: string | null;
  reload: () => void;
}

const RUNNING_POLL_MS = 1200;
const IDLE_POLL_MS = 4000;

export function useTestRuns(sessionId: string): TestRunsState {
  const [runs, setRuns] = React.useState<TestRun[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);
  const [nonce, setNonce] = React.useState(0);

  const reload = React.useCallback(() => setNonce((value) => value + 1), []);

  React.useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const tick = async () => {
      try {
        const data = await api.sessions.testRuns(sessionId);
        if (!alive) return;
        const next = data.runs ?? [];
        setRuns(next);
        setError(null);
        setLoading(false);
        timer = setTimeout(tick, next.some((run) => run.status === "running") ? RUNNING_POLL_MS : IDLE_POLL_MS);
      } catch (caught) {
        if (!alive) return;
        setError(humanizeError(caught, "无法读取测试结果"));
        setLoading(false);
        // Keep polling on a transient failure: the panel should recover on its
        // own rather than requiring a reload of the whole page.
        timer = setTimeout(tick, IDLE_POLL_MS);
      }
    };

    void tick();

    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, [sessionId, nonce]);

  return { runs, loading, error, reload };
}
