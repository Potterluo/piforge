"use client";

import { ChevronDown, Scissors } from "lucide-react";

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Marker, MarkerContent, MarkerIcon } from "@/components/ui/marker";
import { Progress, ProgressLabel, ProgressValue } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { formatCost, formatTokens } from "@/lib/session/format";
import type { ContextMarker, LiveUsage } from "@/lib/session/reduce";
import { useSessionStats } from "@/lib/session/use-session-stats";

/**
 * A compaction is a lossy event: the user deserves to see that it happened and
 * what was kept, inline where it happened (PRD FR-1.9).
 */
export function CompactionMarker({ marker }: { marker: ContextMarker }) {
  const reasonLabel =
    marker.reason === "threshold" ? "自动触发" : marker.reason === "overflow" ? "超出窗口" : "手动触发";

  return (
    <Collapsible>
      <CollapsibleTrigger
        render={
          <Marker
            variant="separator"
            className="cursor-pointer rounded-md outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
          />
        }
      >
        <MarkerIcon>
          <Scissors />
        </MarkerIcon>
        <MarkerContent>
          上下文已压缩（{reasonLabel}）
          {marker.tokensBefore !== undefined && marker.tokensAfter !== undefined
            ? ` ${formatTokens(marker.tokensBefore)} → ${formatTokens(marker.tokensAfter)} tokens`
            : ""}
          {marker.firstKeptEntryId ? `，从 ${marker.firstKeptEntryId} 起保留` : ""}
        </MarkerContent>
        <ChevronDown className="size-3.5 shrink-0" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="rounded-md border border-border bg-card p-2 text-xs whitespace-pre-wrap text-muted-foreground">
          {marker.summary?.trim() || "本次压缩没有返回摘要。"}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

/**
 * Live context usage. SSE `context.usage` is fresher than the stats endpoint,
 * so it wins when present; the 5s poll keeps the value from freezing between
 * runs. Deliberately a plain Progress rather than a chart (PRD §10.4.1).
 */
export function ContextMeter({
  sessionId,
  running,
  usage,
}: {
  sessionId: string;
  running: boolean;
  usage?: LiveUsage;
}) {
  const stats = useSessionStats(sessionId, running);
  const data = stats.data;

  const tokens = usage?.tokens ?? data?.context_tokens ?? 0;
  const window = usage?.window ?? data?.context_window ?? 0;
  const percent =
    usage?.percent ??
    data?.context_percent ??
    (window > 0 ? (tokens / window) * 100 : 0);
  const clamped = Math.max(0, Math.min(100, percent));

  if (stats.isPending) {
    return <Skeleton className="h-4 w-28" />;
  }

  if (stats.isError) {
    return <span className="text-xs text-muted-foreground">上下文用量不可用</span>;
  }

  return (
    <Progress
      value={clamped}
      data-testid="context-meter"
      title={`上下文 ${formatTokens(tokens)} / ${formatTokens(window)} tokens · 花费 ${formatCost(data?.cost_usd)}`}
      className="w-32 gap-x-2 gap-y-1"
    >
      <ProgressLabel className="text-[11px] font-normal text-muted-foreground">
        上下文
      </ProgressLabel>
      <ProgressValue className="text-[11px]">
        {(_formatted, value) => `${Math.round(value ?? 0)}%`}
      </ProgressValue>
    </Progress>
  );
}
