"use client";

import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CopyIcon, KeyRound, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { formatRelative } from "@/lib/session/format";

/**
 * Personal API keys (PRD FR-13.9).
 *
 * The token is shown exactly once, at creation. That is not a UI nicety: nothing on
 * the server can read it back, so a page that promised to show it later would be
 * lying.
 */
export function ApiKeysPage() {
  const queryClient = useQueryClient();
  const keys = useQuery({ queryKey: ["api-keys"], queryFn: () => api.apiKeys.list() });
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [token, setToken] = React.useState<string | null>(null);

  const create = useMutation({
    mutationFn: () => api.apiKeys.create(name.trim()),
    onSuccess: (data) => {
      setToken(data.token);
      setName("");
      void queryClient.invalidateQueries({ queryKey: ["api-keys"] });
    },
    onError: (error) =>
      toast.error("创建失败", {
        description: humanizeError(error),
      }),
  });

  const revoke = useMutation({
    mutationFn: (id: string) => api.apiKeys.revoke(id),
    onSuccess: () => {
      toast.success("已吊销");
      void queryClient.invalidateQueries({ queryKey: ["api-keys"] });
    },
    onError: (error) =>
      toast.error("吊销失败", {
        description: humanizeError(error),
      }),
  });

  const copy = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      toast.success("已复制");
    } catch {
      toast.error("浏览器不允许写入剪贴板，请手动选中复制");
    }
  };

  return (
    <div
      className="mx-auto flex min-h-0 w-full max-w-3xl flex-1 flex-col gap-4 overflow-y-auto p-6"
      data-testid="api-keys-page"
    >
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="flex items-center gap-2 text-lg font-medium">
            <KeyRound className="size-4" />
            API Key
          </h1>
          <p className="text-sm text-muted-foreground">
            给脚本用的令牌。请求时带上
            <code className="mx-1 rounded bg-muted px-1 py-0.5 text-[11px]">
              Authorization: Bearer pfk_…
            </code>
            ，就能像网页一样调用这套 API。
          </p>
        </div>
        <Button size="sm" data-testid="api-key-add" onClick={() => setOpen(true)}>
          <Plus />
          新建
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">我的令牌</CardTitle>
          <CardDescription>吊销后立刻失效；记录会保留，便于追查泄露。</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          {(keys.data?.api_keys ?? []).length === 0 ? (
            <p className="text-xs text-muted-foreground" data-testid="api-keys-empty">
              还没有令牌。新建一个，然后把明文复制走——它只出现这一次。
            </p>
          ) : (
            (keys.data?.api_keys ?? []).map((key) => (
              <div
                key={key.id}
                data-testid="api-key-row"
                data-name={key.name}
                className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2"
              >
                <div className="flex min-w-0 flex-col">
                  <span className="truncate text-sm">{key.name}</span>
                  <span className="truncate font-mono text-[11px] text-muted-foreground">
                    {key.prefix}…
                    {key.last_used_at ? ` · 最近使用 ${formatRelative(key.last_used_at)}` : " · 还没用过"}
                  </span>
                </div>
                {key.revoked ? (
                  <Badge variant="outline" className="text-[10px]">
                    已吊销
                  </Badge>
                ) : (
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    aria-label="吊销"
                    data-testid="api-key-revoke"
                    onClick={() => revoke.mutate(key.id)}
                  >
                    <Trash2 />
                  </Button>
                )}
              </div>
            ))
          )}
        </CardContent>
      </Card>

      <Dialog
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) setToken(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{token ? "复制这个令牌" : "新建 API Key"}</DialogTitle>
            <DialogDescription>
              {token
                ? "关掉这个对话框就再也看不到了。丢了只能重新建一个。"
                : "起个名字，方便以后认出它是给谁用的。"}
            </DialogDescription>
          </DialogHeader>

          {token ? (
            <div className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <Input readOnly value={token} data-testid="api-key-token" className="font-mono text-[11px]" />
                <Button size="sm" variant="outline" onClick={() => void copy(token)}>
                  <CopyIcon />
                  复制
                </Button>
              </div>
              <p className="text-[11px] text-muted-foreground">
                例：curl -H &quot;Authorization: Bearer $TOKEN&quot; /v1/sessions
              </p>
            </div>
          ) : (
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="api-key-name">名字</FieldLabel>
                <Input
                  id="api-key-name"
                  data-testid="api-key-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="例如：ci-scripts"
                  autoFocus
                />
                <FieldDescription>只是一个标签，不影响权限。</FieldDescription>
              </Field>
            </FieldGroup>
          )}

          <DialogFooter>
            {token ? (
              <Button size="sm" onClick={() => setOpen(false)}>
                我已保存
              </Button>
            ) : (
              <Button
                size="sm"
                data-testid="api-key-submit"
                disabled={!name.trim() || create.isPending}
                onClick={() => create.mutate()}
              >
                创建
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <p className="text-xs text-muted-foreground">
        令牌以你的身份工作：能读的、能改的和你在网页里一样。不要贴进公开的仓库或日志。
      </p>
    </div>
  );
}