"use client";

import Link from "next/link";
import { useState } from "react";
import { ArrowUpRight, Clock, FolderGit2, Plus } from "lucide-react";

import { NewSessionDialog } from "@/components/shell/new-session-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Item, ItemActions, ItemContent, ItemDescription, ItemMedia, ItemTitle } from "@/components/ui/item";
import { Skeleton } from "@/components/ui/skeleton";
import {
  formatRelative,
  sandboxLabel,
  sessionKindLabel,
  sessionStatusLabel,
  sessionStatusTone,
} from "@/lib/session/format";
import { useProject, useProjectSessions } from "@/lib/session/queries";
import { cn } from "@/lib/utils";
import type { SessionStatus } from "@/lib/types";

function StatusDot({ status }: { status: SessionStatus }) {
  const tone = sessionStatusTone(status);
  return (
    <span
      aria-hidden="true"
      className={cn(
        "size-1.5 shrink-0 rounded-full",
        tone === "running" && "animate-pulse bg-primary",
        tone === "idle" && "bg-muted-foreground/50",
        tone === "paused" && "border border-muted-foreground bg-transparent",
        tone === "failed" && "bg-destructive",
      )}
    />
  );
}

export function ProjectOverviewSkeleton() {
  return (
    <div className="flex flex-col gap-4 p-4">
      <Skeleton className="h-6 w-48" />
      <Skeleton className="h-4 w-72" />
      <div className="mt-2 flex flex-col gap-2">
        <Skeleton className="h-14 w-full" />
        <Skeleton className="h-14 w-full" />
        <Skeleton className="h-14 w-full" />
      </div>
    </div>
  );
}

export function ProjectOverview({ projectId }: { projectId: string }) {
  const project = useProject(projectId);
  const sessions = useProjectSessions(projectId);
  const [dialogOpen, setDialogOpen] = useState(false);

  const data = project.data?.project;

  if (project.isError) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6">
        <p className="text-sm text-muted-foreground">项目加载失败：{project.error.message}</p>
        <Button variant="outline" onClick={() => void project.refetch()}>
          重试
        </Button>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <header className="flex flex-col gap-3 border-b border-border p-4">
        <div className="flex flex-wrap items-start gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <div className="flex items-center gap-2">
              <FolderGit2 className="size-4 shrink-0 text-muted-foreground" />
              <h1 className="truncate text-base font-medium">
                {data?.name ?? <Skeleton className="h-5 w-40" />}
              </h1>
            </div>
            <p className="truncate font-mono text-xs text-muted-foreground">
              {data?.repo_url || "空白工作区"}
            </p>
          </div>

          <div className="ml-auto flex flex-wrap items-center gap-2">
            {data ? (
              <>
                <Badge variant="outline">{data.default_branch}</Badge>
                <Badge variant="secondary">沙箱 · {sandboxLabel(data.sandbox_state)}</Badge>
                <Badge variant="outline">{data.session_count} 个会话</Badge>
              </>
            ) : null}
            <Button onClick={() => setDialogOpen(true)} size="sm" data-testid="new-session-button">
              <Plus />
              新会话
            </Button>
          </div>
        </div>
      </header>

      <div className="flex flex-col gap-3 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-medium">会话</h2>
          {data?.last_active_at ? (
            <span className="flex items-center gap-1 text-xs text-muted-foreground">
              <Clock className="size-3.5" />
              最近活动 {formatRelative(data.last_active_at)}
            </span>
          ) : null}
        </div>

        {sessions.isPending ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
          </div>
        ) : sessions.isError ? (
          <Card>
            <CardHeader>
              <CardTitle>会话加载失败</CardTitle>
              <CardDescription>{sessions.error.message}</CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="outline" size="sm" onClick={() => void sessions.refetch()}>
                重试
              </Button>
            </CardContent>
          </Card>
        ) : (sessions.data?.sessions ?? []).length === 0 ? (
          <Empty className="border border-dashed border-border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <FolderGit2 />
              </EmptyMedia>
              <EmptyTitle>还没有会话</EmptyTitle>
              <EmptyDescription>
                创建一个会话，agent 会在独立 worktree 里开始工作。可以随时分叉，不影响主线。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" data-testid="new-session-button" onClick={() => setDialogOpen(true)}>
                <Plus />
                新会话
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <div className="flex flex-col gap-2">
            {(sessions.data?.sessions ?? []).map((session) => (
              <Link
                key={session.id}
                href={`/p/${projectId}/s/${session.id}`}
                data-testid="session-link"
                className="rounded-lg outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
              >
                <Item variant="outline" className="hover:bg-muted/50">
                  <ItemMedia>
                    <StatusDot status={session.status} />
                  </ItemMedia>
                  <ItemContent className="min-w-0 gap-0.5">
                    <ItemTitle className="max-w-full truncate">
                      {session.title || "未命名会话"}
                    </ItemTitle>
                    <ItemDescription className="text-xs">
                      {sessionKindLabel(session.kind)}
                      {session.label ? ` · ${session.label}` : ""} ·{" "}
                      {sessionStatusLabel(session.status)} · {formatRelative(session.last_active_at)}
                    </ItemDescription>
                  </ItemContent>
                  <ItemActions>
                    <ArrowUpRight className="size-4 text-muted-foreground" />
                  </ItemActions>
                </Item>
              </Link>
            ))}
          </div>
        )}
      </div>

      <NewSessionDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        projectId={projectId}
      />
    </div>
  );
}
