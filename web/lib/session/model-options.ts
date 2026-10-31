import type { Provider } from "@/lib/types";

/**
 * The models a picker may offer, and the providers it must not hide.
 *
 * The new-session dialog used to list every enabled provider, including
 * `shared-anthropic`, which ships without a key on a fresh deployment. Picking
 * it started a session whose agent could not answer: ~30s of retries and a raw
 * 502 in the transcript. A provider that cannot answer is not a choice, but
 * hiding it entirely would make "why is my provider missing?" unanswerable —
 * so its models are listed disabled with the reason, and the caller renders a
 * link to the page where the key goes.
 *
 * Exported separately from the components so the rule can be tested without a
 * browser: every picker (new session, composer) must agree about what is
 * selectable.
 */
export interface ModelChoice {
  value: string;
  label: string;
  /** False only when the provider has no key; the row is still shown. */
  selectable: boolean;
  /** The reason a row cannot be picked, e.g. 缺少 API Key. */
  note?: string;
}

export interface BlockedProvider {
  name: string;
  reason: string;
}

export interface ModelChoices {
  /** Selectable models, plus disabled rows that explain why they are not. */
  options: ModelChoice[];
  /** Providers that exist but cannot answer, for the hint under the picker. */
  blocked: BlockedProvider[];
}

const NO_KEY = "缺少 API Key";

/** Build the picker rows from the account's (already merged) provider list. */
export function buildModelChoices(providers: Provider[] | undefined): ModelChoices {
  const options: ModelChoice[] = [];
  const blocked: BlockedProvider[] = [];

  for (const provider of providers ?? []) {
    if (!provider.enabled) continue;
    const models = provider.models ?? [];
    const hasKey = Boolean(provider.has_key);

    if (!hasKey) {
      blocked.push({ name: provider.name, reason: NO_KEY });
      for (const spec of models) {
        options.push({
          value: `${provider.name}/${spec.id}`,
          label: `${provider.name} / ${spec.name || spec.id}`,
          selectable: false,
          note: NO_KEY,
        });
      }
      continue;
    }

    for (const spec of models) {
      options.push({
        value: `${provider.name}/${spec.id}`,
        label: `${provider.name} / ${spec.name || spec.id}`,
        selectable: true,
      });
    }
  }

  return { options, blocked };
}
