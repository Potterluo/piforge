"use client";

import Link from "next/link";
import { AlertTriangle } from "lucide-react";

import type { Provider } from "@/lib/types";

/**
 * Is there a model this account can actually send to?
 *
 * A provider that is disabled, has no key, or lists no models cannot answer a
 * prompt. Counting those as "available" is how a fresh account ended up with a
 * send button that could only ever return 400 `no_model` — the failure was
 * real, but it lived in a toast that disappeared.
 */
export function hasUsableModel(providers: Provider[] | undefined): boolean {
  if (!providers) return false;
  return providers.some(
    (provider) =>
      provider.enabled && provider.has_key && (provider.models?.length ?? 0) > 0,
  );
}

/**
 * The composer's persistent "no model" state.
 *
 * It sits above the input and stays there: before the first send it explains
 * the configuration gap, and after a `no_model` refusal it carries the server's
 * own words next to the text the user does not have to retype.
 */
export function NoModelBar({ error }: { error?: string }) {
  return (
    <div
      data-testid="no-model-bar"
      role="status"
      aria-live="polite"
      className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400"
    >
      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
      <div className="min-w-0">
        <p className="font-medium">这个账号还没有可用模型</p>
        {error ? (
          <p data-testid="no-model-error" className="mt-0.5 break-words opacity-90">
            {error}
          </p>
        ) : null}
        <p className="mt-0.5 text-muted-foreground">
          请先在{" "}
          <Link
            data-testid="no-model-settings-link"
            href="/settings/providers"
            className="underline underline-offset-2 hover:text-foreground"
          >
            模型与凭据
          </Link>{" "}
          里配置一个可用的模型服务；配置好之后回到这里重新发送。
        </p>
      </div>
    </div>
  );
}
