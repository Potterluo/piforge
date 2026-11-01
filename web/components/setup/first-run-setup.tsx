"use client";

import * as React from "react";
import Link from "next/link";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertTriangleIcon, ServerCogIcon } from "lucide-react";
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
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { useProviders, useSetup } from "@/lib/session/queries";
import type { Provider } from "@/lib/types";
import { cn } from "@/lib/utils";

import { hasUsableModel } from "@/components/session/no-model-bar";

/**
 * First-run setup, shown where a send would otherwise fail.
 *
 * QA-16's fresh volume had an account and no provider: every send answered 400
 * `no_model`, and the only explanation was a toast that vanished while the
 * message stayed in the composer. This panel is the missing step, on the home
 * page and above the composer:
 *
 *   - it names what is actually missing (nothing configured / disabled / no key
 *     / no models), not just "error";
 *   - it links to 模型与凭据 for the self-service path;
 *   - an admin gets a one-click shared provider, which is the only way a normal
 *     user can be fixed without touching their own settings. The endpoint and
 *     model are prefilled only when the deployment documented them through
 *     PIFORGE_BOOTSTRAP_PROVIDER; the key is always typed here and never
 *     returned by the API.
 *
 * Whether the panel is needed comes from GET /v1/setup, with the provider list
 * as a fallback for a page that mounted before that request settled.
 */
export function FirstRunSetup({ className }: { className?: string }) {
  const setup = useSetup();
  const providers = useProviders();
  const queryClient = useQueryClient();
  const [dialogOpen, setDialogOpen] = React.useState(false);

  const known = setup.data !== undefined || providers.data !== undefined;
  const needs = setup.data
    ? setup.data.needs_provider
    : providers.data
      ? !hasUsableModel(providers.data.providers)
      : false;
  if (!known || !needs) return null;

  const isAdmin = setup.data?.is_admin ?? false;
  const sharedAvailable = setup.data?.shared_provider_available ?? false;
  const list = providers.data?.providers ?? [];
  const missing = missingReason(list);

  return (
    <section
      data-testid="first-run-panel"
      data-needs-provider="1"
      aria-labelledby="first-run-title"
      className={cn(
        "flex flex-col gap-3 rounded-lg border border-amber-500/40 bg-amber-500/[0.06] p-3 text-xs",
        className,
      )}
    >
      <div className="flex items-start gap-2">
        <AlertTriangleIcon className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden="true" />
        <div className="min-w-0">
          <h2 id="first-run-title" data-testid="first-run-title" className="text-sm font-medium">
            先配置一个模型服务
          </h2>
          <p className="mt-0.5 text-muted-foreground">
            这个账号还没有可用的模型，现在发送消息会被拒绝（<code className="font-mono">400 no_model</code>
            ）。{missing}
          </p>
          {/* The deployment names a fallback provider that does not exist
              (PIFORGE_DEFAULT_PROVIDER=volc with no volc). The acceptance pass
              only found this out when the first send failed; the panel says it
              up front, naming the value to change. */}
          {setup.data?.default_provider_missing && setup.data.default_provider ? (
            <p className="mt-1 text-amber-700 dark:text-amber-300" data-testid="setup-default-provider-missing">
              默认服务商「{setup.data.default_provider}」不存在，请添加或更改默认值。
            </p>
          ) : null}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2 pl-6">
        <Button size="sm" variant="outline" data-testid="first-run-settings-link" render={<Link href="/settings/providers" />}>
          打开模型与凭据
        </Button>
        {isAdmin ? (
          <Button size="sm" data-testid="first-run-add-shared" onClick={() => setDialogOpen(true)}>
            <ServerCogIcon />
            添加共享模型服务
          </Button>
        ) : (
          <span className="text-muted-foreground">
            需要管理员在部署级别添加共享模型服务，或者你也可以在「模型与凭据」里加一个自己的。
          </span>
        )}
      </div>

      {isAdmin ? (
        <p className="pl-6 text-muted-foreground">
          共享模型服务会被这个部署的所有账号继承。{sharedAvailable ? "部署里已经有一个共享服务；如果它仍不可用，请检查它的密钥。" : "现在还没有可用的共享服务。"}
        </p>
      ) : null}

      <SharedProviderDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        defaults={setup.data?.bootstrap}
        onCreated={() => {
          // The panel is driven by both queries: invalidating them makes it
          // disappear in place, with no reload, which is the whole point of a
          // one-click path.
          void queryClient.invalidateQueries({ queryKey: ["setup"] });
          void queryClient.invalidateQueries({ queryKey: ["providers"] });
          void queryClient.invalidateQueries({ queryKey: ["admin-providers"] });
        }}
      />
    </section>
  );
}

