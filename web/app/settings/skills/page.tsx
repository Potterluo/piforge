"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Plus, Sparkles, Trash } from "lucide-react";

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
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api/client";
import { formatRelative } from "@/lib/session/format";
import { qk } from "@/lib/session/queries";

export default function SkillsPage() {
  const queryClient = useQueryClient();
  const skills = useQuery({ queryKey: qk.skills, queryFn: () => api.skills.list() });
  const [dialogOpen, setDialogOpen] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [body, setBody] = useState("");

  const create = useMutation({
    mutationFn: () =>
      api.skills.create({
        name: name.trim(),
        description: description.trim(),
        body: body.trim() || undefined,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.skills });
      setDialogOpen(false);
      setName("");
      setDescription("");
      setBody("");
      toast.success("Skill 已创建");
    },
    onError: (error) => {
      toast.error("创建 Skill 失败", {
        description: error instanceof Error ? error.message : "未知错误",
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
        description: error instanceof Error ? error.message : "未知错误",
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
        <Button size="sm" className="ml-auto" onClick={() => setDialogOpen(true)}>
          <Plus />
          新建 Skill
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
            <Item key={skill.id} variant="outline">
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
                <Button
                  size="icon-xs"
                  variant="ghost"
                  aria-label="删除 Skill"
                  disabled={skill.scope === "admin" || remove.isPending}
                  title={skill.scope === "admin" ? "共享 Skill 由管理员维护" : "删除"}
                  onClick={() => remove.mutate(skill.id)}
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
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  placeholder="一句话说明它能做什么"
                  required
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="skill-body">正文（SKILL.md）</FieldLabel>
                <Textarea
                  id="skill-body"
                  value={body}
                  onChange={(event) => setBody(event.target.value)}
                  rows={8}
                  className="font-mono text-xs"
                  placeholder={"# 发布说明\n\n1. 收集上一个 tag 之后的提交\n2. 按类型分组…"}
                />
                <FieldDescription>留空则只创建一个占位 Skill，稍后再补正文。</FieldDescription>
              </Field>
            </FieldGroup>

            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setDialogOpen(false)}>
                取消
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending ? <Spinner /> : null}
                创建
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
