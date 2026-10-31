"use client";

import * as React from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangleIcon, GitBranch, GitMerge, LoaderCircle, PanelRight, ScrollText } from "lucide-react";
import type { PanelImperativeHandle } from "react-resizable-panels";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
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
import { humanizeError } from "@/lib/errors";
import { ApiError } from "@/lib/types";
import { toast } from "sonner";
import { sessionKindLabel } from "@/lib/session/format";
import { sessionModelLabel } from "@/lib/session/model-display";
import { qk, useProject, useProviders, useSession, useSessionSkills, useSkills } from "@/lib/session/queries";
import { buildModelChoices } from "@/lib/session/model-options";
import { collectBashOutput, runningToolCount, selectTimeline, type Turn } from "@/lib/session/reduce";
import { capFollowText } from "@/lib/session/follow";
import type { StreamStatus } from "@/lib/session/use-session-events";
import type { ForkPoint } from "@/lib/types";

import { BranchTree } from "./branch-tree";
import { AdoptDialog } from "./adopt-dialog";
import { CompactContextButton } from "./compact-context";
import { Composer, type ModelOption } from "./composer";
import { ContextMeter } from "./context-marker";
import { ForkDialog } from "./fork-dialog";
import { NoModelBar, hasUsableModel } from "./no-model-bar";
import { FirstRunSetup } from "@/components/setup/first-run-setup";
import { NoWorktreeState } from "./no-worktree-state";
import { SessionEventsProvider, useSessionEventState } from "./session-events-provider";
import { SessionStream } from "./session-stream";
import { SessionLogsSheet } from "./session-logs-sheet";
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

/**
 * The page for a session id the server does not know.
 *
 * A deleted or mistyped id used to render the whole live workbench: an enabled
 * composer pointed at nothing, 重连中 forever while the event stream retried a
 * 404, and every workspace panel firing its own 404. There is nothing to type
 * into, so this says what happened and offers the two ways back.
 */
export function SessionNotFound({
  sessionId,
  projectId,
}: {
  sessionId: string;
  projectId: string;
}) {
  return (
    <div
      data-testid="session-not-found"
      className="flex h-full min-h-0 flex-1 items-center justify-center p-6"
    >
      <Empty className="max-w-md border border-dashed border-border">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <AlertTriangleIcon />
          </EmptyMedia>
          <EmptyTitle>会话不存在</EmptyTitle>
          <EmptyDescription>
            这个会话可能已经被删除，或者链接里的 id 不对（<code className="font-mono">{sessionId}</code>
            ）。没有可以继续对话的内容。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" data-testid="session-not-found-project" render={<Link href={`/p/${projectId}`} />}>
            返回项目
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="session-not-found-home"
            render={<Link href="/" />}
          >
            所有项目
          </Button>
        </EmptyContent>
      </Empty>
    </div>
  );
}

/** The session query, passed down so the page reads the session exactly once. */
type SessionQuery = ReturnType<typeof useSession>;

