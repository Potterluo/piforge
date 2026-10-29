"use client";

import * as React from "react";
import { MinusIcon, PlusIcon, Undo2Icon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Item, ItemActions, ItemContent, ItemGroup, ItemTitle } from "@/components/ui/item";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { GitChange } from "@/lib/types";
import { cn } from "@/lib/utils";
import { changeLetter, type ChangeRowModel, type ChangeState } from "@/components/workspace/git-data";

const LETTER_CLASS: Record<string, string> = {
  M: "text-amber-600 dark:text-amber-400",
  A: "text-emerald-600 dark:text-emerald-400",
  D: "text-red-600 dark:text-red-400",
  U: "text-muted-foreground",
  "!": "text-destructive",
};

/**
 * The one label a row carries. A path is in exactly one state, so it gets
 * exactly one badge — showing 部分暂存 on a fully staged file was the visible
 * symptom of the `.`-versus-space comparison bug.
 */
const STATE_LABEL: Record<ChangeState, string> = {
  staged: "已暂存",
  unstaged: "未暂存",
  partial: "部分暂存",
  untracked: "未跟踪",
};

const STATE_HINT: Record<ChangeState, string> = {
  staged: "在索引里，下一次提交会包含它",
  unstaged: "工作区有改动，还没有暂存",
  partial: "一部分在索引里，一部分还没有暂存",
  untracked: "git 还没有跟踪这个文件",
};

const STATE_CLASS: Record<ChangeState, string> = {
  staged: "border-emerald-600/40 text-emerald-700 dark:text-emerald-400",
  unstaged: "border-amber-600/40 text-amber-700 dark:text-amber-400",
  partial: "border-amber-600/40 text-amber-700 dark:text-amber-400",
  untracked: "text-muted-foreground",
};

function StateBadge({ state }: { state: ChangeState }) {
  return (
    <Badge
      variant="outline"
      data-testid="git-state"
      data-state={state}
      title={STATE_HINT[state]}
      className={cn("shrink-0 px-1 py-0 text-[10px] font-normal", STATE_CLASS[state])}
    >
      {STATE_LABEL[state]}
    </Badge>
  );
}

function ChangeRow({
  row,
  selected,
  onSelect,
  onStage,
  onUnstage,
  onDiscard,
  busy,
}: {
  row: ChangeRowModel;
  selected: boolean;
  onSelect: (path: string) => void;
  onStage: (path: string) => void;
  onUnstage: (path: string) => void;
  /** Restores the file, or deletes it when it is new. The one action that cannot be undone. */
  onDiscard: (path: string) => void;
  busy: boolean;
}) {
  const change: GitChange = row.change;
  const letter = changeLetter(change);

  return (
    <Item
      size="xs"
      data-testid="git-change"
      data-path={row.path}
      data-state={row.state}
      className={cn(
        "cursor-pointer rounded-sm py-0.5 hover:bg-accent/60",
        selected && "bg-accent text-accent-foreground",
      )}
      onClick={() => onSelect(row.path)}
    >
      <span
        className={cn("w-3 shrink-0 font-mono text-[10px] leading-none", LETTER_CLASS[letter])}
        title={`git: ${change.index}${change.worktree}`}
      >
        {letter}
      </span>
      <ItemContent className="min-w-0 gap-0">
        <ItemTitle className="max-w-full truncate font-mono text-[11px] font-normal" title={row.path}>
          {row.path}
        </ItemTitle>
      </ItemContent>
      <StateBadge state={row.state} />
      <ItemActions className="shrink-0">
        {row.canStage ? (
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="暂存"
                  disabled={busy}
                  onClick={(event: React.MouseEvent) => {
                    event.stopPropagation();
                    onStage(row.path);
                  }}
                />
              }
            >
              <PlusIcon />
            </TooltipTrigger>
            <TooltipContent>{row.state === "partial" ? "暂存剩余改动" : "暂存这个文件"}</TooltipContent>
          </Tooltip>
        ) : null}
        {row.canUnstage ? (
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="取消暂存"
                  disabled={busy}
                  onClick={(event: React.MouseEvent) => {
                    event.stopPropagation();
                    onUnstage(row.path);
                  }}
                />
              }
            >
              <MinusIcon />
            </TooltipTrigger>
            <TooltipContent>取消暂存</TooltipContent>
          </Tooltip>
        ) : null}
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label="丢弃改动"
                data-testid="git-discard"
                data-path={row.path}
                disabled={busy}
                onClick={(event: React.MouseEvent) => {
                  event.stopPropagation();
                  onDiscard(row.path);
                }}
              />
            }
          >
            <Undo2Icon />
          </TooltipTrigger>
          <TooltipContent side="left">丢弃 {row.path} 的改动</TooltipContent>
        </Tooltip>
      </ItemActions>
    </Item>
  );
}

export interface ChangeListProps {
  rows: ChangeRowModel[];
  selectedPath: string | null;
  onSelect: (path: string) => void;
  onStage: (paths: string[]) => void;
  onUnstage: (paths: string[]) => void;
  /** Discards one file's changes, after the caller has confirmed it. */
  onDiscard: (path: string) => void;
  busy: boolean;
}

/**
 * The 变更 list: one row per path, whatever state it is in (PRD §10.4.4).
 *
 * It used to be three collapsible groups, which meant a partly staged file was
 * drawn twice; a fully staged one was drawn twice too, because of the `.`
 * comparison bug, and the panel looked like it had staged everything twice.
 */
export function ChangeList({
  rows,
  selectedPath,
  onSelect,
  onStage,
  onUnstage,
  onDiscard,
  busy,
}: ChangeListProps) {
  const stagable = React.useMemo(
    () => rows.filter((row) => row.canStage).map((row) => row.path),
    [rows],
  );

  if (rows.length === 0) {
    return <p className="px-2 py-2 text-[11px] text-muted-foreground">工作区干净，没有改动。</p>;
  }

  return (
    <div className="min-w-0">
      <ItemGroup className="gap-0" data-testid="git-change-list">
        {rows.map((row) => (
          <ChangeRow
            key={row.path}
            row={row}
            selected={selectedPath === row.path}
            onSelect={onSelect}
            onStage={(path) => onStage([path])}
            onUnstage={(path) => onUnstage([path])}
            onDiscard={onDiscard}
            busy={busy}
          />
        ))}
      </ItemGroup>
      {stagable.length > 0 ? (
        <div className="px-2 py-1">
          <Button
            variant="outline"
            size="xs"
            data-testid="git-stage-all"
            disabled={busy}
            onClick={() => onStage(stagable)}
          >
            全部暂存
          </Button>
        </div>
      ) : null}
    </div>
  );
}
