"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { KeyRound, Plus, Trash } from "lucide-react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api, type CreateProviderInput } from "@/lib/api/client";
import { qk, useProviders } from "@/lib/session/queries";

const API_KINDS = [
  { value: "anthropic-messages", label: "anthropic-messages" },
  { value: "openai-responses", label: "openai-responses" },
  { value: "openai-compat", label: "openai-compat（兼容接口）" },
];

function parseModels(raw: string): { id: string; name?: string }[] {
  return raw
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .map((line) => {
      const [id, ...rest] = line.split(/\s+/);
      const name = rest.join(" ").trim();
      return name ? { id, name } : { id };
    });
}

export default function ProvidersPage() {
  const queryClient = useQueryClient();
  const providers = useProviders();
  const [dialogOpen, setDialogOpen] = useState(false);

  const [name, setName] = useState("");
  const [apiKind, setApiKind] = useState("openai-compat");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [modelsText, setModelsText] = useState("");

  const create = useMutation({
    mutationFn: (input: CreateProviderInput) => api.providers.create(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.providers });
      setDialogOpen(false);
      setName("");
      setApiKind("openai-compat");
      setBaseUrl("");
      setApiKey("");
      setModelsText("");
      toast.success("Provider 已添加");
    },
    onError: (error) => {
      toast.error("添加失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.providers.remove(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.providers });
      toast.success("Provider 已删除");
    },
    onError: (error) => {
      toast.error("删除失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    },
  });

  const test = useMutation({
    mutationFn: (id: string) => api.providers.test(id),
    onSuccess: (result) => {
      if (result.ok) toast.success("连通性正常", { description: result.message });
      else toast.error("连通性测试失败", { description: result.message });
    },
    onError: (error) => {
      toast.error("连通性测试失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    },
  });

  const list = providers.data?.providers ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <header className="flex flex-wrap items-start gap-3 border-b border-border p-4">
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className="text-base font-medium">模型与凭据</h1>
          <p className="max-w-2xl text-xs text-muted-foreground">
            继承项由管理员统一配置，所有人可用；私有项只属于你，key 加密保存，任何时候都不会回显。
          </p>
        </div>
        <Button size="sm" className="ml-auto" onClick={() => setDialogOpen(true)}>
          <Plus />
          添加 Provider
        </Button>
      </header>

      <div className="flex flex-col gap-2 p-4">
        {providers.isPending ? (
          <>
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </>
        ) : providers.isError ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            Provider 列表加载失败
            <Button size="sm" variant="outline" onClick={() => void providers.refetch()}>
              重试
            </Button>
          </div>
        ) : list.length === 0 ? (
          <Empty className="border border-dashed border-border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <KeyRound />
              </EmptyMedia>
              <EmptyTitle>还没有可用的模型</EmptyTitle>
              <EmptyDescription>
                添加一个 provider 并填入自己的 API key，或者等管理员配置共享模型。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" onClick={() => setDialogOpen(true)}>
                <Plus />
                添加 Provider
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          list.map((provider) => (
            <Item key={provider.id} variant="outline">
              <ItemMedia variant="icon">
                <KeyRound />
              </ItemMedia>
              <ItemContent className="min-w-0 gap-1">
                <div className="flex flex-wrap items-center gap-2">
                  <ItemTitle>{provider.name}</ItemTitle>
                  <Badge variant={provider.source === "custom" ? "secondary" : "outline"}>
                    {provider.source === "custom" ? "自定义" : "继承"}
                  </Badge>
                  <Badge variant="outline">{provider.api}</Badge>
                  {provider.has_key ? (
                    <Badge variant="outline">已配置 key</Badge>
                  ) : (
                    <Badge variant="destructive">缺少 key</Badge>
                  )}
                  {provider.enabled ? null : <Badge variant="outline">已停用</Badge>}
                </div>
                <ItemDescription className="font-mono text-xs">
                  {provider.base_url || "默认端点"}
                  {provider.models.length > 0
                    ? ` · ${provider.models
                        .slice(0, 4)
                        .map((model) => model.id)
                        .join(", ")}${provider.models.length > 4 ? ` +${provider.models.length - 4}` : ""}`
                    : " · 未声明模型"}
                </ItemDescription>
              </ItemContent>
              <ItemActions>
                <Button
                  size="xs"
                  variant="outline"
                  disabled={test.isPending}
                  onClick={() => test.mutate(provider.id)}
                >
                  {test.isPending && test.variables === provider.id ? <Spinner /> : null}
                  测试
                </Button>
                {provider.source === "custom" ? (
                  <AlertDialog>
                    <AlertDialogTrigger
                      render={
                        <Button size="icon-xs" variant="ghost" aria-label="删除 Provider" />
                      }
                    >
                      <Trash />
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>删除「{provider.name}」？</AlertDialogTitle>
                        <AlertDialogDescription>
                          用这个 provider 的会话将无法再调用它的模型，已保存的 key 会一并删除。
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>取消</AlertDialogCancel>
                        <AlertDialogAction onClick={() => remove.mutate(provider.id)}>
                          删除
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                ) : null}
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
              if (!name.trim()) {
                toast.error("请填写名称");
                return;
              }
              create.mutate({
                name: name.trim(),
                api: apiKind,
                base_url: baseUrl.trim() || undefined,
                api_key: apiKey.trim() || undefined,
                models: parseModels(modelsText),
              });
            }}
          >
            <DialogHeader>
              <DialogTitle>添加 Provider</DialogTitle>
              <DialogDescription>
                key 只提交一次，服务端加密存储；之后任何接口都不会返回明文。
              </DialogDescription>
            </DialogHeader>

            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="provider-name">名称</FieldLabel>
                <Input
                  id="provider-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="例如：my-openai"
                  autoFocus
                  required
                />
                <FieldDescription>会话里的模型引用会是「名称/模型 id」。</FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="provider-api">接口协议</FieldLabel>
                <Select
                  items={API_KINDS}
                  value={apiKind}
                  onValueChange={(value) => setApiKind(String(value))}
                >
                  <SelectTrigger id="provider-api" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {API_KINDS.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field>
                <FieldLabel htmlFor="provider-base-url">Base URL</FieldLabel>
                <Input
                  id="provider-base-url"
                  value={baseUrl}
                  onChange={(event) => setBaseUrl(event.target.value)}
                  placeholder="https://api.example.com/v1"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="provider-api-key">API Key</FieldLabel>
                <Input
                  id="provider-api-key"
                  type="password"
                  value={apiKey}
                  onChange={(event) => setApiKey(event.target.value)}
                  placeholder="sk-…"
                  autoComplete="off"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="provider-models">模型</FieldLabel>
                <Textarea
                  id="provider-models"
                  value={modelsText}
                  onChange={(event) => setModelsText(event.target.value)}
                  rows={3}
                  placeholder={"gpt-4o\nclaude-sonnet-4  Claude Sonnet 4"}
                  className="font-mono text-xs"
                />
                <FieldDescription>每行一个模型 id，可在后面空格跟显示名。</FieldDescription>
              </Field>
            </FieldGroup>

            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setDialogOpen(false)}>
                取消
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending ? <Spinner /> : null}
                添加
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
