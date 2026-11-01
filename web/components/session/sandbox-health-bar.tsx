"use client";

import * as React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, LoaderCircle, RotateCcw } from "lucide-react";

import { api } from "@/lib/api/client";
import { Button } from "@/components/ui/button";

/**
 * 「沙箱无响应，正在恢复」 — the visible half of the sandbox watchdog.
 *
 * The defect this answers (adversarial acceptance #2, 2026-11-01): a fork bomb
 * inside one sandbox exhausted its process budget, after which the container
 * could not spawn anything — `docker exec` failed, the platform's own disk
 * measurement timed out, the agent could not start — and the product showed
 * nothing at all. The only recovery was an operator pausing and resuming the
 * sandbox by hand; a user hitting the wall saw a prompt that never answered.
 *
 * The control plane now probes each running sandbox through its own API every
 * few seconds and performs that stop+start itself (backend/internal/httpapi/
 * sandbox_watch.go). This bar is where it says so: the state comes from the
 * server's cached verdict, so the page does not probe anything itself, and the
 * button calls the same repair synchronously for the case where the user does
 * not want to wait for the watchdog.
 *
 * It renders nothing when the sandbox is healthy or has never been probed: a
 * banner on a working session is noise, and "unknown" is not "broken".
 */
export function SandboxHealthBar({ sessionId }: { sessionId: string }) {
  const queryClient = useQueryClient();
  const [result, setResult] = React.useState<string | null>(null);
  const [failed, setFailed] = React.useState<string | null>(null);
  const [pending, setPending] = React.useState(false);

  const sandbox = useQuery({
    queryKey: ["session-sandbox", sessionId],
    queryFn: () => api.sessions.sandbox(sessionId),
    // The server probes on its own schedule; this only reads the verdict, so a
    // slow poll is enough to notice a recovery that happened without this tab.
    refetchInterval: 15_000,
    staleTime: 5_000,
    // A session with no worktree has no sandbox at all, and that is a different
    // state the workspace panel already explains.
    retry: false,
  });

  const unresponsive = sandbox.data?.responsive === false;
  const recovering = Boolean(sandbox.data?.recovering);
  const canRecover = Boolean(sandbox.data?.can_recover);

  // The result of a click outlives the poll that follows it: the banner is about
  // to disappear, and "已恢复" has to be readable for a moment or the click looks
  // like it did nothing.
  React.useEffect(() => {
    if (!result && !failed) return;
    const timer = window.setTimeout(() => {
      setResult(null);
      setFailed(null);
    }, 8_000);
    return () => window.clearTimeout(timer);
  }, [result, failed]);

  const onRecover = async () => {
    setPending(true);
    setFailed(null);
    try {
      const answer = await api.sessions.recoverSandbox(sessionId);
      setResult(answer.message ?? (answer.recovered ? "沙箱已恢复。" : "沙箱正在恢复。"));
      await queryClient.invalidateQueries({ queryKey: ["session-sandbox", sessionId] });
    } catch (error) {
      setFailed(error instanceof Error ? error.message : String(error));
    } finally {
      setPending(false);
    }
  };

  if (!unresponsive && !recovering && !result && !failed) return null;

  const tone =
    failed || (unresponsive && !recovering)
      ? "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400"
      : "border-border bg-muted/40 text-muted-foreground";

  return (
    <div
      data-testid="sandbox-health-bar"
      role="status"
      aria-live="polite"
      className={`mx-3 mt-3 flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-xs ${tone}`}
    >
      {recovering || pending ? (
        <LoaderCircle className="size-3.5 shrink-0 animate-spin" aria-hidden="true" />
      ) : (
        <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
      )}
      <span className="font-medium" data-testid="sandbox-health-title">
        {failed
          ? "沙箱恢复失败"
          : unresponsive || recovering
            ? "沙箱无响应，正在恢复"
            : "沙箱状态"}
      </span>
      <span className="min-w-0 break-words opacity-90" data-testid="sandbox-health-detail">
        {failed
          ? failed
          : result
            ? result
            : recovering || pending
              ? "正在重启沙箱容器（工作区、对话和文件都在卷上，不会丢数据）…"
              : (sandbox.data?.probe?.error ??
                "沙箱容器还在运行，但已经无法执行命令。恢复会重启容器，数据保留。")}
      </span>
      {canRecover ? (
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="ml-auto h-7 gap-1 px-2 text-xs"
          disabled={pending || recovering}
          onClick={() => void onRecover()}
          data-testid="sandbox-recover-button"
        >
          <RotateCcw className={`size-3.5 ${pending ? "animate-spin" : ""}`} aria-hidden="true" />
          {pending || recovering ? "恢复中…" : "恢复"}
        </Button>
      ) : null}
    </div>
  );
}
