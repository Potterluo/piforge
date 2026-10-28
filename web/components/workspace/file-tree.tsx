"use client";

import * as React from "react";
import dynamic from "next/dynamic";
import {
  ArrowDownIcon,
  ArrowUpIcon,
  FolderOpenIcon,
  RefreshCwIcon,
  SearchIcon,
} from "lucide-react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import api from "@/lib/api/client";
import { baseName, gitLetterMap } from "@/lib/fs/file-kind";
import type { FileNode } from "@/lib/types";
import { PanelError, PanelSkeleton } from "@/components/workspace/panel-state";
import { useAsync } from "@/components/workspace/use-async";
import { useOptionalSessionEventState } from "@/components/session/session-events-provider";
import {
  buildTree,
  collectDirs,
  collectFiles,
  filterFiles,
  type TreeItem,
} from "@/components/workspace/tree-model";

/**
 * Lazy, virtualised file tree.
 *
 * Children are fetched per directory (`depth=2` for the root so the first
 * screen is useful, `depth=1` afterwards), which is what keeps a 100k-file
 * repository from being loaded in one response.
 *
 * `react-arborist` is imported through `next/dynamic` with `ssr: false`: it
 * mounts a `react-dnd` backend, which needs a DOM.
 */
const FileTreeCanvas = dynamic(
  () => import("@/components/workspace/file-tree-canvas").then((mod) => mod.FileTreeCanvas),
  { ssr: false, loading: () => <PanelSkeleton rows={12} /> },
);

export interface FileTreeProps {
  sessionId: string;
  /** Bumped by other panels when they write into the checkout. */
  revision?: number;
  activePath?: string | null;
  onOpenFile: (path: string) => void;
  /** Emitted by "在终端中打开"; the shell decides which terminal tab to open. */
  onRevealInTerminal?: (path: string, isDir: boolean) => void;
}

interface PendingDelete {
  path: string;
  isDir: boolean;
}

