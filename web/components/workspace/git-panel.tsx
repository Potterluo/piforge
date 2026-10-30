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
import { Input } from "@/components/ui/input";
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
import type { DiffScope, FileDiff } from "@/lib/types";
import { cn } from "@/lib/utils";
import { ChangeList } from "@/components/workspace/git-changes";
import { CreatePullRequest } from "@/components/workspace/create-pr";
import { changeRows, useGitData } from "@/components/workspace/git-data";
import { GitDiffView } from "@/components/workspace/git-diff";
import { PanelError, PanelSkeleton } from "@/components/workspace/panel-state";
import { useAsync } from "@/components/workspace/use-async";
import { splitHunks } from "@/lib/git/patch";
import {
  DEFAULT_RANGE,
  SCOPES,
  canStageHunks,
  diffQuery,
  isHistoricalScope,
  scopeLabel,
  type DiffRange,
} from "@/lib/git/diff-scope";

type HunkAction = "stage" | "unstage" | "discard";

/**
 * One row per hunk inside the diff the user is looking at.
 *
 * The index sent to the server is the hunk's position in *that* diff: the
 * server rebuilds the patch from the same scope and path, so an index is only
 * meaningful together with the scope it came from. The strip is hidden for
 * historical ranges — staging into a commit that already exists is not a thing,
 * and a button that cannot work is worse than no button.
 */
function HunkActions({
  scope,
  path,
  status,
  hunks,
  busy,
  onHunk,
}: {
  scope: DiffScope;
  path: string;
  status: string;
  hunks: string[];
  busy: boolean;
  onHunk: (action: HunkAction, path: string, index: number) => void;
}) {
  if (hunks.length === 0) return null;
  const readOnly = !canStageHunks(scope);
  // An added or deleted file is one hunk in practice, and reversing a creation
  // is a different operation. The server refuses it; the UI says so beforehand.
  const perHunk = status === "modified";

  return (
    <div data-testid="git-hunk-list" className="shrink-0 divide-y border-b">
      {hunks.map((hunk, index) => (
        <div
          key={index}
          data-testid="git-hunk-row"
          data-index={index}
          data-header={hunk.split("\n")[0]}
          className="flex min-w-0 items-center gap-1 px-2 py-0.5"
        >
          <span
            className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground"
            title={hunk.split("\n")[0]}
          >
            {hunk.split("\n")[0]}
          </span>
          {readOnly || !perHunk ? null : scope === "unstaged" ? (
            <>
              <Button
                size="xs"
                variant="ghost"
                data-testid="git-hunk-stage"
                disabled={busy}
                onClick={() => onHunk("stage", path, index)}
              >
                暂存
              </Button>
              <Button
                size="xs"
                variant="ghost"
                data-testid="git-hunk-discard"
                disabled={busy}
                onClick={() => onHunk("discard", path, index)}
              >
                丢弃
              </Button>
            </>
          ) : (
            <Button
              size="xs"
              variant="ghost"
              data-testid="git-hunk-unstage"
              disabled={busy}
              onClick={() => onHunk("unstage", path, index)}
            >
              取消暂存
            </Button>
          )}
        </div>
      ))}
      {readOnly ? (
        <p data-testid="git-hunk-readonly" className="px-2 py-1 text-[10px] text-muted-foreground">
          历史范围只读：暂存或丢弃只能在「工作区」「已暂存」里做。
        </p>
      ) : !perHunk ? (
        <p data-testid="git-hunk-readonly" className="px-2 py-1 text-[10px] text-muted-foreground">
          {status === "added" || status === "deleted"
            ? "新文件或删除的文件只能按文件操作。"
            : "这个文件没有可逐段操作的改动。"}
        </p>
      ) : null}
    </div>
  );
}

