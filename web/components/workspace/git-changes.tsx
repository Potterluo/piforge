"use client";

import * as React from "react";
import { ChevronRightIcon, MinusIcon, PlusIcon, Undo2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Item,
  ItemActions,
  ItemContent,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { GitChange } from "@/lib/types";
import { cn } from "@/lib/utils";
import { changeLetter, groupChanges } from "@/components/workspace/git-data";

const LETTER_CLASS: Record<string, string> = {
  M: "text-amber-600 dark:text-amber-400",
  A: "text-emerald-600 dark:text-emerald-400",
  D: "text-red-600 dark:text-red-400",
  U: "text-muted-foreground",
  "!": "text-destructive",
};

function ChangeRow({
  change,
  action,
  onAction,
  onDiscard,
  selected,
  onSelect,
  busy,
  partial = false,
}: {
  change: GitChange;
  action: "stage" | "unstage";
  onAction: (path: string) => void;
  /** Restores the file, or deletes it when it is new. The one action that cannot be undone. */
  onDiscard: (path: string) => void;
  selected: boolean;
  onSelect: (path: string) => void;
  busy: boolean;
  /** True when this path is partly staged and partly not. */
  partial?: boolean;
}) {
  const letter = changeLetter(change);

  return (
    <Item
      size="xs"
      data-testid="git-change"
      data-path={change.path}
      className={cn(
        "cursor-pointer rounded-sm py-0.5 hover:bg-accent/60",
        selected && "bg-accent text-accent-foreground",
      )}
      onClick={() => onSelect(change.path)}
    >
      <span
        className={cn("w-3 shrink-0 font-mono text-[10px] leading-none", LETTER_CLASS[letter])}
        title={`git: ${change.index}${change.worktree}`}
      >
        {letter}
      </span>
      <ItemContent className="min-w-0 gap-0">
        <ItemTitle
          className="max-w-full truncate font-mono text-[11px] font-normal"
          title={change.path}
        >
          {change.path}
        </ItemTitle>
        {partial ? (
          // A file can legitimately appear in both lists: part of it is staged and part
          // is not. Saying so is the difference between a bug report and an explanation.
          <span
            data-testid="git-partial"
            className="text-[10px] text-muted-foreground"
            title="这个文件已暂存一部分，还有一部分没暂存"
          >
            部分暂存
          </span>
        ) : null}
      </ItemContent>
      <ItemActions className="shrink-0">
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={action === "stage" ? "暂存" : "取消暂存"}
                disabled={busy}
                onClick={(event: React.MouseEvent) => {
                  event.stopPropagation();
                  onAction(change.path);
                }}
              />
            }
          >
            {action === "stage" ? <PlusIcon /> : <MinusIcon />}
          </TooltipTrigger>
          <TooltipContent>{action === "stage" ? "暂存这个文件" : "取消暂存"}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label="丢弃改动"
                data-testid="git-discard"
                data-path={change.path}
                disabled={busy}
                onClick={(event: React.MouseEvent) => {
                  event.stopPropagation();
                  onDiscard(change.path);
                }}
              />
            }
          >
            <Undo2Icon />
          </TooltipTrigger>
          <TooltipContent side="left">{action === "stage" ? "暂存此文件" : "取消暂存"}</TooltipContent>
        </Tooltip>
      </ItemActions>
    </Item>
  );
}

function Group({
  title,
  changes,
  action,
  selectedPath,
  onSelect,
  onAction,
  onDiscard,
  busy,
  partial,
}: {
  title: string;
  changes: GitChange[];
  action: "stage" | "unstage";
  selectedPath: string | null;
  onSelect: (path: string) => void;
  onAction: (path: string) => void;
  onDiscard: (path: string) => void;
  busy: boolean;
  partial: Set<string>;
}) {
  const [open, setOpen] = React.useState(true);
  if (changes.length === 0) return null;

  return (
    <Collapsible open={open} onOpenChange={setOpen} className="min-w-0">
      <CollapsibleTrigger className="flex w-full items-center gap-1 px-2 py-1 text-[10px] font-medium text-muted-foreground hover:text-foreground">
        <ChevronRightIcon className={cn("size-3 transition-transform", open && "rotate-90")} />
        {title}
        <span className="font-mono">({changes.length})</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <ItemGroup className="gap-0">
          {changes.map((change) => (
            <ChangeRow
              key={`${action}:${change.path}:${change.index}${change.worktree}`}
              change={change}
              action={action}
              onAction={onAction}
              onDiscard={onDiscard}
              partial={partial.has(change.path)}
              selected={selectedPath === change.path}
              onSelect={onSelect}
              busy={busy}
            />
          ))}
        </ItemGroup>
      </CollapsibleContent>
    </Collapsible>
  );
}

export interface ChangeGroupsProps {
  changes: GitChange[];
  selectedPath: string | null;
  onSelect: (path: string) => void;
  onStage: (paths: string[]) => void;
  onUnstage: (paths: string[]) => void;
  /** Discards one file's changes, after the caller has confirmed it. */
  onDiscard: (path: string) => void;
  busy: boolean;
}

/** Staged / unstaged / untracked groups (PRD §10.4.4). */
export function ChangeGroups({
  changes,
  selectedPath,
  onSelect,
  onStage,
  onUnstage,
  onDiscard,
  busy,
}: ChangeGroupsProps) {
  const groups = React.useMemo(() => groupChanges(changes), [changes]);
  // Paths that appear in both lists: partly staged, partly not.
  const partial = React.useMemo(() => {
    const staged = new Set(groups.staged.map((change) => change.path));
    return new Set(groups.unstaged.filter((change) => staged.has(change.path)).map((c) => c.path));
  }, [groups]);
  const unstagedPaths = React.useMemo(
    () => [...groups.unstaged, ...groups.untracked].map((change) => change.path),
    [groups],
  );

  if (changes.length === 0) {
    return <p className="px-2 py-2 text-[11px] text-muted-foreground">工作区干净，没有改动。</p>;
  }

  return (
    <div className="min-w-0">
      <Group
        title="已暂存"
        changes={groups.staged}
        action="unstage"
        selectedPath={selectedPath}
        onSelect={onSelect}
        onAction={(path) => onUnstage([path])}
        onDiscard={onDiscard}
        partial={partial}
        busy={busy}
      />
      <Group
        title="未暂存"
        changes={groups.unstaged}
        action="stage"
        selectedPath={selectedPath}
        onSelect={onSelect}
        onAction={(path) => onStage([path])}
        onDiscard={onDiscard}
        partial={partial}
        busy={busy}
      />
      <Group
        title="未跟踪"
        changes={groups.untracked}
        action="stage"
        selectedPath={selectedPath}
        onSelect={onSelect}
        onAction={(path) => onStage([path])}
        onDiscard={onDiscard}
        partial={partial}
        busy={busy}
      />
      {unstagedPaths.length > 0 ? (
        <div className="px-2 py-1">
          <Button
            variant="outline"
            size="xs"
            data-testid="git-stage-all"
            disabled={busy}
            onClick={() => onStage(unstagedPaths)}
          >
            全部暂存
          </Button>
        </div>
      ) : null}
    </div>
  );
}
