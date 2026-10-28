"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { KeyRound, Pencil, Plus, Trash } from "lucide-react";

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
import type { Provider } from "@/lib/types";
import { qk, useCurrentUser, useProviders } from "@/lib/session/queries";

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
  const me = useCurrentUser();
  const isAdmin = me.data?.user.role === "admin";
  const [dialogOpen, setDialogOpen] = useState(false);

  const [name, setName] = useState("");
  const [apiKind, setApiKind] = useState("openai-compat");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [modelsText, setModelsText] = useState("");

  // Edit state: a model list or an endpoint changes far more often than a key,
  // so editing never asks for the key again — an empty field means "leave it".
  const [editing, setEditing] = useState<Provider | null>(null);
  const [editingModels, setEditingModels] = useState<{ id: string; name?: string }[]>([]);
  const [editApi, setEditApi] = useState("openai-compat");
  const [editBaseUrl, setEditBaseUrl] = useState("");
  const [editModels, setEditModels] = useState("");
  const [editKey, setEditKey] = useState("");

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

  const update = useMutation({
    mutationFn: (input: { id: string; api: string; base_url: string; models: string; api_key: string }) =>
      api.providers.update(input.id, {
        api: input.api,
        base_url: input.base_url,
        models: mergeModelNames(parseModels(input.models), editingModels),
        api_key: input.api_key.trim() || undefined,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.providers });
      setEditing(null);
      setEditKey("");
      toast.success("Provider 已更新");
    },
    onError: (error) => {
      toast.error("更新失败", {
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
        <Button
          size="sm"
          className="ml-auto"
          data-testid="provider-add"
          onClick={() => setDialogOpen(true)}
        >
          <Plus />
          添加 Provider
        </Button>
      </header>

      {isAdmin ? (
        <SharedProviders
          onChanged={() => void queryClient.invalidateQueries({ queryKey: qk.providers })}
        />
      ) : null}

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
              <Button size="sm" data-testid="provider-add-empty" onClick={() => setDialogOpen(true)}>
                <Plus />
                添加 Provider
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          list.map((provider) => (
            <Item key={provider.id} variant="outline" data-testid="provider-item" data-name={provider.name}>
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
                  <Button
                    size="xs"
                    variant="ghost"
                    data-testid="provider-edit"
                    onClick={() => {
                      setEditing(provider);
                      setEditApi(provider.api);
                      setEditBaseUrl(provider.base_url ?? "");
                      setEditModels(
                        provider.models
                          // The textarea edits ids; a display name is kept so saving
                          // an unrelated change does not erase it.
                          .map((model) => (model.name ? `${model.id} ${model.name}` : model.id))
                          .join("\n"),
                      );
                      setEditingModels(provider.models);
                      setEditKey("");
                    }}
                  >
                    <Pencil />
                    编辑
                  </Button>
                ) : null}
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
                  data-testid="provider-name"
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
                  data-testid="provider-api-key"
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
              <Button type="submit" data-testid="provider-submit" disabled={create.isPending}>
                {create.isPending ? <Spinner /> : null}
                添加
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(open) => (open ? null : setEditing(null))}>
        <DialogContent className="sm:max-w-lg">
          <form
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (!editing) return;
              update.mutate({
                id: editing.id,
                api: editApi,
                base_url: editBaseUrl.trim(),
                models: editModels,
                api_key: editKey,
              });
            }}
          >
            <DialogHeader>
              <DialogTitle>编辑「{editing?.name}」</DialogTitle>
              <DialogDescription>
                模型列表按行填写，可以是任意 id——Pi 只要求端点说 OpenAI 或 Anthropic 的协议。
              </DialogDescription>
            </DialogHeader>

            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="edit-provider-api">接口协议</FieldLabel>
                <Select items={API_KINDS} value={editApi} onValueChange={(value) => setEditApi(String(value))}>
                  <SelectTrigger id="edit-provider-api" className="w-full">
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
                <FieldLabel htmlFor="edit-provider-base">Base URL</FieldLabel>
                <Input
                  id="edit-provider-base"
                  value={editBaseUrl}
                  onChange={(event) => setEditBaseUrl(event.target.value)}
                  placeholder="https://api.example.com/v1"
                  className="font-mono text-xs"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="edit-provider-models">模型</FieldLabel>
                <Textarea
                  id="edit-provider-models"
                  data-testid="edit-provider-models"
                  value={editModels}
                  onChange={(event) => setEditModels(event.target.value)}
                  rows={4}
                  placeholder={"gpt-4o\nclaude-sonnet-4"}
                  className="font-mono text-xs"
                />
                <FieldDescription>每行一个模型 id，第一个会作为默认值。</FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="edit-provider-key">新的 API Key（可选）</FieldLabel>
                <Input
                  id="edit-provider-key"
                  type="password"
                  value={editKey}
                  onChange={(event) => setEditKey(event.target.value)}
                  placeholder="留空表示沿用已保存的 key"
                />
              </Field>
            </FieldGroup>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setEditing(null)}>
                取消
              </Button>
              <Button type="submit" disabled={update.isPending}>
                保存
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
/**
 * Shared providers (PRD FR-16.2): the deployment's own model accounts. An admin
 * configures one here and every user inherits it; a user's own provider of the
 * same name shadows it, which is the "your machine is yours" illusion in one
 * line.
 */
