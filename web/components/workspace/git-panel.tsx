"use client";

import * as React from "react";
import {
  ArrowDownIcon,
  ArrowUpIcon,
  GitCompareIcon,
  RefreshCwIcon,
  Rows3Icon,
  Columns3Icon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import api from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { toast } from "sonner";
import type { DiffScope } from "@/lib/types";
import { cn } from "@/lib/utils";
import { ChangeList } from "@/components/workspace/git-changes";
import { changeRows, useGitData } from "@/components/workspace/git-data";
import { GitDiffView } from "@/components/workspace/git-diff";
import { PanelError, PanelSkeleton } from "@/components/workspace/panel-state";
import { useAsync } from "@/components/workspace/use-async";

const SCOPES: { value: DiffScope; label: string }[] = [
  { value: "unstaged", label: "未暂存" },
  { value: "staged", label: "已暂存" },
  { value: "branch", label: "分支" },
];

/** Diff body for one file in one scope. */
function DiffSection({
  scope,
  path,
  split,
  onToggleSplit,
}: {
  scope: DiffScope;
  path: string | null;
  split: boolean;
  onToggleSplit: () => void;
}) {
  const git = useGitData();
  const diff = useAsync(
    `diff:${git.sessionId}:${scope}:${path ?? ""}`,
    () => api.git.diff(git.sessionId, scope, path ?? ""),
    { enabled: Boolean(path) },
  );

  const file = diff.data?.diffs[0];

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <div className="flex shrink-0 items-center gap-1.5 border-b px-2 py-1">
        <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">
          {path ?? "选择文件查看差异"}
        </span>
        {file ? (
          <span className="shrink-0 font-mono text-[10px]">
            <span className="text-emerald-600 dark:text-emerald-400">+{file.additions}</span>{" "}
            <span className="text-red-600 dark:text-red-400">-{file.deletions}</span>
          </span>
        ) : null}
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={split ? "切换为统一视图" : "切换为并排视图"}
                onClick={onToggleSplit}
              />
            }
          >
            {split ? <Rows3Icon /> : <Columns3Icon />}
          </TooltipTrigger>
          <TooltipContent side="bottom">{split ? "统一视图" : "并排视图"}</TooltipContent>
        </Tooltip>
      </div>

      <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
        {!path ? (
          <p className="p-3 text-[11px] text-muted-foreground">
            在上方选择一个文件，这里会显示它在此范围内的 diff。
          </p>
        ) : diff.loading && !diff.data ? (
          <PanelSkeleton rows={8} />
        ) : diff.error ? (
          <PanelError message={diff.error} onRetry={diff.reload} />
        ) : file ? (
          file.binary ? (
            <p className="p-3 text-[11px] text-muted-foreground">二进制文件，暂无文本 diff。</p>
          ) : (
            <GitDiffView path={file.path} patch={file.patch} split={split} />
          )
        ) : (
          <p className="p-3 text-[11px] text-muted-foreground">这个文件在当前范围内没有差异。</p>
        )}
      </div>
    </div>
  );
}

/**
 * 变更 tab: working tree status, staging, the commit box, and the diff viewer.
 *
 * Everything mutating goes through `git.refresh()` so the 变更 and Git tabs
 * never disagree about the tree.
 */
