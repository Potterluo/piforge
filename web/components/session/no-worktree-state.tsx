"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, FolderGit2, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";

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
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api/client";
import { humanizeError, repoUrlError } from "@/lib/errors";
import { qk } from "@/lib/session/queries";

/**
 * The right-hand panel for a session the provisioner never gave a worktree.
 *
 * It replaces the workspace panels rather than sitting beside them: the file
 * tree, Git and terminal all answer 422 for such a session, so mounting them
 * produced five copies of "this session has no worktree yet" and a retry
 * button that could never succeed. The cause is almost always the project's
 * repository address, so the two actions here are the two that can change the
 * outcome: correct the address, or delete the project.
 */
export function NoWorktreeState({
  projectId,
  projectName,
  repoUrl,
}: {
  projectId: string;
  projectName?: string;
  repoUrl?: string;
}) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [fixOpen, setFixOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [url, setUrl] = useState(repoUrl ?? "");
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const refreshProject = () => {
    void queryClient.invalidateQueries({ queryKey: qk.project(projectId) });
    void queryClient.invalidateQueries({ queryKey: qk.projects });
    void queryClient.invalidateQueries({ queryKey: qk.sessions(projectId) });
  };

  const openFix = () => {
    setUrl(repoUrl ?? "");
    setFieldError(null);
    setFixOpen(true);
  };

  const save = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const message = repoUrlError(url);
    if (message) {
      setFieldError(message);
      return;
    }
    setSaving(true);
    try {
      await api.projects.update(projectId, { repo_url: url.trim() });
      toast.success("仓库地址已保存", {
        description: "新建一个会话会按新地址重新创建工作区。",
      });
      setFixOpen(false);
      refreshProject();
    } catch (error) {
      setFieldError(humanizeError(error, "保存失败，请检查仓库地址"));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    setDeleting(true);
    try {
      await api.projects.remove(projectId);
      queryClient.removeQueries({ queryKey: qk.project(projectId) });
      void queryClient.invalidateQueries({ queryKey: qk.projects });
      toast.success("项目已删除");
      router.replace("/");
    } catch (error) {
      toast.error("删除项目失败", { description: humanizeError(error) });
      setDeleting(false);
    }
  };

  return (
    <div
      className="flex h-full min-h-0 flex-1 items-center justify-center overflow-y-auto p-6"
      data-testid="no-worktree-state"
    >
      <div className="flex w-full max-w-lg flex-col gap-4 rounded-lg border border-dashed border-border p-5">
        <div className="flex items-start gap-3">
          <AlertTriangle className="mt-0.5 size-5 shrink-0 text-amber-600" />
          <div className="flex min-w-0 flex-col gap-1">
            <h2 className="text-sm font-medium" data-testid="no-worktree-title">
              这个会话还没有工作区
            </h2>
            <p className="text-xs text-muted-foreground">
              创建会话时没能检出仓库，所以文件、Git 和终端都没有可操作的目录。
              仓库地址写错、分支不存在或者克隆失败，都会留下这个状态。
            </p>
          </div>
        </div>

        <div className="flex flex-col gap-1 rounded-md bg-muted/50 p-3 text-xs">
          <span className="text-muted-foreground">项目</span>
          <span className="truncate font-medium" data-testid="no-worktree-project-name">
            {projectName || "未命名项目"}
          </span>
          <span className="mt-1 text-muted-foreground">仓库地址</span>
          <code className="truncate font-mono" data-testid="no-worktree-repo">
            {repoUrl || "空白工作区"}
          </code>
        </div>

        <p className="text-xs text-muted-foreground">
          改好地址后，<span className="text-foreground">新建一个会话</span>
          会按新地址重新创建工作区；这个会话本身不会被删除。
        </p>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" data-testid="no-worktree-fix" onClick={openFix}>
            <Pencil />
            修改仓库地址
          </Button>
          <Button
            size="sm"
            variant="destructive"
            data-testid="no-worktree-delete"
            onClick={() => setDeleteOpen(true)}
          >
            <Trash2 />
            删除项目
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="no-worktree-project"
            render={<Link href={`/p/${projectId}`} />}
          >
            <FolderGit2 />
            返回项目
          </Button>
        </div>
      </div>

      <Dialog open={fixOpen} onOpenChange={setFixOpen}>
        <DialogContent className="sm:max-w-md">
          <form className="flex flex-col gap-4" onSubmit={save}>
            <DialogHeader>
              <DialogTitle>修改仓库地址</DialogTitle>
              <DialogDescription>
                地址必须是 https 链接或 git@主机:路径；留空表示空白工作区。保存后请新建会话验证。
              </DialogDescription>
            </DialogHeader>
            <Field data-invalid={fieldError ? true : undefined}>
              <FieldLabel htmlFor="no-worktree-repo-input">仓库地址</FieldLabel>
              <Input
                id="no-worktree-repo-input"
                data-testid="no-worktree-repo-input"
                value={url}
                aria-invalid={fieldError ? true : undefined}
                onChange={(event) => {
                  setUrl(event.target.value);
                  setFieldError(null);
                }}
                placeholder="https://github.com/acme/cache-service.git"
                autoFocus
              />
              <FieldDescription>留空会清空仓库地址，项目变成空白工作区。</FieldDescription>
              <FieldError data-testid="no-worktree-repo-error">{fieldError}</FieldError>
            </Field>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setFixOpen(false)}>
                取消
              </Button>
              <Button type="submit" data-testid="no-worktree-save" disabled={saving}>
                {saving ? <Spinner /> : null}
                保存
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除「{projectName || "这个项目"}」？</AlertDialogTitle>
            <AlertDialogDescription>
              项目下的所有会话、工作区和改动记录都会一并删除，无法恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              data-testid="no-worktree-delete-confirm"
              disabled={deleting}
              onClick={() => void remove()}
            >
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
