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
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { useOnChange } from "@/hooks/use-on-change";

import { SessionSkillsSection } from "./session-skills-section";

/** One read of the agent's stderr tail, with the key it answered. */
interface ReadState {
  signature: string;
  lines: string[];
  truncated: boolean;
  reason: string | null;
  error: string | null;
}

const EMPTY_READ: ReadState = { signature: "", lines: [], truncated: false, reason: null, error: null };

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
  // One read is identified by what it is about: the session, whether the sheet
  // is open, which open it is, and which poll it is. `loading` is derived from
  // that key rather than written at the top of the effect — the sheet refreshes
  // every three seconds, and a synchronous setLoading there is the cascading
  // render `react-hooks/set-state-in-effect` exists to prevent.
  const [read, setRead] = React.useState<ReadState>(EMPTY_READ);
  const [poll, setPoll] = React.useState(0);
  // Reopening re-reads immediately, even if the previous open left a result for
  // the same session: a fresh open is a fresh question.
  const [generation, setGeneration] = React.useState(0);
  if (useOnChange(open) && open) setGeneration((value) => value + 1);

  const signature = `${sessionId}:${open ? "on" : "off"}:${generation}:${poll}`;
  const loading = open && read.signature !== signature;
  const { lines, truncated, reason, error } = read;

  React.useEffect(() => {
    if (!open) return;
    let alive = true;
    api.sessions
      .logs(sessionId)
      .then((result) => {
        if (!alive) return;
        setRead({
          signature,
          lines: result.logs?.lines ?? [],
          truncated: Boolean(result.logs?.truncated),
          reason: result.reason ?? null,
          error: null,
        });
      })
      .catch((cause: unknown) => {
        if (!alive) return;
        // Keep the lines that are already on screen: a poll that failed says
        // nothing about what the agent wrote before it.
        setRead((previous) => ({ ...previous, signature, error: humanizeError(cause, "读取失败") }));
      });
    return () => {
      alive = false;
    };
  }, [open, sessionId, signature]);

  // The poll only asks for another read; it never touches the read itself.
  React.useEffect(() => {
    if (!open) return;
    const timer = window.setInterval(() => setPoll((value) => value + 1), 3_000);
    return () => window.clearInterval(timer);
  }, [open]);

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
          <Button size="xs" variant="outline" onClick={() => setPoll((value) => value + 1)} disabled={loading}>
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
