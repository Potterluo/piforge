"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Scissors } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { qk } from "@/lib/session/queries";

/**
 * Manual compaction (PRD FR-1.9), in the context-usage area where the number it
 * changes already lives.
 *
 * Pi refuses to compact a session that is too small. That is an answer, not a
 * fault: the API returns `{compacted:false, reason}`, and the reason is shown on
 * screen (inline and as a toast) rather than being swallowed. When it does work
 * the stats query is invalidated so the meter moves without a reload — the SSE
 * `context.usage` event normally gets there first, but a missed event must not
 * leave a stale percentage over a compacted transcript.
 *
 * The button is disabled when the session has no live agent process: `compact`
 * is a command to a running Pi process, and a fresh session has none, so an
 * enabled button could only produce a 404. The explanation is visible, not only
 * in a title attribute.
 */
export function CompactContextButton({
  sessionId,
  agentAlive,
}: {
  sessionId: string;
  /** A Pi process exists for this session (process_id from GET …/status). */
  agentAlive: boolean;
}) {
  const queryClient = useQueryClient();
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState<{ tone: "ok" | "info"; text: string } | null>(null);

  const compact = async () => {
    if (!agentAlive || pending) return;
    setPending(true);
    try {
      const result = await api.sessions.compact(sessionId);
      if (result.compacted) {
        setNotice({ tone: "ok", text: "上下文已压缩，用量已更新" });
        toast.success("上下文已压缩", { description: "上下文用量已更新，无需刷新" });
      } else {
        // The exact sentence the API produced, e.g. 没有可压缩的上下文.
        const reason = result.reason?.trim() || "没有可压缩的上下文";
        setNotice({ tone: "info", text: reason });
        toast.info("没有压缩上下文", { description: reason });
      }
      await queryClient.invalidateQueries({ queryKey: qk.stats(sessionId) });
    } catch (error) {
      const text = humanizeError(error);
      setNotice({ tone: "info", text });
      toast.error("压缩失败", { description: text });
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="flex items-center gap-1.5">
      <Button
        size="sm"
        variant="ghost"
        data-testid="compact-context"
        disabled={!agentAlive || pending}
        title={
          agentAlive
            ? "把较早的对话压成摘要，保留最近的内容"
            : "这个会话没有正在运行的 agent：先发一条消息，再压缩"
        }
        onClick={() => void compact()}
      >
        {pending ? <LoaderCircle className="animate-spin" /> : <Scissors />}
        压缩上下文
      </Button>
      {!agentAlive ? (
        <span
          data-testid="compact-disabled-reason"
          className="text-[11px] text-muted-foreground"
        >
          没有运行中的 agent
        </span>
      ) : null}
      {notice ? (
        <span
          data-testid="compact-status"
          data-tone={notice.tone}
          className="max-w-56 truncate text-[11px] text-muted-foreground"
          title={notice.text}
        >
          {notice.text}
        </span>
      ) : null}
    </div>
  );
}
