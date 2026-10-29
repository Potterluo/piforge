"use client";

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";

import { SessionSkillsSection } from "./session-skills-section";

/**
 * The agent's stderr tail (PRD FR-5.10).
 *
 * When a run dies without saying anything in the transcript, this is where the
 * reason is: a missing model, a bad flag, an extension that threw on startup. It
 * refreshes while open, because the usual reason to look is that something is
 * happening right now.
 */
export function SessionLogsSheet({
  sessionId,
  open,
  onOpenChange,
}: {
  sessionId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [lines, setLines] = React.useState<string[]>([]);
  const [truncated, setTruncated] = React.useState(false);
  const [reason, setReason] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const result = await api.sessions.logs(sessionId);
      setLines(result.logs?.lines ?? []);
      setTruncated(Boolean(result.logs?.truncated));
      setReason(result.reason ?? null);
      setError(null);
    } catch (cause) {
      setError(humanizeError(cause, "读取失败"));
    } finally {
      setLoading(false);
    }
  }, [sessionId]);

  React.useEffect(() => {
    if (!open) return;
    void load();
    const timer = window.setInterval(() => void load(), 3_000);
    return () => window.clearInterval(timer);
  }, [open, load]);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col gap-2 sm:max-w-2xl" side="right">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            Agent 日志
            {truncated ? (
              <Badge variant="outline" className="text-[10px]">
                仅保留末尾
              </Badge>
            ) : null}
          </SheetTitle>
          <SheetDescription>
            agent 进程的标准错误输出。模型调用、参数错误、扩展异常都会先出现在这里——转写里什么都没说的时候，先看它。
          </SheetDescription>
        </SheetHeader>

        <div className="flex items-center gap-2">
          <Button size="xs" variant="outline" onClick={() => void load()} disabled={loading}>
            刷新
          </Button>
          {loading ? <span className="text-[11px] text-muted-foreground">读取中…</span> : null}
        </div>

        {/* FR-14.5: what this session actually ran with, next to the agent's
            stderr because both answer "why did it behave like that". */}
        <SessionSkillsSection sessionId={sessionId} />

        {error ? (
          <p className="rounded-md bg-destructive/10 p-2 text-xs text-destructive">{error}</p>
        ) : reason === "runtime_keeps_no_logs" ? (
          <p className="text-xs text-muted-foreground">
            当前运行时没有保留 agent 日志，这个会话没有可看的内容。
          </p>
        ) : lines.length === 0 ? (
          <p className="text-xs text-muted-foreground">还没有输出。agent 启动过、但什么都没写到标准错误。</p>
        ) : (
          <pre
            data-testid="session-logs"
            className="min-h-0 flex-1 overflow-auto rounded-md bg-muted/40 p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap"
          >
            {lines.join("\n")}
          </pre>
        )}
      </SheetContent>
    </Sheet>
  );
}