/** Diff body for one file in one scope. */
function DiffSection({
  scope,
  range,
  path,
  split,
  busy,
  onToggleSplit,
  onHunk,
}: {
  scope: DiffScope;
  range: DiffRange;
  path: string | null;
  split: boolean;
  busy: boolean;
  onToggleSplit: () => void;
  onHunk: (action: HunkAction, path: string, index: number) => void;
}) {
  const git = useGitData();
  // `revision` is in the key so a hunk action (which refreshes the git data)
  // re-reads this diff too; otherwise the pane would keep showing the patch
  // from before the staging it just performed.
  const query = diffQuery(scope, path ?? "", range).toString();
  const diff = useAsync(
    `diff:${git.sessionId}:${query}:${git.revision}`,
    () => api.git.diff(git.sessionId, scope, path ?? "", range),
    { enabled: Boolean(path) },
  );

  const file = diff.data?.diffs[0];
  const hunks = React.useMemo(() => (file ? splitHunks(file.patch) : []), [file]);

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <div className="flex shrink-0 items-center gap-1.5 border-b px-2 py-1">
        <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">
          {path ?? "选择文件查看差异"}
        </span>
        <span
          data-testid="git-diff-scope"
          data-scope={scope}
          className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
        >
          {scopeLabel(scope, range)}
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

      {file && !file.binary ? (
        <HunkActions
          scope={scope}
          path={file.path}
          status={file.status}
          hunks={hunks}
          busy={busy}
          onHunk={onHunk}
        />
      ) : null}

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
            <GitDiffView sessionId={git.sessionId} path={file.path} patch={file.patch} split={split} />
          )
        ) : (
          <p className="p-3 text-[11px] text-muted-foreground">这个文件在当前范围内没有差异。</p>
        )}
      </div>
    </div>
  );
}

/**
 * The files a historical scope touches.
 *
 * The change list only knows the working tree, so a commit range whose file has
 * no local edits could not be opened at all — the one case a review is for. The
 * list comes from the scope's own diff, and its first file is selected
 * implicitly so switching to 范围 shows something instead of an empty pane.
 */
function ScopeFiles({
  files,
  selected,
  loading,
  onSelect,
}: {
  files: FileDiff[];
  selected: string | null;
  loading: boolean;
  onSelect: (path: string) => void;
}) {
  return (
    <div
      data-testid="git-scope-files"
      data-files={files.length}
      className="flex shrink-0 flex-wrap items-center gap-1 border-b px-2 py-1"
    >
      <span className="text-[10px] text-muted-foreground">
        {loading && files.length === 0 ? "读取范围内文件…" : `范围内 ${files.length} 个文件`}
      </span>
      {files.map((file) => (
        <button
          key={file.path}
          type="button"
          data-testid="git-scope-file"
          data-path={file.path}
          onClick={() => onSelect(file.path)}
          className={cn(
            "flex min-w-0 items-center gap-1 rounded px-1 py-0.5 font-mono text-[10px] hover:bg-accent",
            selected === file.path && "bg-accent text-accent-foreground",
          )}
        >
          <span className="max-w-40 truncate">{file.path}</span>
          <span className="text-emerald-600 dark:text-emerald-400">+{file.additions}</span>
          {file.deletions > 0 ? (
            <span className="text-red-600 dark:text-red-400">−{file.deletions}</span>
          ) : null}
        </button>
      ))}
    </div>
  );
}

/**
 * 变更 tab: working tree status, staging, the commit box, and the diff viewer.
 *
 * Everything mutating goes through `git.refresh()` so the 变更 and Git tabs
 * never disagree about the tree. The diff scopes follow the same rule: changing
 * the scope is a different request, not a relabelled pane (FR-12.1).
 */