export function FileTree({ sessionId, revision = 0, activePath, onOpenFile, onRevealInTerminal }: FileTreeProps) {
  const [showIgnored, setShowIgnored] = React.useState(false);
  const [filter, setFilter] = React.useState("");
  const [extraDirs, setExtraDirs] = React.useState<Record<string, FileNode[]>>({});
  const [loadingDirs, setLoadingDirs] = React.useState<string[]>([]);
  const [pending, setPending] = React.useState<PendingDelete | null>(null);
  const [actionError, setActionError] = React.useState<string | null>(null);

  // The switch is part of the query key: the server decides what to list, so
  // flipping it has to refetch rather than hide rows the client already has.
  // The agent writes files during a run; a tree that waits for a click is wrong
  // about the workspace for as long as the user trusts it.
  const settled = useOptionalSessionEventState()?.state.settled ?? 0;

  const tree = useAsync(`files:${sessionId}:${showIgnored ? "all" : "tracked"}:${settled}:${revision}`, async () => {
    const response = await api.files.tree(sessionId, "", 2, showIgnored);
    return { nodes: response.nodes, dirs: collectDirs(response.nodes) };
  });

  const status = useAsync(`git-status:${sessionId}`, () => api.git.status(sessionId));

  const dirs = React.useMemo(
    () => ({ ...(tree.data?.dirs ?? {}), ...extraDirs }),
    [tree.data, extraDirs],
  );
  // Wrapped in a memo: the `?? []` fallback would otherwise be a new array on
  // every render and invalidate every downstream useMemo.
  const root = React.useMemo(() => tree.data?.nodes ?? [], [tree.data]);
  const gitMap = React.useMemo(
    () => gitLetterMap(status.data?.changes ?? []),
    [status.data],
  );

  const files = React.useMemo(
    () => collectFiles(root, dirs, showIgnored),
    [root, dirs, showIgnored],
  );

  const items = React.useMemo<TreeItem[]>(() => {
    if (filter.trim()) {
      return filterFiles(files, filter).map((path) => ({
        id: path,
        name: path,
        path,
        isDir: false,
        children: null,
      }));
    }
    return buildTree(root, dirs, showIgnored);
  }, [filter, files, root, dirs, showIgnored]);

  const loadDir = React.useCallback(
    async (path: string) => {
      if (dirs[path] || loadingDirs.includes(path)) return;
      setLoadingDirs((previous) => [...previous, path]);
      try {
        const response = await api.files.tree(sessionId, path, 1, showIgnored);
        setExtraDirs((previous) => ({ ...previous, [path]: response.nodes }));
      } catch (error) {
        setActionError(error instanceof Error ? error.message : "目录加载失败");
      } finally {
        setLoadingDirs((previous) => previous.filter((entry) => entry !== path));
      }
    },
    [dirs, loadingDirs, sessionId],
  );

  const handleToggleDir = React.useCallback(
    (path: string, open: boolean) => {
      if (open) void loadDir(path);
    },
    [loadDir],
  );

  const handleRefresh = React.useCallback(() => {
    setExtraDirs({});
    tree.reload();
    status.reload();
  }, [status, tree]);

  const handleDownload = React.useCallback(
    (path: string) => {
      const link = document.createElement("a");
      link.href = api.files.downloadUrl(sessionId, path);
      link.download = baseName(path);
      document.body.appendChild(link);
      link.click();
      link.remove();
    },
    [sessionId],
  );

  const handleCopyPath = React.useCallback((path: string) => {
    void navigator.clipboard?.writeText(path).catch(() => {});
  }, []);

  const handleDelete = React.useCallback(async () => {
    if (!pending) return;
    const target = pending;
    setPending(null);
    try {
      await api.files.remove(sessionId, target.path);
      setExtraDirs({});
      tree.reload();
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "删除失败");
    }
  }, [pending, sessionId, tree]);

  const loading = tree.loading && !tree.data;
  const error = tree.error;

  return (
    <div data-testid="file-tree" className="flex h-full min-h-0 min-w-0 flex-col">
      <header className="flex shrink-0 flex-wrap items-center gap-1 border-b px-2 py-1.5">
        <Badge variant="secondary" className="max-w-[45%] truncate font-mono text-[10px]">
          {status.data?.branch ?? "—"}
        </Badge>
        {status.data ? (
          <span className="flex items-center gap-0.5 text-[10px] text-muted-foreground">
            <ArrowUpIcon className="size-3" />
            {status.data.ahead}
            <ArrowDownIcon className="ml-0.5 size-3" />
            {status.data.behind}
          </span>
        ) : null}
        <div className="ms-auto flex items-center gap-1.5">
          <Tooltip>
            <TooltipTrigger
              render={
                <span className="flex cursor-pointer items-center gap-1 text-[10px] text-muted-foreground" />
              }
            >
              显示忽略文件
              <Switch
                size="sm"
                checked={showIgnored}
                onCheckedChange={(checked) => setShowIgnored(checked)}
                aria-label="显示忽略文件"
              />
            </TooltipTrigger>
            <TooltipContent side="bottom">显示 node_modules / dist 等被忽略的目录</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="刷新文件树"
                  onClick={handleRefresh}
                />
              }
            >
              <RefreshCwIcon className={loading ? "animate-spin" : undefined} />
            </TooltipTrigger>
            <TooltipContent side="bottom">刷新</TooltipContent>
          </Tooltip>
        </div>
      </header>

      <div className="relative shrink-0 px-2 py-1.5">
        <SearchIcon className="pointer-events-none absolute top-1/2 left-4 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="筛选已加载文件"
          aria-label="筛选已加载文件"
          className="h-7 pr-2 pl-7 text-xs"
        />
      </div>

      {actionError ? (
        <PanelError
          message={actionError}
          onRetry={() => {
            setActionError(null);
            handleRefresh();
          }}
          className="border-t"
        />
      ) : null}

      {loading ? <PanelSkeleton rows={12} /> : null}

      {error ? (
        <PanelError message={error} onRetry={tree.reload} />
      ) : null}

      {!loading && !error && items.length === 0 ? (
        <Empty className="flex-1">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <FolderOpenIcon />
            </EmptyMedia>
            <EmptyTitle>{filter ? "没有匹配的文件" : "目录为空"}</EmptyTitle>
            <EmptyDescription>
              {filter ? "筛选只覆盖已经展开过的目录。" : "这个工作目录里还没有文件。"}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : null}

      {!loading && !error && items.length > 0 ? (
        <FileTreeCanvas
          items={items}
          flat={filter.trim().length > 0}
          gitMap={gitMap}
          activePath={activePath}
          onOpen={onOpenFile}
          onToggleDir={handleToggleDir}
          onDelete={setPending}
          onDownload={handleDownload}
          onCopyPath={handleCopyPath}
          onRevealInTerminal={(path, isDir) => onRevealInTerminal?.(path, isDir)}
        />
      ) : null}

      <AlertDialog open={pending !== null} onOpenChange={(open) => !open && setPending(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除{pending?.isDir ? "目录" : "文件"}？</AlertDialogTitle>
            <AlertDialogDescription className="font-mono break-all">
              {pending?.path}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => void handleDelete()}>删除</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
