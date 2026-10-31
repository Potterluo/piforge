/**
 * The one place a session's model is turned into header text.
 *
 * The acceptance pass read `跟随默认（unknown）` in a session header: Pi answers
 * `get_state` with provider/model "unknown" when it never resolved a model, and
 * that placeholder was formatted as if it were a model name. `unknown` is an
 * internal state — it must never reach a person.
 *
 * The rules here are the product's wording, and they are also what keeps the
 * three sources of truth in order:
 *
 *   - the session's pinned reference, when it resolves, is what runs;
 *   - otherwise the control plane's resolved model (the deployment default, or
 *     the fallback the next send would pick) is named as 默认模型（…）;
 *   - with nothing usable at all the header says 未配置.
 */
import { shortModel } from "./format";

const UNKNOWN = "unknown";

/**
 * Strip Pi's `unknown` placeholder. A reference like `unknown/unknown` (or
 * `provider/unknown`) describes nothing, so it becomes null instead of being
 * shortened into a plausible-looking model name.
 */
export function cleanModelRef(ref: string | null | undefined): string | null {
  const value = (ref ?? "").trim();
  if (!value) return null;
  const slash = value.indexOf("/");
  if (slash < 0) return value.toLowerCase() === UNKNOWN ? null : value;
  const provider = value.slice(0, slash).trim();
  const model = value.slice(slash + 1).trim();
  if (!model || model.toLowerCase() === UNKNOWN) return null;
  if (!provider || provider.toLowerCase() === UNKNOWN) return model;
  return `${provider}/${model}`;
}

export interface SessionModelSources {
  /** `session.model_ref`: what the user picked. Empty means "follow default". */
  stored?: string | null;
  /** `status.resolved_model_ref`: what the control plane would use right now. */
  resolved?: string | null;
  /** `status.model_unavailable`: the server says nothing can answer. */
  unavailable?: boolean;
  /** `hasUsableModel(providers)`: false means there is nothing to send to. */
  hasUsableModel?: boolean;
}

/** The header's model text. Never returns `unknown` or an empty string. */
export function sessionModelLabel({
  stored,
  resolved,
  unavailable,
  hasUsableModel,
}: SessionModelSources): string {
  // With nothing usable anywhere the honest label is a state, not a model name:
  // a session pinned to a deleted provider still carries its old reference. The
  // server's answer wins over a provider list the page may still be caching.
  if (unavailable || hasUsableModel === false) return "未配置";
  const pinned = cleanModelRef(stored);
  const effective = cleanModelRef(resolved);
  // The control plane's resolution wins: when the pinned provider is gone it is
  // the fallback the run will actually use, and naming the dead reference would
  // describe a model nobody is running.
  if (effective) {
    return pinned ? shortModel(effective) : `默认模型（${shortModel(effective)}）`;
  }
  if (pinned) return shortModel(pinned);
  return "默认模型";
}
