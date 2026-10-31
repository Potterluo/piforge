"use client";

import * as React from "react";
import { GitMerge, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { useSession } from "@/lib/session/queries";
import { useAsync } from "@/components/workspace/use-async";
import {
  adoptFilePaths,
  adoptModeLabel,
  adoptSummary,
  previewAdopt,
} from "@/lib/git/adopt";
import { useQueryClient } from "@tanstack/react-query";

/**
 * The confirmation for adopting a branch (PRD FR-2.6).
 *
 * The action used to live only in the sidebar's hover menu of the branch row,
 * with no confirmation and no summary — an acceptance tester opened the main
 * session's menu, saw 对比 but no merge, and concluded the product could not
 * adopt at all. This dialog is reachable from the branch's own header and from
 * that menu, and it says three things before the button is pressed:
 *
 *   - which branch, into which session and branch (source → target),
 *   - what will come over (the commits the target does not have, and the files),
 *   - whether the target is in a state where a merge is safe.
 *
 * The backend still owns the merge; nothing here re-implements it. After the
 * call the outcome names the mode (fast-forward / merge commit / applied to
 * working tree) and the files, and a conflict keeps the dialog open with the
 * server's file list instead of a bare toast.
 */
export function AdoptDialog({
  branchId,
  open,
  onOpenChange,
  onAdopted,
}: {
  branchId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Fired after a successful adopt, so the caller can refresh its own lists. */
  onAdopted?: (result: { mode: string; commit: string; files: string[]; targetId: string }) => void;
}) {
  const queryClient = useQueryClient();
  const branchSession = useSession(branchId);
  const branch = branchSession.data?.session;
  const targetId = branch?.parent_id ?? "";
  const targetSession = useSession(targetId);
  const target = targetSession.data?.session;

  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [revision, setRevision] = React.useState(0);

  // Opening the dialog is the moment to re-read both checkouts: the lists can
  // change while the sheet is closed (a commit in another tab, a run settling).
  React.useEffect(() => {
    if (!open) return;
    setError(null);
    setRevision((value) => value + 1);
  }, [open]);

  const branchStatus = useAsync(`adopt-source:${branchId}:${revision}`, () => api.git.status(branchId), {
    enabled: open,
  });
  const targetStatus = useAsync(`adopt-target:${targetId}:${revision}`, () => api.git.status(targetId), {
    enabled: open && Boolean(targetId),
  });
  const branchLog = useAsync(`adopt-log:${branchId}:${revision}`, () => api.git.log(branchId, 200), {
    enabled: open,
  });
  const targetLog = useAsync(`adopt-log:${targetId}:${revision}`, () => api.git.log(targetId, 200), {
    enabled: open && Boolean(targetId),
  });

  const preview = React.useMemo(() => {
    const branchGraph = branchLog.data;
    const targetGraph = targetLog.data;
    if (!branchGraph || !targetGraph) return null;
    return previewAdopt(branchGraph.head, branchGraph.commits, targetGraph.head, targetGraph.commits);
  }, [branchLog.data, targetLog.data]);

  // What the commits touch, measured from the fork point. Only asked for when
  // there are commits to measure: an empty range would spend a request to say 0.
  const rangeDiff = useAsync(
    `adopt-files:${branchId}:${preview?.base ?? ""}:${branchLog.data?.head ?? ""}:${revision}`,
    () =>
      api.git.diff(branchId, "range", "", {
        from: preview?.base ?? "HEAD~1",
        to: branchLog.data?.head ?? "HEAD",
      }),
    { enabled: open && Boolean(preview?.base) && (preview?.commits.length ?? 0) > 0 },
  );

  const committedFiles = React.useMemo(
    () => adoptFilePaths(rangeDiff.data?.diffs ?? []),
    [rangeDiff.data],
  );
  const uncommittedPaths = React.useMemo(
    () => (branchStatus.data?.changes ?? []).map((change) => change.path),
    [branchStatus.data],
  );
  const files = React.useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const path of [...committedFiles, ...uncommittedPaths]) {
      if (!path || seen.has(path)) continue;
      seen.add(path);
      out.push(path);
    }
    return out;
  }, [committedFiles, uncommittedPaths]);

  const targetDirty = Boolean(targetStatus.data && !targetStatus.data.clean);
  const loading = !branch || !target || !branchStatus.data || !targetStatus.data || !preview;
  const nothingToAdopt =
    Boolean(preview) && preview?.commits.length === 0 && uncommittedPaths.length === 0;

  const adopt = async () => {
    setBusy(true);
    setError(null);
    try {
      const data = await api.sessions.adopt(branchId);
      const adopted = data.adopted;
      toast.success(`采纳完成：${adoptModeLabel(adopted.mode)}`, {
        description: adoptSummary(adopted),
      });
      // The target is another session, so its cached status/log and the sidebar
      // counts are what changed; invalidate rather than trying to patch them.
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
      void queryClient.invalidateQueries({ queryKey: ["session", data.target] });
      void queryClient.invalidateQueries({ queryKey: ["session", branchId] });
      onAdopted?.({ ...adopted, targetId: data.target });
      onOpenChange(false);
    } catch (cause) {
      // The server's conflict message names the files and says what state the
      // target was left in. Keep it on screen: a toast disappears before a list
      // of conflicted paths has been read.
      const message = humanizeError(cause, "采纳失败，请稍后重试");
      setError(message);
      toast.error("采纳失败", { description: message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent data-testid="adopt-dialog">
        <DialogHeader>
          <DialogTitle>采纳这个分支</DialogTitle>
          <DialogDescription>
            把分支的工作带回它分叉出来的会话：先合并分支上已有的提交，再把剩下的未提交改动应用到目标工作区。
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3 text-xs" data-testid="adopt-summary">
          <div className="grid grid-cols-[3.5rem_1fr] items-start gap-x-2 gap-y-1 rounded-md border p-2">
            <span className="text-muted-foreground">来源</span>
            <span className="flex min-w-0 flex-wrap items-center gap-1.5">
              <span className="truncate font-medium">{branch?.title || branch?.label || "这个分支"}</span>
              <Badge variant="secondary" className="max-w-full truncate font-mono text-[10px]">
                {branchStatus.data?.branch ?? "…"}
              </Badge>
            </span>
            <span className="text-muted-foreground">目标</span>
            <span className="flex min-w-0 flex-wrap items-center gap-1.5">
              <span className="truncate font-medium" data-testid="adopt-target-session">
                {target?.title || "来源会话"}
              </span>
              <Badge variant="secondary" className="max-w-full truncate font-mono text-[10px]">
                {targetStatus.data?.branch ?? "…"}
              </Badge>
            </span>
          </div>

          {loading || !preview ? (
            <p className="text-muted-foreground">正在读取两边的 git 状态…</p>
          ) : (
            <>
              <div data-testid="adopt-commits">
                <p className="mb-1 font-medium">
                  提交 · {preview.commits.length} 个
                  {preview.commits.length > 0 ? (
                    <span className="ml-1 font-normal text-muted-foreground">
                      （目标是 {targetStatus.data?.branch}，这些提交会先合并进去）
                    </span>
                  ) : null}
                </p>
                {preview.commits.length === 0 ? (
                  <p className="text-muted-foreground">没有领先目标的提交，只带回未提交的改动。</p>
                ) : (
                  <ul className="max-h-32 space-y-0.5 overflow-y-auto rounded-md bg-muted/40 p-1.5">
                    {preview.commits.slice(0, 20).map((commit) => (
                      <li key={commit.hash} className="flex min-w-0 items-baseline gap-2">
                        <code className="shrink-0 font-mono text-[10px] text-muted-foreground">
                          {commit.short}
                        </code>
                        <span className="truncate">{commit.subject}</span>
                      </li>
                    ))}
                    {preview.commits.length > 20 ? (
                      <li className="text-muted-foreground">还有 {preview.commits.length - 20} 个提交…</li>
                    ) : null}
                  </ul>
                )}
              </div>

              <div data-testid="adopt-files">
                <p className="mb-1 font-medium">文件 · {files.length} 个</p>
                {files.length === 0 ? (
                  <p className="text-muted-foreground">这次采纳不会改动任何文件。</p>
                ) : (
                  <p className="break-all rounded-md bg-muted/40 p-1.5 font-mono text-[10px]">
                    {files.slice(0, 12).join("、")}
                    {files.length > 12 ? ` 等 ${files.length} 个` : ""}
                  </p>
                )}
                {uncommittedPaths.length > 0 ? (
                  <p className="mt-1 text-muted-foreground">
                    其中 {uncommittedPaths.length} 个是分支上还没提交的文件，采纳后会留在目标的工作区里，等你决定是否提交。
                  </p>
                ) : null}
              </div>

              {targetDirty ? (
                <p
                  data-testid="adopt-target-dirty"
                  className="rounded-md border border-amber-600/40 bg-amber-500/10 p-2 text-amber-700 dark:text-amber-400"
                >
                  目标会话的工作区有未提交的改动。采纳会被拒绝，以免覆盖你正在写的内容：先在「{target?.title || "目标会话"}
                  」里提交或撤销这些改动，再回来。
                </p>
              ) : (
                <p className="rounded-md bg-muted/40 p-2 text-muted-foreground">
                  目标工作区是干净的，可以安全合并。合并不会改动基准分支的历史：目标没动过就是快进，动过就是一条合并提交。
                </p>
              )}

              {nothingToAdopt ? (
                <p className="rounded-md border border-dashed p-2 text-muted-foreground">
                  这个分支既没有领先目标的提交，也没有未提交的改动，没有可采纳的内容。
                </p>
              ) : null}
            </>
          )}

          {error ? (
            <p
              data-testid="adopt-error"
              role="alert"
              className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-destructive"
            >
              {error}
            </p>
          ) : null}
        </div>

        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button
            size="sm"
            data-testid="adopt-submit"
            disabled={busy || loading || targetDirty || nothingToAdopt}
            onClick={() => void adopt()}
          >
            {busy ? <Loader2 className="animate-spin" /> : <GitMerge />}
            {busy ? "正在采纳…" : "采纳"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
