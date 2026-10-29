"use client";

import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { humanizeError, repoUrlError } from "@/lib/errors";
import {
  deriveProjectName,
  freeProjectName,
  PROJECT_NAME_PLACEHOLDER,
  repoBasename,
} from "@/lib/projects/name";
import { useCreateProject, useProjects } from "@/lib/session/queries";
import { ApiError } from "@/lib/types";

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
  const projects = useProjects();
  const [name, setName] = useState("");
  const [repoUrl, setRepoUrl] = useState("");
  const [branch, setBranch] = useState("main");
  const [repoError, setRepoError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);

  const existingNames = useMemo(
    () => (projects.data?.projects ?? []).map((project) => project.name),
    [projects.data],
  );
  // The name is optional: show what an empty field will produce while the user
  // types the address, so "leave it blank" is a visible outcome, not a guess.
  const derivedBase = useMemo(() => deriveProjectName(repoUrl), [repoUrl]);
  // The API appends -2, -3, … to a derived name that is already taken. Preview
  // the same result here: without it the dialog promises a name the server will
  // not use, which is exactly the surprise this fix removes.
  const derivedName = useMemo(
    () => freeProjectName(derivedBase, existingNames),
    [derivedBase, existingNames],
  );
  const typedName = name.trim();
  const typedConflict =
    typedName.length > 0 &&
    existingNames.some((existing) => existing.trim().toLowerCase() === typedName.toLowerCase());

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    // Reject locally first: a bad address must not create a project whose every
    // session then has no worktree. The API applies the same rule for callers
    // that do not use this form.
    const repoMessage = repoUrlError(repoUrl);
    if (repoMessage) {
      setRepoError(repoMessage);
      return;
    }
    try {
      const result = await create.mutateAsync({
        // An empty name is derived, not sent: the server owns the uniqueness
        // rule and appends -2, -3, … when the derived name is taken.
        name: typedName || undefined,
        repo_url: repoUrl.trim() || undefined,
        default_branch: branch.trim() || undefined,
      });
      toast.success("项目已创建", { description: result.project.name });
      setName("");
      setRepoUrl("");
      setBranch("main");
      setRepoError(null);
      setNameError(null);
      onOpenChange(false);
      router.push(`/p/${result.project.id}`);
    } catch (error) {
      if (error instanceof ApiError && error.code === "invalid_argument" && /^repo_url/.test(error.message)) {
        setRepoError(humanizeError(error, repoUrlError(repoUrl) ?? "仓库地址格式不支持"));
        return;
      }
      // A typed name that is already taken is a field error, not a mystery
      // toast: the server's 409 names the conflict, and the field is where the
      // user can fix it.
      if (error instanceof ApiError && error.code === "conflict") {
        setNameError(humanizeError(error));
        return;
      }
      toast.error("创建项目失败", {
        description: humanizeError(error),
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
              <FieldLabel htmlFor="project-repo">仓库地址</FieldLabel>
              <Input
                id="project-repo"
                data-testid="project-repo"
                value={repoUrl}
                aria-invalid={repoError ? true : undefined}
                onChange={(event) => {
                  setRepoUrl(event.target.value);
                  setRepoError(null);
                }}
                placeholder="https://github.com/acme/cache-service.git"
                autoFocus
              />
              <FieldDescription>留空则创建一个空白工作区。支持 https 链接或 git@主机:路径。</FieldDescription>
              <FieldError data-testid="project-repo-error">{repoError}</FieldError>
            </Field>
            <Field>
              <FieldLabel htmlFor="project-name">名称</FieldLabel>
              <Input
                id="project-name"
                data-testid="project-name"
                value={name}
                aria-invalid={nameError || typedConflict ? true : undefined}
                onChange={(event) => {
                  setName(event.target.value);
                  setNameError(null);
                }}
                placeholder={PROJECT_NAME_PLACEHOLDER}
              />
              <FieldDescription data-testid="project-name-derived">
                {typedName
                  ? typedConflict
                    ? `「${typedName}」已被占用（不区分大小写）。换一个名称，或者留空让系统自动加序号。`
                    : "用你填的名字。"
                  : derivedName !== derivedBase
                    ? `留空则用「${derivedName}」——「${derivedBase}」已被占用，自动加序号。`
                    : repoBasename(repoUrl)
                      ? `留空则用「${derivedName}」——仓库地址的最后一段，去掉 .git。`
                      : `留空则用「${derivedName}」——没有仓库地址时按日期命名。`}
              </FieldDescription>
              <FieldError data-testid="project-name-error">{nameError}</FieldError>
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
