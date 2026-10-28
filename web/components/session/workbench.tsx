"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { GitBranch, LoaderCircle, PanelRight } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { WorkspacePanels } from "@/components/workspace/workspace-panels";
import { api } from "@/lib/api/client";
import { sessionKindLabel, shortModel } from "@/lib/session/format";
import { qk, useProject, useProviders, useSession } from "@/lib/session/queries";
import { runningToolCount, type Turn } from "@/lib/session/reduce";
import type { StreamStatus } from "@/lib/session/use-session-events";
import type { ForkPoint } from "@/lib/types";

import { BranchTree } from "./branch-tree";
import { Composer, type ModelOption } from "./composer";
import { ContextMeter } from "./context-marker";
import { ForkDialog } from "./fork-dialog";
import { SessionEventsProvider, useSessionEventState } from "./session-events-provider";
import { SessionStream } from "./session-stream";
import { SidecarDock } from "./sidecar-dock";

/** First prose of a user turn, used as the fork dialog's context line. */
function turnPreview(turn: Turn): string {
  return turn.blocks
    .filter((block) => block.kind === "text")
    .map((block) => (block as { text: string }).text)
    .join(" ")
    .slice(0, 240);
}

/**
 * The SSE envelope does not carry Pi entry ids in this build, so a user turn
 * cannot be forked by its own id. The fork-points endpoint knows them, and its
 * previews are the same user text — match on that, exactly first.
 */
function matchForkEntry(turn: Turn, points: ForkPoint[]): string | undefined {
  if (turn.role !== "user") return undefined;
  const text = turnPreview(turn).trim();
  if (!text) return undefined;

  const exact = points.find((point) => point.preview.trim() === text);
  if (exact) return exact.entry_id;

  const head = text.slice(0, 48);
  return points.find((point) => {
    const preview = point.preview.trim();
    return preview.startsWith(head) || text.startsWith(preview.slice(0, 48));
  })?.entry_id;
}

function ConnectionBadge({ status }: { status: StreamStatus }) {
  if (status === "open") return null;
  const label =
    status === "connecting" ? "连接中" : status === "reconnecting" ? "重连中" : "已断开";
  return (
    <Badge variant={status === "offline" ? "destructive" : "outline"}>
      {status === "offline" ? null : <LoaderCircle className="animate-spin" />}
      {label}
    </Badge>
  );
}

