"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Download, Plus, Sparkles, Trash } from "lucide-react";

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
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Item, ItemActions, ItemContent, ItemDescription, ItemMedia, ItemTitle } from "@/components/ui/item";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api, type SkillFile, type SkillImportResult } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { formatRelative } from "@/lib/session/format";
import { qk, useCurrentUser } from "@/lib/session/queries";

/**
 * What an import actually did, as one toast.
 *
 * The old toast said "导入了 N 个 Skill" whenever the bundle had been replaced,
 * even when the row stayed disabled — so the skill never reached the agent while
 * the UI claimed success. The server now re-enables a replaced skill and reports
 * that in `reenabled`; this function is where the promise is kept in words.
 */
function importToast(result: SkillImportResult, fallbackReason: string) {
  const imported = result.skills.filter((skill) => !skill.skipped);
  const names = imported.map((skill) => skill.name);
  const reenabled = imported.filter((skill) => skill.reenabled).map((skill) => skill.name);
  const skipped = result.skills.filter((skill) => skill.skipped);

  if (names.length === 0) {
    toast.error("没有导入任何 Skill", {
      description: skipped[0]?.reason ?? fallbackReason,
    });
    return;
  }
  const description =
    reenabled.length > 0
      ? `${names.join("、")}（${reenabled.join("、")} 之前是停用状态，已重新启用）`
      : names.join("、");
  toast.success(`导入了 ${names.length} 个 Skill`, { description });
  if (skipped.length > 0) {
    toast.warning(`${skipped.length} 个 Skill 没有导入`, {
      description: skipped.map((skill) => `${skill.name}：${skill.reason ?? "被跳过"}`).join("；"),
    });
  }
}