function missingReason(providers: Provider[]): string {
  if (providers.length === 0) return "这个部署和这个账号都还没有配置任何模型服务。";
  if (providers.every((provider) => !provider.enabled)) return "已有的模型服务都被停用了。";
  if (providers.every((provider) => provider.enabled && !provider.has_key)) {
    return "已有的模型服务缺少 API Key。";
  }
  if (providers.every((provider) => provider.models.length === 0)) {
    return "已有的模型服务还没有配置模型。";
  }
  return "已有的模型服务不完整（需要启用、有密钥、并至少配置一个模型）。";
}

/**
 * The admin's one-click path: a shared provider every account inherits.
 *
 * It uses the ordinary admin provider endpoint (an upsert by name), so adding
 * here is the same operation as adding one on the settings page — no second
 * code path whose validation could drift.
 */
function SharedProviderDialog({
  open,
  onOpenChange,
  defaults,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaults?: { name: string; base_url?: string; model: string };
  onCreated: () => void;
}) {
  const [name, setName] = React.useState(defaults?.name ?? "shared");
  const [baseUrl, setBaseUrl] = React.useState(defaults?.base_url ?? "");
  const [model, setModel] = React.useState(defaults?.model ?? "");
  const [apiKey, setApiKey] = React.useState("");

  // The dialog may mount before /v1/setup resolves, so the documented defaults
  // are applied when they arrive unless the user already typed something. The
  // comparison is on the values, not on the object: a refetched `/v1/setup`
  // hands over an equal-looking object, and re-seeding on that would overwrite
  // what the user typed with the deployment's defaults.
  const defaultsKey = defaults ? `${defaults.name}\u0000${defaults.base_url ?? ""}\u0000${defaults.model}` : "";
  const [appliedKey, setAppliedKey] = React.useState(defaultsKey);
  if (defaults && defaultsKey !== appliedKey) {
    setAppliedKey(defaultsKey);
    setName((current) => (current === "shared" ? defaults.name : current));
    setBaseUrl((current) => (current === "" ? defaults.base_url ?? "" : current));
    setModel((current) => (current === "" ? defaults.model : current));
  }

  const create = useMutation({
    mutationFn: () =>
      api.admin.shareProvider({
        name: name.trim(),
        api: "openai-compat",
        base_url: baseUrl.trim() || undefined,
        models: model.trim() ? [{ id: model.trim(), name: model.trim() }] : [],
        api_key: apiKey.trim(),
      }),
    onSuccess: () => {
      toast.success("共享模型服务已添加", { description: "这个部署的所有账号都可以使用它。" });
      setApiKey("");
      onCreated();
      onOpenChange(false);
    },
    onError: (error) => {
      toast.error("添加失败", { description: humanizeError(error) });
    },
  });

  const ready = name.trim() !== "" && model.trim() !== "" && apiKey.trim() !== "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md" data-testid="first-run-dialog">
        <DialogHeader>
          <DialogTitle>添加共享模型服务</DialogTitle>
          <DialogDescription>
            所有账号都会继承它。密钥在服务端加密保存，之后不会再显示。
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="first-run-name">名称</FieldLabel>
            <Input
              id="first-run-name"
              data-testid="first-run-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              autoComplete="off"
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="first-run-base-url">Base URL</FieldLabel>
            <Input
              id="first-run-base-url"
              data-testid="first-run-base-url"
              value={baseUrl}
              onChange={(event) => setBaseUrl(event.target.value)}
              placeholder="https://api.example.com/v1"
              autoComplete="off"
            />
            <FieldDescription>可以留空，使用服务商的默认端点。</FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="first-run-model">模型 id</FieldLabel>
            <Input
              id="first-run-model"
              data-testid="first-run-model"
              value={model}
              onChange={(event) => setModel(event.target.value)}
              placeholder="gpt-4o-mini"
              autoComplete="off"
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="first-run-api-key">API Key</FieldLabel>
            <Input
              id="first-run-api-key"
              data-testid="first-run-api-key"
              type="password"
              value={apiKey}
              onChange={(event) => setApiKey(event.target.value)}
              autoComplete="new-password"
            />
            <FieldDescription>使用 openai-compat 协议；密钥只在此处输入一次。</FieldDescription>
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button data-testid="first-run-submit" disabled={!ready || create.isPending} onClick={() => create.mutate()}>
            {create.isPending ? <Spinner /> : null}
            添加并启用
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
