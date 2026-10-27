"use client";

import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Bot,
  ChevronRight,
  GitFork,
  MessageSquare,
  Scissors,
  User,
  Wrench,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Item, ItemContent, ItemDescription, ItemMedia, ItemTitle } from "@/components/ui/item";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api/client";
import { formatRelative, truncate } from "@/lib/session/format";
import type { ForkPoint, TreeEntry } from "@/lib/types";
import { cn } from "@/lib/utils";

import { ForkDialog } from "./fork-dialog";

function entryIcon(node: TreeEntry): ReactNode {
  if (node.type === "tool" || node.type === "tool_call") return <Wrench />;
  if (node.type === "compaction") return <Scissors />;
  if (node.role === "user") return <User />;
  if (node.role === "assistant") return <Bot />;
  return <MessageSquare />;
}

function EntryRow({
  node,
  leafId,
  className,
}: {
  node: TreeEntry;
  leafId?: string | null;
  className?: string;
}) {
  const title = node.label || node.preview || node.entry_id;

  return (
    <Item
      data-testid="branch-row"
      size="xs"
      variant="muted"
      className={cn(
        "min-w-0 flex-1 py-1.5",
        leafId === node.entry_id && "ring-1 ring-ring/40",
        className,
      )}
    >
      <ItemMedia variant="icon">
        {entryIcon(node)}
      </ItemMedia>
      <ItemContent className="min-w-0 gap-0">
        <ItemTitle className="max-w-full truncate text-xs">{truncate(title, 96)}</ItemTitle>
        <ItemDescription className="text-[11px]">
          {formatRelative(node.ts)} · {node.type || node.role || "entry"}
        </ItemDescription>
      </ItemContent>
      {leafId === node.entry_id ? (
        <Badge className="text-[10px]">当前</Badge>
      ) : node.role ? (
        <Badge variant="outline" className="text-[10px]">
          {node.role}
        </Badge>
      ) : null}
    </Item>
  );
}

function TreeNodes({
  nodes,
  depth,
  leafId,
}: {
  nodes: TreeEntry[];
  depth: number;
  leafId?: string | null;
}) {
  return (
    <ul className={cn("flex min-w-0 flex-col gap-1", depth > 0 && "ml-2 border-l border-border pl-2")}>
      {nodes.map((node) => (
        <TreeNodeRow key={node.entry_id} node={node} depth={depth} leafId={leafId} />
      ))}
    </ul>
  );
}

function TreeNodeRow({
  node,
  depth,
  leafId,
}: {
  node: TreeEntry;
  depth: number;
  leafId?: string | null;
}) {
  const children = node.children ?? [];
  if (children.length === 0) {
    return (
      <li className="flex min-w-0">
        <EntryRow node={node} leafId={leafId} />
      </li>
    );
  }

  return (
    <li className="min-w-0">
      <Collapsible defaultOpen={depth === 0}>
        <div className="flex min-w-0 items-center gap-1">
          <CollapsibleTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                className="group shrink-0"
                aria-label="展开子条目"
              />
            }
          >
            <ChevronRight className="transition-transform group-data-[panel-open]:rotate-90" />
          </CollapsibleTrigger>
          <EntryRow node={node} leafId={leafId} />
        </div>
        <CollapsibleContent>
          <div className="mt-1">
            <TreeNodes nodes={children} depth={depth + 1} leafId={leafId} />
          </div>
        </CollapsibleContent>
      </Collapsible>
    </li>
  );
}

/**
 * Branch view for one session: the Pi session tree plus the user messages that
 * can serve as fork origins. Pi only forks at user messages, so only those are
 * offered (PRD FR-2.1).
 */
export function BranchTree({
  sessionId,
  projectId,
  leafId,
}: {
  sessionId: string;
  projectId: string;
  leafId?: string | null;
}) {
  const [target, setTarget] = useState<ForkPoint | null>(null);

  const tree = useQuery({
    queryKey: ["session-tree", sessionId],
    queryFn: () => api.sessions.tree(sessionId),
    staleTime: 10_000,
  });

  const forkPoints = useQuery({
    queryKey: ["session-fork-points", sessionId],
    queryFn: () => api.sessions.forkPoints(sessionId),
    staleTime: 10_000,
  });

  return (
    <div className="flex min-h-0 flex-col gap-4">
      <section className="flex flex-col gap-2">
        <h3 className="text-xs font-medium text-muted-foreground">会话树</h3>
        {tree.isPending ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-5/6" />
            <Skeleton className="h-8 w-4/6" />
          </div>
        ) : tree.isError ? (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            会话树加载失败
            <Button size="xs" variant="outline" onClick={() => void tree.refetch()}>
              重试
            </Button>
          </div>
        ) : (tree.data?.tree ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">还没有可展示的会话条目。</p>
        ) : (
          <TreeNodes nodes={tree.data?.tree ?? []} depth={0} leafId={leafId ?? tree.data?.leaf_id} />
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h3 className="text-xs font-medium text-muted-foreground">可分叉的用户消息</h3>
        {forkPoints.isPending ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-5/6" />
          </div>
        ) : forkPoints.isError ? (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            分叉点加载失败
            <Button size="xs" variant="outline" onClick={() => void forkPoints.refetch()}>
              重试
            </Button>
          </div>
        ) : (forkPoints.data?.points ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">
            还没有可分叉的消息。发送一条消息后，就能从它这里开出平行分支。
          </p>
        ) : (
          (forkPoints.data?.points ?? []).map((point) => (
            <Item key={point.entry_id} size="xs" variant="outline">
              <ItemContent className="min-w-0 gap-0.5">
                <ItemDescription className="line-clamp-2 text-xs">
                  {truncate(point.preview, 160)}
                </ItemDescription>
                <ItemTitle className="text-[11px] font-normal text-muted-foreground">
                  {formatRelative(point.ts)}
                </ItemTitle>
              </ItemContent>
              <Button size="xs" variant="secondary" onClick={() => setTarget(point)}>
                <GitFork />
                从这里分叉
              </Button>
            </Item>
          ))
        )}
      </section>

      <ForkDialog
        open={target !== null}
        onOpenChange={(open) => {
          if (!open) setTarget(null);
        }}
        sessionId={sessionId}
        projectId={projectId}
        entryId={target?.entry_id ?? null}
        preview={target?.preview}
      />
    </div>
  );
}
