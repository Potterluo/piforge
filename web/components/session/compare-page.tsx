"use client";

import * as React from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { GitCompare } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { formatRelative } from "@/lib/session/format";
import type { CompareRow } from "@/lib/types";

/**
 * Compare two or three branches (PRD FR-2.5).
 *
 * This is the screen that makes "try a few approaches and pick one" true: the numbers
 * are per attempt, and every column can be opened or adopted from here. It deliberately
 * has no "tests" column — this deployment does not run a suite, and a column that always
 * read "—" would be decoration pretending to be evidence.
 */
export function ComparePage({ projectId, ids }: { projectId: string; ids: string[] }) {
  const unique = new Set(ids);
  const duplicate = unique.size !== ids.length;
  const invalid = ids.length < 2 || duplicate || ids.length > 3;

  const compare = useQuery({
    queryKey: ["compare", projectId, ids.join(",")],
    queryFn: () => api.sessions.compare(ids),
    enabled: !invalid,
  });

  if (invalid) {
    // A hand-edited URL (or the old single-session action) can ask for the same
    // session twice. Fetching it would render two identical columns and call it
    // "对比 2 个尝试", which is not a comparison — so it is rejected before the
    // request, with the reason and a way back.
    const title = duplicate
      ? "不能拿同一个会话和自己对比"
      : ids.length < 2
        ? "选两个会话再对比"
        : "一次最多对比三个会话";
    const description = duplicate
      ? "对比需要两个不同的会话。回到会话菜单，选择另一个会话作为对比对象。"
      : ids.length < 2
        ? "在会话的 ⋯ 菜单里选「对比」，然后选择同项目里的另一个会话。"
        : "请从会话菜单重新选择，最多选三个不同的会话。";
    return (
      <div className="p-6" data-testid="compare-page">
        <Empty data-testid="compare-invalid">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <GitCompare />
            </EmptyMedia>
            <EmptyTitle>{title}</EmptyTitle>
            <EmptyDescription>{description}</EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button size="sm" variant="outline" render={<Link href={`/p/${projectId}`} />}>
              返回项目
            </Button>
          </EmptyContent>
        </Empty>
      </div>
    );
  }

  const rows = compare.data?.rows ?? [];

  return (
    <div className="flex flex-col gap-4 p-6" data-testid="compare-page">
      <div className="flex items-center gap-2">
        <GitCompare className="size-4" />
        <h1 className="text-lg font-medium">对比 {rows.length || ids.length} 个尝试</h1>
        <Button size="sm" variant="ghost" render={<Link href={`/p/${projectId}`} />}>
          返回项目
        </Button>
      </div>

      {compare.isLoading ? (
        <Skeleton className="h-40 w-full" />
      ) : compare.isError ? (
        <p className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
          读取失败：{humanizeError(compare.error)}
        </p>
      ) : (
        <div className="grid gap-3 md:grid-cols-3" data-testid="compare-grid">
          {rows.map((row) => (
            <CompareColumn key={row.session_id} projectId={projectId} row={row} />
          ))}
        </div>
      )}
    </div>
  );
}

function CompareColumn({ projectId, row }: { projectId: string; row: CompareRow }) {
  return (
    <div
      className="flex flex-col gap-2 rounded-md border border-border p-3"
      data-testid="compare-column"
      data-session={row.session_id}
    >
      <div className="flex items-start justify-between gap-2">
        <Link
          href={`/p/${projectId}/s/${row.session_id}`}
          className="truncate text-sm font-medium hover:underline"
        >
          {row.title || "未命名"}
        </Link>
        <Badge variant="outline" className="text-[10px]">
          {row.kind === "branch" ? "分支" : row.kind === "sidecar" ? "旁路" : "主会话"}
        </Badge>
      </div>

      <dl className="grid grid-cols-2 gap-x-2 gap-y-1 text-[11px]">
        <Metric label="改动" value={`${row.files_changed} 个文件`} hint={row.diff_error} />
        <Metric label="增删" value={`+${row.additions} / -${row.deletions}`} />
        <Metric label="轮次" value={`${row.user_messages}`} />
        <Metric label="工具调用" value={`${row.tool_calls}`} />
        <Metric label="输入 token" value={row.tokens_in.toLocaleString()} />
        <Metric label="输出 token" value={row.tokens_out.toLocaleString()} />
        <Metric label="最近活动" value={formatRelative(row.last_active_at)} />
        <Metric label="模型" value={row.model_ref || "跟随默认"} />
      </dl>

      <div className="mt-auto flex items-center gap-2 pt-1">
        <Button size="xs" variant="outline" render={<Link href={`/p/${projectId}/s/${row.session_id}`} />}>
          打开
        </Button>
      </div>
    </div>
  );
}

function Metric({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="flex min-w-0 flex-col">
      <dt className="truncate text-muted-foreground">{label}</dt>
      <dd className="truncate">{value}</dd>
      {hint ? <dd className="truncate text-[10px] text-amber-600">{hint}</dd> : null}
    </div>
  );
}
