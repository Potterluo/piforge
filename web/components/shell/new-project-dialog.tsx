"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { useCreateProject } from "@/lib/session/queries";

/**
 * Creates a project and jumps straight into it: a project with no session has
 * nothing to show, so landing on its overview page is the useful next step.
 */
export function NewProjectDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const router = useRouter();
  const create = useCreateProject();
  const [name, setName] = useState("");
  const [repoUrl, setRepoUrl] = useState("");
  const [branch, setBranch] = useState("main");

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      toast.error("请填写项目名称");
      return;
    }
    try {
      const result = await create.mutateAsync({
        name: trimmed,
        repo_url: repoUrl.trim() || undefined,
        default_branch: branch.trim() || undefined,
      });
      toast.success("项目已创建", { description: result.project.name });
      setName("");
      setRepoUrl("");
      setBranch("main");
      onOpenChange(false);
      router.push(`/p/${result.project.id}`);
    } catch (error) {
      toast.error("创建项目失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>新建项目</DialogTitle>
            <DialogDescription>
              一个项目对应一个 git 仓库。仓库会在你专用的沙箱里检出，不会碰你的本机目录。
            </DialogDescription>
          </DialogHeader>

          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="project-name">名称</FieldLabel>
              <Input
                id="project-name"
                data-testid="project-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="例如：cache-service"
                autoFocus
                required
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="project-repo">仓库地址</FieldLabel>
              <Input
                id="project-repo"
                data-testid="project-repo"
                value={repoUrl}
                onChange={(event) => setRepoUrl(event.target.value)}
                placeholder="https://github.com/acme/cache-service.git"
              />
              <FieldDescription>留空则创建一个空白工作区。</FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="project-branch">基准分支</FieldLabel>
              <Input
                id="project-branch"
                value={branch}
                onChange={(event) => setBranch(event.target.value)}
                placeholder="main"
              />
            </Field>
          </FieldGroup>

          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" data-testid="project-submit" disabled={create.isPending}>
              {create.isPending ? <Spinner /> : null}
              创建
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
