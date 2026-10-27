"use client";

import * as React from "react";
import { HistoryIcon, RefreshCwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { GitCommit } from "@/lib/types";
import { GitCommitSheet } from "@/components/workspace/git-commit-sheet";
import { useGitData } from "@/components/workspace/git-data";
import { GitGraph } from "@/components/workspace/git-graph";
import { PanelError, PanelSkeleton } from "@/components/workspace/panel-state";

/**
 * Git tab: the history list rendered as a DAG.
 *
 * The list is the 200 newest commits from `api.git.log`; the graph virtualises
 * whatever the backend returns, so raising the limit later is a one-number
 * change rather than a rewrite.
 */
export function GitHistoryPanel() {
  const git = useGitData();
  const [selected, setSelected] = React.useState<GitCommit | null>(null);

  const commits = React.useMemo(() => git.log.data?.commits ?? [], [git.log.data]);
  const head = git.log.data?.head;

  const handleSelectParent = React.useCallback(
    (hash: string) => {
      const parent = commits.find((commit) => commit.hash === hash);
      if (parent) setSelected(parent);
    },
    [commits],
  );

  return (
    <div data-testid="git-history" className="flex h-full min-h-0 min-w-0 flex-col">
      <header className="flex shrink-0 items-center gap-1.5 border-b px-2 py-1.5">
        <HistoryIcon className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate text-[11px]">提交历史</span>
        <Badge variant="outline" className="shrink-0 font-mono text-[10px]">
          {commits.length}
        </Badge>
        {head ? (
          <Badge variant="secondary" className="max-w-24 shrink-0 truncate font-mono text-[10px]">
            HEAD {head.slice(0, 7)}
          </Badge>
        ) : null}
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label="刷新提交历史"
                className="ms-auto"
                onClick={git.log.reload}
                disabled={git.log.loading}
              />
            }
          >
            <RefreshCwIcon className={git.log.loading ? "animate-spin" : undefined} />
          </TooltipTrigger>
          <TooltipContent side="bottom">刷新</TooltipContent>
        </Tooltip>
      </header>

      {git.log.loading && commits.length === 0 ? <PanelSkeleton rows={12} /> : null}

      {git.log.error ? <PanelError message={git.log.error} onRetry={git.log.reload} /> : null}

      {!git.log.loading && !git.log.error && commits.length === 0 ? (
        <Empty className="flex-1">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <HistoryIcon />
            </EmptyMedia>
            <EmptyTitle>还没有提交</EmptyTitle>
            <EmptyDescription>这个分支上还没有 commit，或者 log 请求没有返回数据。</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : null}

      {commits.length > 0 ? (
        <GitGraph
          commits={commits}
          activeHash={selected?.hash ?? null}
          onSelect={setSelected}
        />
      ) : null}

      <GitCommitSheet
        commit={selected}
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
        onSelectParent={handleSelectParent}
      />
    </div>
  );
}