export default function SkillsPage() {
  const queryClient = useQueryClient();
  const skills = useQuery({ queryKey: qk.skills, queryFn: () => api.skills.list() });
  // A shared skill belongs to the deployment: only an admin may delete one, so the
  // button has to know who is looking.
  const me = useCurrentUser();
  const isAdmin = me.data?.user.role === "admin";
  const [dialogOpen, setDialogOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const importMutation = useMutation({
    mutationFn: (input: { url: string; ref: string }) => api.skills.import(input.url, input.ref),
    onSuccess: (result) => {
      const imported = result.skills.some((skill) => !skill.skipped);
      importToast(result, "仓库里没有可用的 SKILL.md");
      if (imported) {
        setImportOpen(false);
        setImportUrl("");
        setImportRef("");
        void queryClient.invalidateQueries({ queryKey: qk.skills });
      }
    },
    onError: (error: Error) => toast.error("导入失败", { description: humanizeError(error) }),
  });
  const [importUrl, setImportUrl] = useState("");
  const [importRef, setImportRef] = useState("");
  const [importZip, setImportZip] = useState<File | null>(null);
  // The zip half of FR-14.4. It shares the import dialog with the git form: both
  // answer "install a collection in one action", and two dialogs would make the
  // second one look like a different feature.
  const zipMutation = useMutation({
    mutationFn: (file: File) => api.skills.importZip(file),
    onSuccess: (result) => {
      const imported = result.skills.some((skill) => !skill.skipped);
      importToast(result, "zip 里没有可用的 SKILL.md");
      if (imported) {
        setImportOpen(false);
        setImportZip(null);
        void queryClient.invalidateQueries({ queryKey: qk.skills });
      }
    },
    onError: (error: Error) => toast.error("导入失败", { description: humanizeError(error) }),
  });
  // Enabling/disabling is a mutation per row; the row's own pending state is
  // enough, and a global spinner would freeze every other switch.
  const toggleEnabled = useMutation({
    mutationFn: (input: { id: string; enabled: boolean }) =>
      api.skills.setEnabled(input.id, input.enabled),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: qk.skills });
      toast.success(result.skill.enabled ? "Skill 已启用" : "Skill 已停用", {
        description: result.skill.enabled
          ? "新建的会话会重新挂载它。"
          : "新建的会话不再挂载它；已挂载的副本不受影响。",
      });
    },
    onError: (error) => toast.error("更新失败", { description: humanizeError(error) }),
  });
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [body, setBody] = useState("");
  const [assets, setAssets] = useState<SkillFile[]>([]);
  // Only an admin sees the switch. Creating a shared skill is the missing half
  // of "共享 Skill 由管理员维护": before this, an admin could delete a shared
  // skill but had no way to create one (FR-14.1 / FR-16.3).
  const [shared, setShared] = useState(false);

  const create = useMutation({
    // Two steps on purpose: the row is created first (its id names the bundle
    // directory), then the files land on it. A failed upload leaves a skill with
    // a body and no assets, which the editor can fix; the reverse would be a
    // skill that reports content it does not have.
    mutationFn: async () => {
      const created = await api.skills.create({
        name: name.trim(),
        description: description.trim(),
        body: body.trim() || undefined,
        scope: shared && isAdmin ? "admin" : undefined,
      });
      if (assets.length > 0) {
        // The bundle is replaced wholesale, so SKILL.md from the textarea has to
        // be part of the same upload — sending only the picked files would
        // delete the body that was just typed.
        const bundle: SkillFile[] = [
          ...(body.trim() ? [{ path: "SKILL.md", content: body }] : []),
          ...assets.filter((file) => file.path !== "SKILL.md"),
        ];
        await api.skills.uploadBundle(created.skill.id, bundle);
      }
      return created;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.skills });
      setDialogOpen(false);
      setName("");
      setDescription("");
      setBody("");
      setAssets([]);
      setShared(false);
      toast.success("Skill 已创建");
    },
    onError: (error) => {
      toast.error("创建 Skill 失败", {
        description: humanizeError(error),
      });
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.skills.remove(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.skills });
      toast.success("Skill 已删除");
    },
    onError: (error) => {
      toast.error("删除失败", {
        description: humanizeError(error),
      });
    },
  });

  const list = skills.data?.skills ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <header className="flex flex-wrap items-start gap-3 border-b border-border p-4">
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className="text-base font-medium">Skill 中心</h1>
          <p className="max-w-2xl text-xs text-muted-foreground">
            Skill 是一份 SKILL.md：agent 按需加载，用来学会某个具体流程。共享 Skill 由管理员维护，私有 Skill 只有你自己能用。
          </p>
        </div>
        <Button size="sm" className="ml-auto" data-testid="skill-add" onClick={() => setDialogOpen(true)}>
          <Plus />
          新建 Skill
        </Button>
        <Button variant="outline" size="sm" data-testid="skill-import" onClick={() => setImportOpen(true)}>
          <Download />
          导入 Skill
        </Button>
      </header>

      <div className="flex flex-col gap-2 p-4">
        {skills.isPending ? (
          <>
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </>
        ) : skills.isError ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            Skill 列表加载失败
            <Button size="sm" variant="outline" onClick={() => void skills.refetch()}>
              重试
            </Button>
          </div>
        ) : list.length === 0 ? (
          <Empty className="border border-dashed border-border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Sparkles />
              </EmptyMedia>
              <EmptyTitle>还没有 Skill</EmptyTitle>
              <EmptyDescription>
                目前没有可用的 Skill。你可以新建一个私有 Skill，把团队里的固定操作流程写下来给 agent 用。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" onClick={() => setDialogOpen(true)}>
                <Plus />
                新建 Skill
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          list.map((skill) => (
            <Item key={skill.id} variant="outline" data-testid="skill-item" data-name={skill.name}>
              <ItemMedia variant="icon">
                <Sparkles />
              </ItemMedia>
              <ItemContent className="min-w-0 gap-1">
                <div className="flex flex-wrap items-center gap-2">
                  <ItemTitle>{skill.name}</ItemTitle>
                  <Badge variant="outline">v{skill.version}</Badge>
                  <Badge variant={skill.scope === "admin" ? "secondary" : "outline"}>
                    {skill.scope === "admin" ? "共享" : "私有"}
                  </Badge>
                  {skill.enabled ? null : <Badge variant="destructive">已停用</Badge>}
                </div>
                <ItemDescription className="text-xs">{skill.description}</ItemDescription>
                <ItemDescription className="font-mono text-[11px]">
                  {skill.content_hash.slice(0, 12)} · {formatRelative(skill.created_at)}
                </ItemDescription>
              </ItemContent>
              <ItemActions>
                <Switch
                  size="sm"
                  data-testid="skill-enabled"
                  data-skill={skill.name}
                  aria-label={`启用 ${skill.name}`}
                  title={
                    skill.scope === "admin" && !isAdmin
                      ? "共享 Skill 由管理员维护"
                      : skill.enabled
                        ? "停用：新建会话不再挂载它"
                        : "启用：新建会话会挂载它"
                  }
                  checked={skill.enabled}
                  disabled={(skill.scope === "admin" && !isAdmin) || toggleEnabled.isPending}
                  onCheckedChange={(checked) =>
                    toggleEnabled.mutate({ id: skill.id, enabled: Boolean(checked) })
                  }
                />
                <Button
                  size="icon-xs"
                  variant="ghost"
                  aria-label="删除 Skill"
                  disabled={(skill.scope === "admin" && !isAdmin) || remove.isPending}
                  title={
                    skill.scope === "admin"
                      ? isAdmin
                        ? "删除共享 Skill（对所有用户生效）"
                        : "共享 Skill 由管理员维护"
                      : "删除"
                  }
                  data-testid="skill-delete"
                  onClick={() => {
                    // Deleting a skill removes what a run depends on, and there is no
                    // undo; asking first is the difference between a slip and a loss.
                    toast.warning(`删除 Skill「${skill.name}」？`, {
                      description: "已挂载到现有会话里的副本不会被自动移除。",
                      action: { label: "删除", onClick: () => remove.mutate(skill.id) },
                    });
                  }}
                >
                  <Trash />
                </Button>
              </ItemActions>
            </Item>
          ))
        )}
      </div>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-lg">
          <form
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (!name.trim() || !description.trim()) {
                toast.error("请填写名称和描述");
                return;
              }
              create.mutate();
            }}
          >
            <DialogHeader>
              <DialogTitle>新建 Skill</DialogTitle>
              <DialogDescription>
                写清楚「什么时候用、怎么做、注意什么」。描述会出现在 agent 的技能清单里，请写短。
              </DialogDescription>
            </DialogHeader>

            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="skill-name">名称</FieldLabel>
                <Input
                  id="skill-name"
                  data-testid="skill-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="例如：release-notes"
                  autoFocus
                  required
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="skill-description">描述</FieldLabel>
                <Input
                  id="skill-description"
                  data-testid="skill-description"
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  placeholder="一句话说明它能做什么"
                  required
                />
              </Field>
              {isAdmin ? (
                <Field>
                  <div className="flex items-center justify-between gap-3">
                    <FieldLabel htmlFor="skill-shared">共享给所有用户</FieldLabel>
                    <Switch
                      id="skill-shared"
                      data-testid="skill-shared"
                      checked={shared}
                      onCheckedChange={(checked) => setShared(Boolean(checked))}
                    />
                  </div>
                  <FieldDescription>
                    共享 Skill 会出现在每个人的 Skill 列表里，只有管理员能修改或删除。关闭时只创建你自己的私有 Skill。
                  </FieldDescription>
                </Field>
              ) : null}
              <Field>
                <FieldLabel htmlFor="skill-body">正文（SKILL.md）</FieldLabel>
                <Textarea
                  id="skill-body"
                  data-testid="skill-body"
                  value={body}
                  onChange={(event) => setBody(event.target.value)}
                  rows={8}
                  className="font-mono text-xs"
                  placeholder={"# 发布说明\n\n1. 收集上一个 tag 之后的提交\n2. 按类型分组…"}
                />
                <FieldDescription>留空则只创建一个占位 Skill，稍后再补正文。</FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="skill-assets">附加文件</FieldLabel>
                <Input
                  id="skill-assets"
                  data-testid="skill-assets"
                  type="file"
                  multiple
                  className="h-8 py-1 text-xs"
                  onChange={(event) => {
                    const picked = Array.from(event.target.files ?? []);
                    void Promise.all(
                      picked.map(async (file) => ({
                        // A bundle is text: a skill that needs a binary asset
                        // goes through the API instead of the browser.
                        path: file.name.replace(/\\/g, "/"),
                        content: await file.text(),
                      })),
                    ).then(setAssets);
                  }}
                />
                <FieldDescription>
                  可选。除 SKILL.md 之外的脚本和参考文件会一起挂进沙箱；需要子目录请用 API 上传。
                </FieldDescription>
              </Field>
            </FieldGroup>

            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setDialogOpen(false)}>
                取消
              </Button>
              <Button type="submit" data-testid="skill-submit" disabled={create.isPending}>
                {create.isPending ? <Spinner /> : null}
                创建
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>导入 Skill</DialogTitle>
            <DialogDescription>
              从仓库克隆，或上传一个 zip。两种方式都把其中每个含{" "}
              <span className="font-mono">SKILL.md</span> 的目录各存成一个 Skill，只取该目录下的文件。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <label htmlFor="skill-import-url" className="text-xs font-medium">
                仓库地址
              </label>
              <Input
                id="skill-import-url"
                data-testid="skill-import-url"
                value={importUrl}
                onChange={(event) => setImportUrl(event.target.value)}
                placeholder="https://github.com/org/skills"
                className="font-mono text-xs"
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="skill-import-ref" className="text-xs font-medium">
                分支或标签（可选）
              </label>
              <Input
                id="skill-import-ref"
                data-testid="skill-import-ref"
                value={importRef}
                onChange={(event) => setImportRef(event.target.value)}
                placeholder="main"
                className="font-mono text-xs"
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="skill-import-zip" className="text-xs font-medium">
                或上传 zip
              </label>
              <Input
                id="skill-import-zip"
                data-testid="skill-import-zip"
                type="file"
                accept=".zip,application/zip"
                className="h-8 py-1 text-xs"
                onChange={(event) => setImportZip(event.target.files?.[0] ?? null)}
              />
              <p className="text-[11px] text-muted-foreground">
                最大 8 MB。zip 里每个含 SKILL.md 的目录各存成一个 Skill；路径穿越的条目会被整体拒绝。
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => setImportOpen(false)}
              disabled={importMutation.isPending || zipMutation.isPending}
            >
              取消
            </Button>
            <Button
              type="button"
              variant="outline"
              data-testid="skill-import-zip-submit"
              disabled={importZip === null || zipMutation.isPending}
              onClick={() => {
                if (importZip) zipMutation.mutate(importZip);
              }}
            >
              {zipMutation.isPending ? "上传中…" : "上传 zip"}
            </Button>
            <Button
              type="button"
              data-testid="skill-import-submit"
              disabled={importMutation.isPending || importUrl.trim() === ""}
              onClick={() =>
                importMutation.mutate({ url: importUrl.trim(), ref: importRef.trim() })
              }
            >
              {importMutation.isPending ? "导入中…" : "从仓库导入"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