export function GitChangesPanel() {
  const git = useGitData();
  const [selectedPath, setSelectedPath] = React.useState<string | null>(null);
  const [scope, setScope] = React.useState<DiffScope>("unstaged");
  const [split, setSplit] = React.useState(false);
  const [message, setMessage] = React.useState("");
  const [amend, setAmend] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [actionError, setActionError] = React.useState<string | null>(null);
  const textareaRef = React.useRef<HTMLTextAreaElement | null>(null);

  const status = git.status.data;
  const changes = React.useMemo(() => status?.changes ?? [], [status]);
  // One row per path; `stagedPaths` is derived from the same model, so the
  // commit button and the list can never disagree about what is in the index.
  const rows = React.useMemo(() => changeRows(changes), [changes]);
  const stagedPaths = React.useMemo(
    () => rows.filter((row) => row.canUnstage).map((row) => row.path),
    [rows],
  );

  const mutate = React.useCallback(
    async (operation: () => Promise<unknown>) => {
      setBusy(true);
      setActionError(null);
      try {
        await operation();
        git.refresh();
      } catch (error) {
        setActionError(humanizeError(error, "git 操作失败"));
      } finally {
        setBusy(false);
      }
    },
    [git],
  );

  const stage = React.useCallback(
    (paths: string[]) => void mutate(() => api.git.stage(git.sessionId, paths)),
    [git.sessionId, mutate],
  );

  const unstage = React.useCallback(
    (paths: string[]) => void mutate(() => api.git.unstage(git.sessionId, paths)),
    [git.sessionId, mutate],
  );

  // Discarding is the one action here that cannot be undone, so it asks first and names
  // the file. A confirmation that only says "are you sure" trains people to click through.
  const discard = React.useCallback(
    (path: string) => {
      const untracked = changes.some((change) => change.path === path && change.worktree === "?");
      toast.warning(untracked ? `删除新文件 ${path}？` : `丢弃 ${path} 的改动？`, {
        description: untracked ? "这个文件还没有被 git 跟踪，删除后无法恢复。" : "会还原到上一次提交的内容，无法撤销。",
        action: {
          label: "丢弃",
          onClick: () => void mutate(() => api.git.discardFile(git.sessionId, path)),
        },
      });
    },
    [changes, git.sessionId, mutate],
  );

  const commit = React.useCallback(() => {
    const trimmed = message.trim();
    if (!trimmed) {
      setActionError("提交信息不能为空");
      textareaRef.current?.focus();
      return;
    }
    void mutate(async () => {
      await api.git.commit(git.sessionId, trimmed, amend ? [] : stagedPaths, amend);
      setMessage("");
      setAmend(false);
    });
  }, [amend, git.sessionId, message, mutate, stagedPaths]);

  // `?.` on the array too: a null `changes` from an older server must not
  // crash the whole panel.
  const dirty = rows.length > 0;
  // The server totals are authoritative; summing the rows is the fallback for a
  // server built before FR-12.4. A change list that says "+7 −1" but whose rows
  // add up to something else is worse than no totals at all.
  const additions = status?.additions ?? rows.reduce((n, row) => n + (row.change.additions ?? 0), 0);
  const deletions = status?.deletions ?? rows.reduce((n, row) => n + (row.change.deletions ?? 0), 0);

  return (
    <div data-testid="git-panel" className="flex h-full min-h-0 min-w-0 flex-col">
      <header className="flex shrink-0 flex-wrap items-center gap-1.5 border-b px-2 py-1.5">
        <Badge
          variant="secondary"
          data-testid="git-status-branch"
          className="max-w-[50%] truncate font-mono text-[10px]"
        >
          {status?.branch ?? "—"}
        </Badge>
        {status ? (
          <span className="flex items-center gap-0.5 font-mono text-[10px] text-muted-foreground">
            <ArrowUpIcon className="size-3" />
            {status.ahead}
            <ArrowDownIcon className="ml-0.5 size-3" />
            {status.behind}
          </span>
        ) : null}
        <span
          className={cn(
            "rounded-full px-1.5 py-0.5 text-[10px]",
            dirty ? "bg-amber-500/10 text-amber-600 dark:text-amber-400" : "bg-muted text-muted-foreground",
          )}
        >
          {dirty ? `${rows.length} 个改动` : "干净"}
        </span>
        {dirty ? (
          <span
            data-testid="git-change-totals"
            className="font-mono text-[10px] tabular-nums text-muted-foreground"
            title="相对 HEAD 的增删行数（含已暂存与未暂存）"
          >
            <span className="text-emerald-600 dark:text-emerald-400">+{additions}</span>{" "}
            <span className="text-red-600 dark:text-red-400">−{deletions}</span>
          </span>
        ) : null}

        <div className="ms-auto flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="刷新 git 状态"
            onClick={git.refresh}
            disabled={git.status.loading}
          >
            <RefreshCwIcon className={git.status.loading ? "animate-spin" : undefined} />
          </Button>
          <Button size="xs" onClick={() => textareaRef.current?.focus()} disabled={busy}>
            提交
          </Button>
          <RemoteActions />
        </div>
      </header>

      {actionError ? (
        <PanelError message={actionError} onRetry={() => setActionError(null)} className="border-b" />
      ) : null}

      <ResizablePanelGroup orientation="vertical" className="min-h-0 flex-1">
        <ResizablePanel defaultSize="58%" minSize={150} className="min-h-0">
          <div className="flex h-full min-h-0 min-w-0 flex-col">
            <div className="min-h-0 flex-1 overflow-auto border-b">
              {git.status.loading && !status ? (
                <PanelSkeleton rows={8} />
              ) : git.status.error ? (
                <PanelError message={git.status.error} onRetry={git.status.reload} />
              ) : (
                <ChangeList
                  rows={rows}
                  selectedPath={selectedPath}
                  onSelect={setSelectedPath}
                  onStage={stage}
                  onUnstage={unstage}
                  onDiscard={discard}
                  busy={busy}
                />
              )}
            </div>

            <div className="shrink-0 space-y-1.5 p-2">
              <Textarea
                ref={textareaRef}
                value={message}
                onChange={(event) => setMessage(event.target.value)}
                placeholder={amend ? "修改上一次提交的信息…" : "提交信息…"}
                aria-label="提交信息"
                data-testid="git-commit-message"
                className="min-h-14 text-xs"
              />
              <div className="flex items-center justify-between gap-2">
                <label className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
                  <Switch size="sm" checked={amend} onCheckedChange={setAmend} aria-label="amend" />
                  amend 上一次
                </label>
                <Button
                  size="xs"
                  data-testid="git-commit-submit"
                  onClick={commit}
                  disabled={busy || (!amend && stagedPaths.length === 0)}
                >
                  {busy ? "处理中…" : amend ? "修改提交" : "提交"}
                </Button>
              </div>
              {!amend && stagedPaths.length === 0 ? (
                <p className="text-[10px] text-muted-foreground">先暂存文件，或勾选 amend。</p>
              ) : null}
            </div>
          </div>
        </ResizablePanel>

        <ResizableHandle withHandle />

        <ResizablePanel defaultSize="42%" minSize={120} className="min-h-0">
          <div className="flex h-full min-h-0 min-w-0 flex-col">
            <div className="flex shrink-0 items-center gap-1 border-b px-2 py-1">
              <GitCompareIcon className="size-3 text-muted-foreground" />
              <Tabs value={scope} onValueChange={(value) => setScope(value as DiffScope)}>
                <TabsList variant="line" className="h-6 gap-0">
                  {SCOPES.map((entry) => (
                    <TabsTrigger key={entry.value} value={entry.value} className="px-1.5 text-[10px]">
                      {entry.label}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
            </div>
            <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
              <DiffSection
                scope={scope}
                path={selectedPath}
                split={split}
                onToggleSplit={() => setSplit((value) => !value)}
              />
            </div>
          </div>
        </ResizablePanel>
      </ResizablePanelGroup>
    </div>
  );
}
/**
 * Push and pull (FR-8.7).
 *
 * The count comes from the remote-tracking ref, so the buttons say what they would do:
 * 推送 3 means three commits would leave this machine. A checkout with no remote gets
 * no buttons at all rather than two that always fail.
 */
function RemoteActions() {
  const git = useGitData();
  const remote = useAsync(`remote:${git.sessionId}:${git.revision}`, () => api.git.remote(git.sessionId));
  const [busy, setBusy] = React.useState<"push" | "pull" | null>(null);
  const [error, setError] = React.useState<string | null>(null);

  if (!remote.data?.remote) return null;

  const act = async (which: "push" | "pull") => {
    setBusy(which);
    setError(null);
    try {
      if (which === "push") {
        await api.git.push(git.sessionId);
        toast.success("已推送");
      } else {
        const result = await api.git.pull(git.sessionId);
        // `changed` is the difference between "pulled" and "pulled something": a
        // fast-forward that brought nothing is not worth a toast.
        if (result.changed) toast.success("已拉取远端改动");
        else toast.info("已经是最新的");
      }
      git.refresh();
      remote.reload();
    } catch (cause) {
      setError(humanizeError(cause, "远端操作失败"));
    } finally {
      setBusy(null);
    }
  };

  const ahead = remote.data?.ahead ?? 0;
  const behind = remote.data?.behind ?? 0;

  return (
    <div className="flex items-center gap-1">
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              size="xs"
              variant="ghost"
              data-testid="git-pull"
              disabled={busy !== null}
              onClick={() => void act("pull")}
            >
              <ArrowDownIcon />
              {behind > 0 ? behind : null}
            </Button>
          }
        />
        <TooltipContent>从远端拉取（仅快进）</TooltipContent>
      </Tooltip>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              size="xs"
              variant={ahead > 0 ? "default" : "ghost"}
              data-testid="git-push"
              disabled={busy !== null || ahead === 0}
              onClick={() => void act("push")}
            >
              <ArrowUpIcon />
              {ahead > 0 ? ahead : null}
            </Button>
          }
        />
        <TooltipContent>{ahead > 0 ? `推送 ${ahead} 个提交` : "没有待推送的提交"}</TooltipContent>
      </Tooltip>
      {error ? (
        <span data-testid="git-remote-error" className="truncate text-[10px] text-destructive">
          {error}
        </span>
      ) : null}
    </div>
  );
}