"use client";

import { useMemo } from "react";
import { AlertCircle, LoaderCircle, MessagesSquare, Square } from "lucide-react";

import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from "@/components/ui/message-scroller";
import { Marker, MarkerContent, MarkerIcon } from "@/components/ui/marker";
import { Skeleton } from "@/components/ui/skeleton";
import { selectTimeline, type Turn } from "@/lib/session/reduce";
import { groupTranscript } from "@/lib/session/transcript-runs";
import { cn } from "@/lib/utils";

import { CompactionMarker } from "./context-marker";
import { MessageItem } from "./message-item";
import { RecoveryMarker } from "./recovery-marker";
import { AssistantRun } from "./run-item";
import { useSessionEventState } from "./session-events-provider";

/**
 * The transcript column.
 *
 * Scrolling is entirely the MessageScroller primitive's job: it owns
 * stick-to-bottom, the "jump to latest" button and the anchor behaviour. Any
 * hand-written scroll logic here would fight it (PRD §10.4.1).
 */
export function SessionStream({
  className,
  sessionId,
  sessionModel,
  onFork,
  resolveForkEntry,
}: {
  className?: string;
  /** Needed by the recovery marker: it reads the live checkout summary. */
  sessionId: string;
  /** The model the session is pinned to; inherited sessions have none. */
  sessionModel?: string;
  onFork?: (entryId: string) => void;
  resolveForkEntry?: (turn: Turn) => string | undefined;
}) {
  const { state, status } = useSessionEventState();
  const timeline = useMemo(() => selectTimeline(state), [state]);
  // One reply is several Pi messages. Grouping them here (and not in the
  // reducer) keeps turn ids, per-turn tests and fork entry ids untouched.
  const transcript = useMemo(() => groupTranscript(timeline), [timeline]);

  const loading = timeline.length === 0 && (status === "connecting" || status === "reconnecting");

  return (
    <MessageScrollerProvider
      autoScroll
      defaultScrollPosition="last-anchor"
      scrollPreviousItemPeek={64}
    >
      <MessageScroller className={cn("min-h-0 flex-1", className)}>
        <MessageScrollerViewport
          preserveScrollOnPrepend
          aria-label="会话记录"
          data-testid="session-stream"
        >
          <MessageScrollerContent
            role="log"
            aria-relevant="additions"
            aria-busy={state.streaming}
            className="px-4 py-6"
          >
            {state.error ? (
              <MessageScrollerItem>
                <div
                  role="alert"
                  className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-2.5 text-xs text-destructive"
                >
                  <AlertCircle className="mt-0.5 size-3.5 shrink-0" />
                  <div className="min-w-0">
                    <p className="font-medium">运行时报告了错误</p>
                    <p className="mt-0.5 break-words opacity-80">{state.error}</p>
                  </div>
                </div>
              </MessageScrollerItem>
            ) : null}

            {loading ? (
              <MessageScrollerItem>
                <div className="flex flex-col gap-3">
                  <Skeleton className="h-16 w-2/3" />
                  <Skeleton className="h-24 w-full" />
                  <Skeleton className="h-16 w-1/2" />
                </div>
              </MessageScrollerItem>
            ) : timeline.length === 0 && status === "open" ? (
              <MessageScrollerItem>
                <Empty className="border border-dashed border-border">
                  <EmptyHeader>
                    <EmptyMedia variant="icon">
                      <MessagesSquare />
                    </EmptyMedia>
                    <EmptyTitle>还没有消息</EmptyTitle>
                    <EmptyDescription>
                      在下方描述任务，agent 会在项目工作区里读代码、改文件、跑命令，每一步都显示在这里。
                    </EmptyDescription>
                  </EmptyHeader>
                </Empty>
              </MessageScrollerItem>
            ) : status === "offline" && timeline.length === 0 ? (
              <MessageScrollerItem>
                <Marker>
                  <MarkerIcon>
                    <LoaderCircle />
                  </MarkerIcon>
                  <MarkerContent>无法连接会话事件流，请刷新页面重试。</MarkerContent>
                </Marker>
              </MessageScrollerItem>
            ) : (
              <>
                {transcript.map((item) =>
                  item.kind === "run" ? (
                    <MessageScrollerItem
                      key={item.key}
                      messageId={item.turns[0]?.id}
                      scrollAnchor={false}
                    >
                      <AssistantRun turns={item.turns} sessionModel={sessionModel} />
                    </MessageScrollerItem>
                  ) : item.kind === "turn" ? (
                    <MessageScrollerItem
                      key={item.key}
                      messageId={item.turn.id}
                      scrollAnchor={item.turn.role === "user"}
                    >
                      <MessageItem
                        turn={item.turn}
                        forkEntryId={resolveForkEntry?.(item.turn)}
                        onFork={onFork}
                      />
                    </MessageScrollerItem>
                  ) : item.kind === "note" ? (
                    <MessageScrollerItem key={item.key}>
                      <Marker data-testid="session-note">
                        <MarkerContent>{item.text}</MarkerContent>
                      </Marker>
                    </MessageScrollerItem>
                  ) : item.kind === "interruption" ? (
                    <MessageScrollerItem key={item.key}>
                      <RecoveryMarker
                        sessionId={sessionId}
                        marker={item.marker}
                        active={state.activeInterruption === item.marker.seq}
                      />
                    </MessageScrollerItem>
                  ) : (
                    <MessageScrollerItem key={item.key}>
                      <CompactionMarker marker={item.marker} />
                    </MessageScrollerItem>
                  ),
                )}
                {state.aborted && state.interruptions.length === 0 ? (
                  // The fallback for a session whose stop was recorded before
                  // run.interrupted existed. New stops render the recovery
                  // marker above, where the cause and the surviving work are
                  // named.
                  <MessageScrollerItem>
                    <Marker data-testid="run-interrupted">
                      <MarkerIcon>
                        <Square />
                      </MarkerIcon>
                      <MarkerContent>已中断</MarkerContent>
                    </Marker>
                  </MessageScrollerItem>
                ) : null}
              </>
            )}
          </MessageScrollerContent>
        </MessageScrollerViewport>
        <MessageScrollerButton direction="end" />
      </MessageScroller>
    </MessageScrollerProvider>
  );
}