function WorkbenchBody({
  sessionId,
  projectId,
  session,
}: {
  sessionId: string;
  projectId: string;
  session: SessionQuery;
}) {
  const { state, status } = useSessionEventState();
  /**
   * The server's refusal for a send that had no usable model behind it.
   *
   * It is kept here, above the composer, instead of in a toast: a toast vanishes
   * while the message is still in the input, which is exactly the state a new
   * account needs explained. The API client announces the code; the bar below
   * carries the server's own sentence.
   */
  const [modelRefusal, setModelRefusal] = useState<string | null>(null);
  useEffect(() => {
    const onNoModel = (event: Event) => {
      setModelRefusal(humanizeError((event as CustomEvent).detail));
    };
    window.addEventListener("piforge:no-model", onNoModel);
    return () => window.removeEventListener("piforge:no-model", onNoModel);
  }, []);
  // A later send that starts a run means the configuration now works (or the
  // user picked a working model); the old refusal must not outlive it.
  useEffect(() => {
    if (state.running) setModelRefusal(null);
  }, [state.running]);
  // The first message names an untitled session on the server. Nothing pushes
  // that title, so a settled run is the cue to re-read it.
  const queryClient = useQueryClient();
  const settled = state.settled;
  useEffect(() => {
    if (settled === 0) return;
    void queryClient.invalidateQueries({ queryKey: qk.session(sessionId) });
    void queryClient.invalidateQueries({ queryKey: qk.sessions(projectId) });
    // A settled run is also when the agent process becomes visible as alive
    // (FR-1.9's compact action needs one), so re-read the status then.
    void queryClient.invalidateQueries({ queryKey: ["session-status", sessionId] });
  }, [settled, queryClient, sessionId, projectId]);
  const project = useProject(projectId);
  const providers = useProviders();
  // The `/` palette lists what the account can invoke, and the composer's chip
  // shows the version this session was actually mounted with (FR-14.5/14.6).
  const skills = useSkills();
  const sessionSkills = useSessionSkills(sessionId);
  const runtime = useQuery({
    queryKey: ["session-status", sessionId],
    queryFn: () => api.sessions.status(sessionId),
    staleTime: 10_000,
    // The status carries process_id, which is the only honest "is there an agent
    // to compact" signal; a freshly started process must become visible without
    // a reload (FR-1.9).
    refetchInterval: 15_000,
  });
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
  const [logsOpen, setLogsOpen] = useState(false);
  const [sidecarOpen, setSidecarOpen] = useState(false);
  const [adoptOpen, setAdoptOpen] = useState(false);
  const [forkTarget, setForkTarget] = useState<{ entryId: string; preview?: string } | null>(null);

  /**
   * The right panel's folded state, owned here rather than inside the panels:
   * collapsing has to shrink the *resizable panel* to the rail's width, or the
   * column keeps its 56% and the icons sit at the start of a dead band of
   * background (the bug behind "it collapsed but the chat column did not widen").
   */
  const [panelsCollapsed, setPanelsCollapsed] = useState(false);
  const panelRef = useRef<PanelImperativeHandle | null>(null);
  useEffect(() => {
    const panel = panelRef.current;
    if (!panel) return;
    if (panelsCollapsed) {
      if (!panel.isCollapsed()) panel.collapse();
    } else if (panel.isCollapsed()) {
      panel.expand();
    }
  }, [panelsCollapsed]);

  const models = useMemo<ModelOption[]>(() => {
    // A provider without a key is listed disabled: choosing it used to cost
    // three automatic retries and a raw 502. Hiding it entirely would leave
    // "why is my provider gone?" unanswered, so the row keeps its reason.
    const { options } = buildModelChoices(providers.data?.providers);
    return [{ value: "inherit", label: "默认模型" }, ...options];
  }, [providers.data]);

  const detail = session.data?.session;
  const title = detail?.title?.trim() || "未命名会话";
  const tools = runningToolCount(state);
  // A session without a pinned model follows the deployment default, and just
  // saying 默认模型 hides which model that is. The first selectable option is that
  // default, so show it and mark it as inherited.
  // `status.model_ref` is what the runtime actually loaded (the gateway rewrites the
  // provider name); the session's own field is what the user picked. Guessing from
  // the first provider option produced a header that named a model nobody used.
  const runtimeModel = runtime.data?.model_ref;
  // A model is only usable when the account can actually send to it. The picker
  // lists every enabled provider, including inherited ones with no key, so the
  // header cannot infer readiness from `models.length`.
  const hasModel = providers.data ? hasUsableModel(providers.data.providers) : undefined;
  // "Agent alive" is not "a run is in flight": Pi keeps the process warm between
  // turns, and compaction is possible exactly then. process_id is present only
  // while a process exists, so a fresh session honestly shows the action disabled.
  const agentAlive = Boolean(runtime.data?.process_id);
  const timeline = useMemo(() => selectTimeline(state), [state]);
  // The agent's own bash output, which the follow view mirrors (FR-9.2). Capped
  // to the newest lines so a long-running command cannot grow the page without
  // bound; the follow pane renders the tail of this string as it changes.
  const followText = useMemo(
    () => capFollowText(collectBashOutput(timeline)),
    [timeline],
  );

  // The header names a model a person can act on. `status.resolved_model_ref` is
  // the control plane's answer (it resolves the deployment default and the
  // fallback a dead reference would use), so a session whose provider was
  // deleted shows the model the next send will actually run on — and an account
  // with nothing to send to reads 未配置. `unknown` never renders: Pi reports it
  // when it has no model, and it is not a model name.
  const effectiveModel = sessionModelLabel({
    stored: detail?.model_ref,
    resolved: runtime.data?.resolved_model_ref ?? runtimeModel,
    unavailable: runtime.data?.model_unavailable,
    hasUsableModel: hasModel,
  });
  // Branches are titled from their label; repeating it under the title reads as a
  // stutter rather than information.
  const showLabel = Boolean(detail?.label) && detail?.label !== title;

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col overflow-hidden" data-session-id={sessionId}>
      <header className="flex h-12 shrink-0 items-center gap-3 border-b border-border px-3">
        <span data-testid="run-state" data-state={state.running ? "running" : "settled"} hidden />
        <div className="flex min-w-0 flex-col">
          <h1 className="truncate text-sm font-medium" data-testid="session-title">
            {title}
            {detail?.ephemeral ? (
              <Badge variant="outline" className="ml-2 align-middle text-[10px]" data-testid="ephemeral-badge">
                临时
              </Badge>
            ) : null}
          </h1>
          <p className="truncate text-[11px] text-muted-foreground" data-testid="session-meta">
            {project.data?.project.name ?? "项目"}
            {" · "}
            {sessionKindLabel(detail?.kind)}
            {" · "}
            {effectiveModel}
            {showLabel && detail ? ` · ${detail.label}` : ""}
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
          <CompactContextButton sessionId={sessionId} agentAlive={agentAlive} />
          <Button
            size="sm"
            variant="ghost"
            data-testid="session-logs-open"
            title="agent 日志（排障用）"
            onClick={() => setLogsOpen(true)}
          >
            <ScrollText />
            日志
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setBranchesOpen(true)}>
            <GitBranch />
            分支
          </Button>
          {/* The merge half of "fork, run in parallel, pick one" (FR-2.6).
              It lives in the branch's own header, not only in a hover menu on a
              sidebar row: the acceptance pass opened the main session's menu,
              saw 对比 and nothing else, and concluded adopt did not exist. */}
          {detail?.kind === "branch" && detail.parent_id ? (
            <Button
              size="sm"
              variant="outline"
              data-testid="session-adopt-open"
              title="把这个分支的提交和改动带回来源会话"
              onClick={() => setAdoptOpen(true)}
            >
              <GitMerge />
              采纳到…
            </Button>
          ) : null}
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
          {/* Named so a test can assert the thing the user actually cares about:
              when a side folds, the transcript column gets the width back. */}
          <div data-testid="transcript-column" className="flex h-full min-h-0 w-full flex-col">
            <SessionStream
              sessionId={sessionId}
              sessionModel={detail?.model_ref}
              resolveForkEntry={(turn) =>
                turn.entryId ?? matchForkEntry(turn, forkPoints.data?.points ?? [])
              }
              onFork={(entryId) => {
                const point = forkPoints.data?.points.find((item) => item.entry_id === entryId);
                setForkTarget({ entryId, preview: point?.preview });
              }}
            />
            {hasModel === false || modelRefusal ? (
              <div className="flex flex-col gap-2 px-3 pt-3" data-testid="no-model-slot">
                <NoModelBar error={modelRefusal ?? undefined} />
                {/* The composer's half of the first-run step: what is missing,
                    the way to settings, and (for an admin) the one-click shared
                    provider. It disappears when the account becomes usable. */}
                <FirstRunSetup />
              </div>
            ) : null}
            <Composer
              sessionId={sessionId}
              running={state.running}
              queue={state.queue}
              modelRef={detail?.model_ref}
              thinkingLevel={detail?.thinking_level}
              models={models}
              skills={skills.data?.skills ?? []}
              skillsLoading={skills.isPending}
              sessionSkills={sessionSkills.data?.skills ?? []}
            />
          </div>
        </ResizablePanel>

        <ResizableHandle withHandle />

        <ResizablePanel
          defaultSize="56"
          minSize="30"
          // Folded, the panel is exactly the rail; that is what gives the width
          // back to the transcript. Dragging the handle below the minimum folds it
          // too, and the state follows so the rail is what you see.
          collapsible
          collapsedSize="44px"
          panelRef={panelRef}
          onResize={() => {
            const isCollapsed = panelRef.current?.isCollapsed() ?? false;
            setPanelsCollapsed((current) => (current === isCollapsed ? current : isCollapsed));
          }}
          className="min-h-0"
        >
          {detail && !detail.worktree_id ? (
            <NoWorktreeState
              projectId={projectId}
              projectName={project.data?.project.name}
              repoUrl={project.data?.project.repo_url}
            />
          ) : (
            <WorkspacePanels
              sessionId={sessionId}
              projectId={projectId}
              followText={followText}
              collapsed={panelsCollapsed}
              onCollapsedChange={setPanelsCollapsed}
            />
          )}
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

      <SessionLogsSheet sessionId={sessionId} open={logsOpen} onOpenChange={setLogsOpen} />
      <SidecarDock sessionId={sessionId} projectId={projectId} open={sidecarOpen} onOpenChange={setSidecarOpen} />

      {detail?.kind === "branch" ? (
        <AdoptDialog branchId={sessionId} open={adoptOpen} onOpenChange={setAdoptOpen} />
      ) : null}

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
 *
 * The session row is read *before* anything that assumes a session exists: an
 * unknown id must not mount the event stream, the composer or the panels.
 */
export function Workbench({ sessionId, projectId }: { sessionId: string; projectId: string }) {
  const session = useSession(sessionId);

  if (session.error instanceof ApiError && session.error.status === 404) {
    return <SessionNotFound sessionId={sessionId} projectId={projectId} />;
  }
  // First paint only. A non-404 failure still renders the workbench, which has
  // its own error states, rather than a skeleton that never resolves.
  if (!session.data && !session.error) return <WorkbenchSkeleton />;

  return (
    <SessionEventsProvider sessionId={sessionId}>
      <WorkbenchBody sessionId={sessionId} projectId={projectId} session={session} />
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
