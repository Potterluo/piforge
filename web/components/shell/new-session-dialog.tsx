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
import { Field, FieldContent, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import { useCreateSession, useProviders } from "@/lib/session/queries";

const THINKING_LEVELS = [
  { value: "default", label: "跟随默认" },
  { value: "off", label: "不推理" },
  { value: "low", label: "低" },
  { value: "medium", label: "中" },
  { value: "high", label: "高" },
];

/**
 * Starts a session inside a project (PRD FR-1.1).
 *
 * A session picks its model up front but can switch later from the composer,
 * so nothing here is a commitment: it is just the starting point.
 */
export function NewSessionDialog({
  open,
  onOpenChange,
  projectId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string | null;
}) {
  const router = useRouter();
  const providers = useProviders();
  const [title, setTitle] = useState("");
  const [model, setModel] = useState("auto");
  const [thinking, setThinking] = useState("default");
  // Scratch space (PRD FR-1.10): kept out of the sidebar and swept after a few
  // hours, so "just try something" does not leave a permanent session behind.
  const [ephemeral, setEphemeral] = useState(false);

  const create = useCreateSession(projectId ?? "");

  const models = useMemo(() => {
    const options = [{ value: "auto", label: "默认模型" }];
    for (const provider of providers.data?.providers ?? []) {
      if (!provider.enabled) continue;
      for (const spec of provider.models ?? []) {
        options.push({
          value: `${provider.name}/${spec.id}`,
          label: `${provider.name} / ${spec.name || spec.id}`,
        });
      }
    }
    return options;
  }, [providers.data]);

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!projectId) {
      toast.error("请先选择一个项目");
      return;
    }
    try {
      const result = await create.mutateAsync({
        title: title.trim() || undefined,
        model_ref: model === "auto" ? undefined : model,
        thinking_level: thinking === "default" ? undefined : thinking,
        ephemeral,
      });
      toast.success(ephemeral ? "临时会话已创建（数小时后自动清理）" : "会话已创建");
      setTitle("");
      setModel("auto");
      setThinking("default");
      setEphemeral(false);
      onOpenChange(false);
      router.push(`/p/${projectId}/s/${result.session.id}`);
    } catch (error) {
      toast.error("创建会话失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>新会话</DialogTitle>
            <DialogDescription>
              会话会在项目沙箱里创建一个独立 worktree，可以随时分叉出并行分支。
            </DialogDescription>
          </DialogHeader>

          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="session-title">标题</FieldLabel>
              <Input
                id="session-title"
                data-testid="session-title"
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                placeholder="例如：修缓存淘汰竞态"
                autoFocus
              />
              <FieldDescription>留空则用第一条消息自动命名。</FieldDescription>
            </Field>
            <Field orientation="horizontal">
              <Switch
                id="session-ephemeral"
                data-testid="session-ephemeral"
                checked={ephemeral}
                onCheckedChange={setEphemeral}
              />
              <FieldContent>
                <FieldLabel htmlFor="session-ephemeral">临时会话</FieldLabel>
                <FieldDescription>
                  不进侧边栏，闲置几小时后自动清理——适合随口问一句。
                </FieldDescription>
              </FieldContent>
            </Field>
            <Field>
              <FieldLabel htmlFor="session-model">模型</FieldLabel>
              <Select
                items={models}
                value={model}
                onValueChange={(value) => setModel(String(value))}
              >
                <SelectTrigger id="session-model" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {models.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {providers.isError ? (
                <FieldDescription className="text-destructive">
                  模型列表加载失败，将使用服务端默认模型。
                </FieldDescription>
              ) : null}
            </Field>
            <Field>
              <FieldLabel htmlFor="session-thinking">推理强度</FieldLabel>
              <Select
                items={THINKING_LEVELS}
                value={thinking}
                onValueChange={(value) => setThinking(String(value))}
              >
                <SelectTrigger id="session-thinking" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINKING_LEVELS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </FieldGroup>

          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" data-testid="session-submit" disabled={create.isPending || !projectId}>
              {create.isPending ? <Spinner /> : null}
              开始
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
