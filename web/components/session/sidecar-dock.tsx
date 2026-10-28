"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GitBranch, MessageSquareDashed, ShieldCheck } from "lucide-react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api/client";
import { qk } from "@/lib/session/queries";
import { selectTimeline } from "@/lib/session/reduce";
import { useSessionEvents } from "@/lib/session/use-session-events";
import { cn } from "@/lib/utils";

import { Composer } from "./composer";
import { MessageItem } from "./message-item";

/**
 * Sidecar conversation: shares the main session's worktree and sandbox but has
 * its own transcript, context and model (PRD §5.1.4). It is read-only by
 * default — the badge is honest about that, and the restriction itself lives
 * in the backend tool set, not here.
 */
function SidecarConversation({ sessionId }: { sessionId: string }) {
  const { state, status } = useSessionEvents(sessionId);
  const timeline = selectTimeline(state);

  return (
    <>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        {timeline.length === 0 ? (
          status === "connecting" || status === "reconnecting" ? (
            <div className="flex flex-col gap-2">
              <Skeleton className="h-12 w-3/4" />
              <Skeleton className="h-16 w-full" />
            </div>
          ) : (
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <MessageSquareDashed />
                </EmptyMedia>
                <EmptyTitle>这是旁路对话</EmptyTitle>
                <EmptyDescription>
                  它共享当前工作区，但上下文独立。适合问一句“这条命令是什么意思”，不会打断主会话。
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          )
        ) : (
          <div className="flex flex-col gap-4">
            {timeline.map((item) =>
              item.kind === "turn" ? (
                <MessageItem key={item.key} turn={item.turn} compact />
              ) : null,
            )}
          </div>
        )}
      </div>
      <Composer
        sessionId={sessionId}
        running={state.running}
        queue={state.queue}
        compact
        className="border-t"
        placeholder="问点什么，不影响主会话…"
        testIds={{ input: "sidecar-input", send: "sidecar-send" }}
      />
    </>
  );
}

export function SidecarDock({
  sessionId,
  projectId,
  open,
  onOpenChange,
}: {
  sessionId: string;
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [sidecarId, setSidecarId] = useState<string | null>(null);
  const requestedRef = useRef(false);
  const queryClient = useQueryClient();

  const create = useMutation({
    mutationFn: () => api.sessions.sidecar(sessionId),
    onSuccess: (data) => {
      setSidecarId(data.session.id);
      // A sidecar is a session: the sidebar lists it, so it must learn about it
      // now rather than on the next full reload.
      void queryClient.invalidateQueries({ queryKey: qk.sessions(projectId) });
      void queryClient.invalidateQueries({ queryKey: qk.projects });
    },
    onError: () => {
      // Allow a later re-open to try again; the inline retry also resets this.
      requestedRef.current = false;
    },
  });
  const { mutate } = create;

  const router = useRouter();
  const { state: sidecarState } = useSessionEvents(sidecarId ?? "");
  const running = Boolean(sidecarId) && sidecarState.running;
  const promote = useMutation({
    mutationFn: () => api.sessions.promote(sidecarId ?? ""),
    onSuccess: (data) => {
      toast.success("已提升为分支", { description: "旁路对话的内容一并带走了。" });
      void queryClient.invalidateQueries({ queryKey: qk.sessions(projectId) });
      onOpenChange(false);
      router.push(`/p/${projectId}/s/${data.session.id}`);
    },
    onError: (error) =>
      toast.error("提升失败", {
        description: error instanceof Error ? error.message : "未知错误",
      }),
  });

  // Opening the dock is the intent to use a sidecar, so create one lazily.
  useEffect(() => {
    if (!open || sidecarId || requestedRef.current) return;
    requestedRef.current = true;
    mutate();
  }, [open, sidecarId, mutate]);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        data-testid="sidecar-dock"
        className="flex w-full flex-col gap-0 p-0 sm:max-w-lg"
      >
        <SheetHeader className="shrink-0 gap-1 border-b p-3 pr-10">
          <div className="flex items-center gap-2">
            <SheetTitle className="text-sm">Sidecar</SheetTitle>
            <Badge variant="outline">
              <ShieldCheck />
              只读工具
            </Badge>
          </div>
          <SheetDescription className="text-xs">
            共享当前工作区，独立上下文。默认只读，避免和主会话抢同一份文件。
          </SheetDescription>
        </SheetHeader>

        {create.isPending ? (
          <div className="flex flex-1 items-center justify-center gap-2 text-xs text-muted-foreground">
            <Spinner />
            正在准备 sidecar…
          </div>
        ) : create.isError ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 p-4 text-xs text-muted-foreground">
            <p className="max-w-xs text-center">
              Sidecar 创建失败：
              {create.error instanceof Error ? create.error.message : "未知错误"}
            </p>
            <Button size="sm" variant="outline" onClick={() => create.mutate()}>
              重试
            </Button>
          </div>
        ) : sidecarId ? (
          <div className={cn("flex min-h-0 flex-1 flex-col")}>
            <div className="flex shrink-0 items-center justify-end gap-2 border-b border-border px-3 py-1.5">
              <Button
                size="xs"
                variant="outline"
                data-testid="sidecar-promote"
                disabled={promote.isPending || running}
                title={
                  running
                    ? "这一轮还在跑，等它结束后再提升"
                    : "给这个旁路对话一个自己的工作区，并把它变成分支"
                }
                onClick={() => promote.mutate()}
              >
                <GitBranch />
                提升为分支
              </Button>
            </div>
            <SidecarConversation sessionId={sidecarId} />
          </div>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
