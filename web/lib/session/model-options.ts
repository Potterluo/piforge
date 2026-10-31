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

/**
 * The human name of a `provider/model` reference.
 *
 * The model's own display name is used when the provider lists one; the id is
 * the fallback. An unknown provider keeps the raw ref rather than inventing a
 * friendlier name for something the account cannot see.
 */
export function modelRefLabel(ref: string | undefined, providers: Provider[] | undefined): string {
  const trimmed = (ref ?? "").trim();
  if (!trimmed) return "";
  const slash = trimmed.indexOf("/");
  if (slash <= 0) return trimmed;
  const providerName = trimmed.slice(0, slash);
  const modelID = trimmed.slice(slash + 1);
  const provider = (providers ?? []).find((entry) => entry.name === providerName);
  const model = provider?.models?.find((entry) => entry.id === modelID);
  return `${providerName} / ${model?.name || modelID}`;
}

export interface DefaultModelCopy {
  /** Empty when nothing usable is configured. */
  label: string;
  /** The sentence under the model picker. */
  hint: string;
  /** The sentence about providers that cannot be chosen (empty when none). */
  blockedHint: string;
}

/**
 * What 「默认模型」 actually means for this account.
 *
 * The placeholder row used to sit directly above 「shared-anthropic 缺少 API
 * Key，不能选择」, which read as "the default is broken" — while the default was
 * a working model the whole time. A picker that lists an unusable provider must
 * also say which model the default resolves to, and that the warning does not
 * touch it.
 */
export function defaultModelCopy(
  defaultRef: string | undefined,
  providers: Provider[] | undefined,
  blocked: BlockedProvider[],
): DefaultModelCopy {
  const label = modelRefLabel(defaultRef, providers);
  const names = blocked.map((provider) => provider.name).join("、");
  if (!label) {
    return {
      label: "",
      hint: "还没有可用的默认模型：先给一个 provider 填 API Key，否则发送会被拒绝。",
      blockedHint: names ? `${names} 缺少 API Key，不能选择。` : "",
    };
  }
  return {
    label,
    hint: `默认模型会使用 ${label}；选它就按这个模型运行。`,
    blockedHint: names
      ? `${names} 缺少 API Key，不能选择；这不影响默认模型。`
      : "",
  };
}
