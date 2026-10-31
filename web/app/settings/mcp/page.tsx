"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Boxes, Plus, Trash } from "lucide-react";

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
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { STDIO_UNAVAILABLE_HEADLINE, hasUnstagedServers, unavailableExplanation } from "@/lib/mcp";
import { formatRelative } from "@/lib/session/format";
import { qk } from "@/lib/session/queries";

const TRANSPORTS = [
  { value: "stdio", label: "stdio（本地进程）" },
  { value: "http", label: "http（远程端点）" },
];

const EXPOSURES = [
  { value: "codemode", label: "codemode（按需代码调用）" },
  { value: "deferred", label: "deferred（延迟加载）" },
  { value: "direct", label: "direct（直接暴露工具）" },
  { value: "hidden", label: "hidden（隐藏）" },
];

export default function McpPage() {
  const queryClient = useQueryClient();
  const servers = useQuery({ queryKey: qk.mcp, queryFn: () => api.mcp.list() });
  const [dialogOpen, setDialogOpen] = useState(false);

  const [name, setName] = useState("");
  const [transport, setTransport] = useState("stdio");
  const [command, setCommand] = useState("");
  const [argsText, setArgsText] = useState("");
  const [cwd, setCwd] = useState("");
  const [url, setUrl] = useState("");
  const [exposure, setExposure] = useState("codemode");
  const [secrets, setSecrets] = useState("");

  const reset = () => {
    setName("");
    setTransport("stdio");
    setCommand("");
    setArgsText("");
    setCwd("");
    setUrl("");
    setExposure("codemode");
    setSecrets("");
  };

  const create = useMutation({
    mutationFn: () =>
      api.mcp.create({
        scope: "user",
        name: name.trim(),
        transport: transport === "http" ? "http" : "stdio",
        command: transport === "stdio" ? command.trim() || undefined : undefined,
        args:
          transport === "stdio"
            ? argsText
                .split("\n")
                .map((line) => line.trim())
                .filter(Boolean)
            : undefined,
        cwd: transport === "stdio" ? cwd.trim() || undefined : undefined,
        url: transport === "http" ? url.trim() || undefined : undefined,
        exposure,
        enabled: true,
        secrets: secrets.trim() || undefined,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.mcp });
      setDialogOpen(false);
      reset();
      toast.success("MCP server 已添加");
    },
    onError: (error) => {
      toast.error("添加失败", {
        description: humanizeError(error),
      });
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.mcp.remove(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qk.mcp });
      toast.success("MCP server 已删除");
    },
    onError: (error) => {
      toast.error("删除失败", {
        description: humanizeError(error),
      });
    },
  });

  const list = servers.data?.servers ?? [];
  // Stage 4: a stdio server cannot be handed to an agent that runs inside the
  // sandbox, and the deployment says so per server instead of leaving the row
  // looking functional (docs/EXECUTION-BOUNDARY.md §10.1 item 8).
  const unavailable = servers.data?.unavailable;
  const showIsolationNotice = hasUnstagedServers(servers.data?.agent_isolated, unavailable);

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <header className="flex flex-wrap items-start gap-3 border-b border-border p-4">
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className="text-base font-medium">MCP 中心</h1>
          <p className="max-w-2xl text-xs text-muted-foreground">
            MCP server 给 agent 提供额外工具。凭据用 AES-GCM 加密存库，接口只能写、不能读回；密钥来自
            PIFORGE_SECRET_KEY 或数据目录的 secret.key。agent 在隔离沙箱里运行时，http server 由控制面代理
            出口并带可吊销的每会话 token，stdio server 不会被装进沙箱（原因见下方提示）。
          </p>
        </div>
        <Button size="sm" className="ml-auto" onClick={() => setDialogOpen(true)}>
          <Plus />
          添加 MCP
        </Button>
      </header>

      <div className="flex flex-col gap-2 p-4">
        {showIsolationNotice ? (
          <div
            data-testid="mcp-isolation-notice"
            className="rounded-md border border-border bg-muted/40 p-3 text-xs text-muted-foreground"
          >
            <p className="font-medium text-foreground">{STDIO_UNAVAILABLE_HEADLINE}</p>
            <p className="mt-1">
              这个部署的 agent 跑在每个会话自己的沙箱容器里；stdio server 的凭据必须写进沙箱环境，
              而沙箱里没有可供控制面注入凭据的出口，所以下面标记的 stdio server 不会被装进沙箱——
              它仍然能用，只是这个会话的 agent 拿不到这些工具。改用 http（远程端点）：控制面代理出口，
              沙箱里只有可吊销的每会话 token，真实凭据不出控制面。
            </p>
          </div>
        ) : null}
        {servers.isPending ? (
          <>
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </>
        ) : servers.isError ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            MCP 列表加载失败
            <Button size="sm" variant="outline" onClick={() => void servers.refetch()}>
              重试
            </Button>
          </div>
        ) : list.length === 0 ? (
          <Empty className="border border-dashed border-border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Boxes />
              </EmptyMedia>
              <EmptyTitle>还没有 MCP server</EmptyTitle>
              <EmptyDescription>
                目前没有任何 MCP server 连接。你可以添加一个本地 stdio server 或一个远程 http 端点。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" onClick={() => setDialogOpen(true)}>
                <Plus />
                添加 MCP
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          list.map((server) => {
            // One sentence per affected server: a banner alone does not tell a
            // user with three servers which one the agent did not get.
            const explanation = unavailableExplanation(server, unavailable);
            return (
            <Item key={server.id} variant="outline">
              <ItemMedia variant="icon">
                <Boxes />
              </ItemMedia>
              <ItemContent className="min-w-0 gap-1">
                <div className="flex flex-wrap items-center gap-2">
                  <ItemTitle>{server.name}</ItemTitle>
                  <Badge variant="outline">{server.transport}</Badge>
                  <Badge variant="secondary">{server.exposure}</Badge>
                  {server.scope === "admin" ? <Badge>共享</Badge> : <Badge variant="outline">私有</Badge>}
                  {server.has_secrets ? <Badge variant="outline">有凭据</Badge> : null}
                  {server.enabled ? null : <Badge variant="destructive">已停用</Badge>}
                  {explanation ? (
                    <Badge variant="destructive" data-testid="mcp-unavailable-badge" data-name={server.name}>
                      隔离模式下不会加载
                    </Badge>
                  ) : null}
                </div>
                <ItemDescription className="font-mono text-xs">
                  {server.transport === "stdio"
                    ? [server.command, ...(server.args ?? [])].filter(Boolean).join(" ")
                    : server.url || "未配置端点"}
                </ItemDescription>
                {explanation ? (
                  <ItemDescription
                    data-testid="mcp-unavailable-reason"
                    data-name={server.name}
                    className="text-[11px] text-destructive"
                  >
                    {explanation}
                  </ItemDescription>
                ) : null}
                <ItemDescription className="text-[11px]">
                  {formatRelative(server.created_at)}
                </ItemDescription>
              </ItemContent>
              <ItemActions>
                <Button
                  size="icon-xs"
                  variant="ghost"
                  aria-label="删除 MCP server"
                  disabled={server.scope === "admin" || remove.isPending}
                  title={server.scope === "admin" ? "共享 MCP 由管理员维护" : "删除"}
                  onClick={() => remove.mutate(server.id)}
                >
                  <Trash />
                </Button>
              </ItemActions>
            </Item>
            );
          })
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
              if (transport === "stdio" && !command.trim()) {
                toast.error("stdio server 需要启动命令");
                return;
              }
              if (transport === "http" && !url.trim()) {
                toast.error("http server 需要 URL");
                return;
              }
              create.mutate();
            }}
          >
            <DialogHeader>
              <DialogTitle>添加 MCP server</DialogTitle>
              <DialogDescription>
                添加后对新会话生效；已运行的会话需要重开才会看到新工具。
              </DialogDescription>
            </DialogHeader>

            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="mcp-name">名称</FieldLabel>
                <Input
                  id="mcp-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="例如：filesystem"
                  autoFocus
                  required
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="mcp-transport">传输方式</FieldLabel>
                <Select
                  items={TRANSPORTS}
                  value={transport}
                  onValueChange={(value) => setTransport(String(value))}
                >
                  <SelectTrigger id="mcp-transport" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {TRANSPORTS.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>

              {transport === "stdio" ? (
                <>
                  <Field>
                    <FieldLabel htmlFor="mcp-command">命令</FieldLabel>
                    <Input
                      id="mcp-command"
                      value={command}
                      onChange={(event) => setCommand(event.target.value)}
                      placeholder="npx"
                      className="font-mono"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="mcp-args">参数</FieldLabel>
                    <Textarea
                      id="mcp-args"
                      value={argsText}
                      onChange={(event) => setArgsText(event.target.value)}
                      rows={3}
                      className="font-mono text-xs"
                      placeholder={"-y\n@modelcontextprotocol/server-filesystem\n/workspace"}
                    />
                    <FieldDescription>每行一个参数。</FieldDescription>
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="mcp-cwd">工作目录</FieldLabel>
                    <Input
                      id="mcp-cwd"
                      value={cwd}
                      onChange={(event) => setCwd(event.target.value)}
                      placeholder="留空则使用会话工作区"
                      className="font-mono"
                    />
                  </Field>
                </>
              ) : (
                <Field>
                  <FieldLabel htmlFor="mcp-url">URL</FieldLabel>
                  <Input
                    id="mcp-url"
                    value={url}
                    onChange={(event) => setUrl(event.target.value)}
                    placeholder="https://mcp.example.com/mcp"
                    className="font-mono"
                  />
                </Field>
              )}

              <Field>
                <FieldLabel htmlFor="mcp-exposure">暴露方式</FieldLabel>
                <Select
                  items={EXPOSURES}
                  value={exposure}
                  onValueChange={(value) => setExposure(String(value))}
                >
                  <SelectTrigger id="mcp-exposure" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {EXPOSURES.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>

              <Field>
                <FieldLabel htmlFor="mcp-secrets">凭据</FieldLabel>
                <Textarea
                  id="mcp-secrets"
                  value={secrets}
                  onChange={(event) => setSecrets(event.target.value)}
                  rows={2}
                  className="font-mono text-xs"
                  placeholder={"API_TOKEN=…"}
                />
                <FieldDescription>KEY=VALUE，每行一条；加密保存且不回显。</FieldDescription>
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