export function GitChangesPanel() {
  const git = useGitData();
  const [selectedPath, setSelectedPath] = React.useState<string | null>(null);
  const [scope, setScope] = React.useState<DiffScope>("unstaged");
  // `range` is what is applied; `draft` is what the inputs hold until 查看.
  const [range, setRange] = React.useState<DiffRange>(DEFAULT_RANGE);
  const [draft, setDraft] = React.useState<DiffRange>(DEFAULT_RANGE);
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

  const historical = isHistoricalScope(scope);
  const scopeDiff = useAsync(
    `scope-files:${git.sessionId}:${scope}:${range.from}:${range.to}:${git.revision}`,
    () => api.git.diff(git.sessionId, scope, "", range),
    { enabled: historical },
  );
  const scopeFiles: FileDiff[] = historical ? (scopeDiff.data?.diffs ?? []) : [];
  // Which file the diff below shows. Historical scopes fall back to their first
  // file without writing that choice into state (which would fight the user's
  // own selection when they switch back).
  const activePath = React.useMemo(() => {
    if (!historical) return selectedPath;
    const paths = scopeFiles.map((file) => file.path);
    return selectedPath && paths.includes(selectedPath) ? selectedPath : (paths[0] ?? null);
  }, [historical, scopeFiles, selectedPath]);

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

  // One hunk: the index is its position in the diff the server returned for this
  // scope and path, never a patch built by the browser.
  const hunk = React.useCallback(
    (action: HunkAction, path: string, index: number) => {
      if (action === "discard") {
        toast.warning(`丢弃 ${path} 的第 ${index + 1} 段改动？`, {
          description: "只还原这一段，文件里的其他改动保留。无法撤销。",
          action: {
            label: "丢弃这段",
            onClick: () => void mutate(() => api.git.hunk(git.sessionId, path, index, "discard")),
          },
        });
        return;
      }
      void mutate(() => api.git.hunk(git.sessionId, path, index, action));
    },
    [git.sessionId, mutate],
  );

  const applyRange = React.useCallback(() => {
    setRange({
      from: draft.from.trim() || DEFAULT_RANGE.from,
      to: draft.to.trim() || DEFAULT_RANGE.to,
    });
  }, [draft]);

  const commit = React.useCallback(() => {
    const trimmed = message.trim();
    if (!trimmed) {
      setActionError("提交信息不能为空");
      textareaRef.current?.focus();
      return;
    }
    void mutate(async () => {
      // `paths: []` commits the index exactly as it stands. Passing the staged
      // paths back made the server `git add` them again, which swept the hunks
      // still in the worktree into the commit — so staging one hunk and
      // committing silently staged the other one too.
      await api.git.commit(git.sessionId, trimmed, [], amend);
      setMessage("");
      setAmend(false);
    });
  }, [amend, git.sessionId, message, mutate]);

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
            data-testid="git-refresh"
            onClick={git.refresh}
            disabled={git.status.loading}
          >
            <RefreshCwIcon className={git.status.loading ? "animate-spin" : undefined} />
          </Button>
          <Button size="xs" onClick={() => textareaRef.current?.focus()} disabled={busy}>
            提交
          </Button>
          <RemoteActions />
          {/* The last step of "change → test → share" (FR-12.6). It is always
              visible: a branch that cannot be PR'd (same as base, no commits)
              says why inside the dialog instead of hiding the feature. */}
          <CreatePullRequest sessionId={git.sessionId} />
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
                  selectedPath={activePath}
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
            <div className="flex shrink-0 flex-wrap items-center gap-1 border-b px-2 py-1">
              <GitCompareIcon className="size-3 text-muted-foreground" />
              <Tabs value={scope} onValueChange={(value) => setScope(value as DiffScope)}>
                <TabsList variant="line" className="h-6 gap-0">
                  {SCOPES.map((entry) => (
                    <TabsTrigger
                      key={entry.value}
                      value={entry.value}
                      data-testid={`git-scope-${entry.value}`}
                      className="px-1.5 text-[10px]"
                    >
                      {entry.label}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
              {scope === "range" ? (
                <div className="flex items-center gap-1">
                  <Input
                    data-testid="git-range-from"
                    aria-label="范围起点"
                    value={draft.from}
                    onChange={(event) =>
                      setDraft((current) => ({ ...current, from: event.target.value }))
                    }
                    onKeyDown={(event) => {
                      if (event.key === "Enter") applyRange();
                    }}
                    className="h-6 w-20 px-1 font-mono text-[10px]"
                  />
                  <span className="text-[10px] text-muted-foreground">..</span>
                  <Input
                    data-testid="git-range-to"
                    aria-label="范围终点"
                    value={draft.to}
                    onChange={(event) =>
                      setDraft((current) => ({ ...current, to: event.target.value }))
                    }
                    onKeyDown={(event) => {
                      if (event.key === "Enter") applyRange();
                    }}
                    className="h-6 w-20 px-1 font-mono text-[10px]"
                  />
                  <Button
                    size="xs"
                    data-testid="git-range-apply"
                    disabled={busy}
                    onClick={applyRange}
                  >
                    查看
                  </Button>
                </div>
              ) : null}
            </div>
            {historical ? (
              <ScopeFiles
                files={scopeFiles}
                selected={activePath}
                loading={scopeDiff.loading}
                onSelect={setSelectedPath}
              />
            ) : null}
            <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
              <DiffSection
                scope={scope}
                range={range}
                path={activePath}
                split={split}
                busy={busy}
                onToggleSplit={() => setSplit((value) => !value)}
                onHunk={hunk}
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