function WorkbenchBody({ sessionId, projectId }: { sessionId: string; projectId: string }) {
  const { state, status } = useSessionEventState();
  const session = useSession(sessionId);
  // The first message names an untitled session on the server. Nothing pushes
  // that title, so a settled run is the cue to re-read it.
  const queryClient = useQueryClient();
  const settled = state.settled;
  useEffect(() => {
    if (settled === 0) return;
    void queryClient.invalidateQueries({ queryKey: qk.session(sessionId) });
    void queryClient.invalidateQueries({ queryKey: qk.sessions(projectId) });
  }, [settled, queryClient, sessionId, projectId]);
  const project = useProject(projectId);
  const providers = useProviders();
  const forkPoints = useQuery({
    queryKey: ["session-fork-points", sessionId],
    queryFn: () => api.sessions.forkPoints(sessionId),
    // Fork points only exist once a user message has been recorded, so they
    // appear after the first prompt rather than at first paint. Polling is the
    // cheap way to notice; the payload is tiny.
    refetchInterval: 4_000,
    staleTime: 4_000,
  });

  const [branchesOpen, setBranchesOpen] = useState(false);
  const [sidecarOpen, setSidecarOpen] = useState(false);
  const [forkTarget, setForkTarget] = useState<{ entryId: string; preview?: string } | null>(null);

  const models = useMemo<ModelOption[]>(() => {
    const options: ModelOption[] = [{ value: "inherit", label: "跟随默认" }];
    for (const provider of providers.data?.providers ?? []) {
      if (!provider.enabled) continue;
      for (const spec of provider.models ?? []) {
        options.push({
          value: `${provider.name}/${spec.id}`,
          label: `${provider.name}/${spec.name || spec.id}`,
        });
      }
    }
    return options;
  }, [providers.data]);

  const detail = session.data?.session;
  const title = detail?.title?.trim() || "未命名会话";
  const tools = runningToolCount(state);

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col overflow-hidden" data-session-id={sessionId}>
      <header className="flex h-12 shrink-0 items-center gap-3 border-b border-border px-3">
        <span data-testid="run-state" data-state={state.running ? "running" : "settled"} hidden />
        <div className="flex min-w-0 flex-col">
          <h1 className="truncate text-sm font-medium" data-testid="session-title">\n            {title}\n          </h1>
          <p className="truncate text-[11px] text-muted-foreground">
            {project.data?.project.name ?? "项目"}
            {" · "}
            {sessionKindLabel(detail?.kind)}
            {" · "}
            {shortModel(detail?.model_ref)}
            {detail?.label ? ` · ${detail.label}` : ""}
          </p>
        </div>

        <div className="ml-auto flex shrink-0 items-center gap-3">
          {state.running ? (
            <Badge variant="outline" className="gap-1">
              <span className="size-1.5 animate-pulse rounded-full bg-primary" />
              运行中
              {tools > 0 ? ` · ${tools} 个工具` : ""}
            </Badge>
          ) : null}
          <ConnectionBadge status={status} />
          <ContextMeter sessionId={sessionId} running={state.running} usage={state.usage} />
          <Button size="sm" variant="ghost" onClick={() => setBranchesOpen(true)}>
            <GitBranch />
            分支
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="sidecar-open"
            onClick={() => setSidecarOpen(true)}
          >
            <PanelRight />
            Sidecar
          </Button>
        </div>
      </header>

      <ResizablePanelGroup orientation="horizontal" className="min-h-0 flex-1">
        <ResizablePanel defaultSize="44" minSize="26" className="flex min-h-0 flex-col">
          <SessionStream
            resolveForkEntry={(turn) =>
              turn.entryId ?? matchForkEntry(turn, forkPoints.data?.points ?? [])
            }
            onFork={(entryId) => {
              const point = forkPoints.data?.points.find((item) => item.entry_id === entryId);
              setForkTarget({ entryId, preview: point?.preview });
            }}
          />
          <Composer
            sessionId={sessionId}
            running={state.running}
            queue={state.queue}
            modelRef={detail?.model_ref}
            thinkingLevel={detail?.thinking_level}
            models={models}
          />
        </ResizablePanel>

        <ResizableHandle withHandle />

        <ResizablePanel defaultSize="56" minSize="30" className="min-h-0">
          <WorkspacePanels sessionId={sessionId} projectId={projectId} />
        </ResizablePanel>
      </ResizablePanelGroup>

      <Sheet open={branchesOpen} onOpenChange={setBranchesOpen}>
        <SheetContent side="right" className="flex w-full flex-col gap-3 overflow-y-auto sm:max-w-md">
          <SheetHeader>
            <SheetTitle>分支</SheetTitle>
            <SheetDescription>
              会话树是 Pi 的 append-only 记录；在任意用户消息处都能开出新的平行分支。
            </SheetDescription>
          </SheetHeader>
          <BranchTree sessionId={sessionId} projectId={projectId} />
        </SheetContent>
      </Sheet>

      <SidecarDock sessionId={sessionId} open={sidecarOpen} onOpenChange={setSidecarOpen} />

      <ForkDialog
        open={forkTarget !== null}
        onOpenChange={(open) => {
          if (!open) setForkTarget(null);
        }}
        sessionId={sessionId}
        projectId={projectId}
        entryId={forkTarget?.entryId ?? null}
        preview={forkTarget?.preview}
      />
    </div>
  );
}

/**
 * The workbench: transcript + composer on the left, workspace panels on the
 * right, sidecar as an overlay dock. One SSE subscription feeds all of them.
 */
export function Workbench({ sessionId, projectId }: { sessionId: string; projectId: string }) {
  return (
    <SessionEventsProvider sessionId={sessionId}>
      <WorkbenchBody sessionId={sessionId} projectId={projectId} />
    </SessionEventsProvider>
  );
}

/** Loading frame used while the route params resolve. */
export function WorkbenchSkeleton() {
  return (
    <div className="flex h-svh flex-col">
      <div className="flex h-12 items-center gap-3 border-b px-3">
        <Skeleton className="h-4 w-48" />
        <div className="ml-auto flex gap-2">
          <Skeleton className="h-6 w-24" />
          <Skeleton className="h-7 w-16" />
        </div>
      </div>
      <div className="flex min-h-0 flex-1">
        <div className="flex w-1/2 flex-col gap-3 border-r p-4">
          <Skeleton className="h-20 w-3/4" />
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-20 w-2/3" />
        </div>
        <div className="flex w-1/2 flex-col gap-3 p-4">
          <Skeleton className="h-8 w-1/3" />
          <Skeleton className="h-full w-full" />
        </div>
      </div>
    </div>
  );
}