function SharedProviders({ onChanged }: { onChanged: () => void }) {
  const queryClient = useQueryClient();
  const shared = useQuery({
    queryKey: ["admin", "providers"],
    queryFn: () => api.admin.providers(),
  });
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [apiKind, setApiKind] = useState("openai-compat");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [modelsText, setModelsText] = useState("");

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin", "providers"] });
    onChanged();
  };

  const share = useMutation({
    mutationFn: () =>
      api.admin.shareProvider({
        name: name.trim(),
        api: apiKind,
        base_url: baseUrl.trim() || undefined,
        api_key: apiKey.trim() || undefined,
        models: parseModels(modelsText),
      }),
    onSuccess: () => {
      toast.success("已共享给所有用户");
      setOpen(false);
      setName("");
      setBaseUrl("");
      setApiKey("");
      setModelsText("");
      refresh();
    },
    onError: (error) =>
      toast.error("共享失败", {
        description: error instanceof Error ? error.message : "未知错误",
      }),
  });

  const unshare = useMutation({
    mutationFn: (id: string) => api.admin.unshareProvider(id),
    onSuccess: () => {
      toast.success("已取消共享");
      refresh();
    },
    onError: (error) =>
      toast.error("取消失败", {
        description: error instanceof Error ? error.message : "未知错误",
      }),
  });

  const list = shared.data?.providers ?? [];

  return (
    <section className="flex flex-col gap-2 border-b border-border p-4" data-testid="shared-providers">
      <div className="flex flex-wrap items-start gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h2 className="text-sm font-medium">共享模型（全员可用）</h2>
          <p className="max-w-2xl text-xs text-muted-foreground">
            这里配置的 provider 会出现在每个用户的列表里，标记为「继承」。用户自己建的同名 provider 会覆盖它。
          </p>
        </div>
        <Button size="sm" variant="outline" data-testid="shared-provider-add" onClick={() => setOpen(true)}>
          <Plus />
          共享一个
        </Button>
      </div>

      {list.length === 0 ? (
        <p className="text-xs text-muted-foreground">还没有共享 provider，每个用户得自己配 key。</p>
      ) : (
        <div className="flex flex-col gap-2">
          {list.map((provider) => (
            <Item key={provider.id} variant="outline" data-testid="shared-provider-item">
              <ItemMedia variant="icon">
                <KeyRound />
              </ItemMedia>
              <ItemContent className="min-w-0 gap-1">
                <div className="flex flex-wrap items-center gap-2">
                  <ItemTitle>{provider.name}</ItemTitle>
                  <Badge variant="outline">{provider.api}</Badge>
                  {provider.has_key ? (
                    <Badge variant="outline">已配置 key</Badge>
                  ) : (
                    <Badge variant="destructive">缺少 key</Badge>
                  )}
                </div>
                <ItemDescription className="font-mono text-xs">
                  {provider.base_url || "默认端点"}
                  {provider.models.length
                    ? ` · ${provider.models.map((model) => model.id).join(", ")}`
                    : " · 未声明模型"}
                </ItemDescription>
              </ItemContent>
              <ItemActions>
                <Button
                  size="icon-xs"
                  variant="ghost"
                  aria-label="取消共享"
                  disabled={unshare.isPending}
                  onClick={() => unshare.mutate(provider.id)}
                >
                  <Trash />
                </Button>
              </ItemActions>
            </Item>
          ))}
        </div>
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          <form
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (!name.trim()) {
                toast.error("请填写名称");
                return;
              }
              share.mutate();
            }}
          >
            <DialogHeader>
              <DialogTitle>共享一个 provider</DialogTitle>
              <DialogDescription>
                所有用户都会看到它；同名时用户自己的配置优先。key 同样加密保存，不会回显。
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="shared-name">名称</FieldLabel>
                <Input
                  id="shared-name"
                  data-testid="shared-provider-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="shared-anthropic"
                  autoFocus
                  required
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="shared-api">接口协议</FieldLabel>
                <Select items={API_KINDS} value={apiKind} onValueChange={(value) => setApiKind(String(value))}>
                  <SelectTrigger id="shared-api" className="w-full">
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
                <FieldLabel htmlFor="shared-base">Base URL</FieldLabel>
                <Input
                  id="shared-base"
                  value={baseUrl}
                  onChange={(event) => setBaseUrl(event.target.value)}
                  placeholder="https://api.example.com/v1"
                  className="font-mono text-xs"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="shared-key">API Key</FieldLabel>
                <Input
                  id="shared-key"
                  type="password"
                  value={apiKey}
                  onChange={(event) => setApiKey(event.target.value)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="shared-models">模型</FieldLabel>
                <Textarea
                  id="shared-models"
                  value={modelsText}
                  onChange={(event) => setModelsText(event.target.value)}
                  rows={3}
                  placeholder={"claude-sonnet-4\ngpt-4o"}
                  className="font-mono text-xs"
                />
                <FieldDescription>每行一个模型 id。</FieldDescription>
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                取消
              </Button>
              <Button type="submit" data-testid="shared-provider-submit" disabled={share.isPending}>
                共享
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </section>
  );
}
/**
 * parseModels keeps a display name when one follows the id on the same line, and
 * mergeModelNames puts back the names the textarea cannot show. Without this,
 * editing a base URL silently blanked every model's display name.
 */
function mergeModelNames(
  next: { id: string; name?: string }[],
  previous: { id: string; name?: string }[],
): { id: string; name?: string }[] {
  const known = new Map(previous.map((model) => [model.id, model.name]));
  return next.map((model) => ({
    id: model.id,
    name: model.name || known.get(model.id) || undefined,
  }));
}