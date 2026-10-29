/**
 * What to tell the user about a failed model call.
 *
 * The transcript used to end every failure with the same line — "check this
 * provider's address and key" — which is right for a credential problem and wrong
 * for everything else. A `model_not_found` (a typo'd id) sent the user to a
 * settings page where nothing was wrong, and a 502 from a gateway that Pi then
 * retried successfully is not an auth outage at all.
 *
 * The message is the provider's own text, so classification is by the words
 * providers actually use, with the status code where one is present.
 */
export type ModelErrorKind = "model_not_found" | "credentials" | "rate_limit" | "upstream" | "other";

const MODEL_NOT_FOUND =
  /model_not_found|model not found|requested model does not exist|does not exist|unknown model|invalid model|no such model/i;
const CREDENTIALS =
  /401|403|unauthorized|forbidden|invalid api key|invalid_api_key|incorrect api key|authentication|no key configured|has no api key|is not available|provider_unavailable|invalid_token|expired token/i;
const RATE_LIMIT = /429|rate limit|rate_limit|quota|insufficient_quota|too many requests/i;
const UPSTREAM = /50[0-4]|upstream_error|bad gateway|service unavailable|timeout|timed out|econnreset|socket hang up/i;

/** Classifies a provider failure so the UI can give the matching advice. */
export function classifyModelError(error: string): ModelErrorKind {
  const text = error ?? "";
  if (MODEL_NOT_FOUND.test(text)) return "model_not_found";
  if (CREDENTIALS.test(text)) return "credentials";
  if (RATE_LIMIT.test(text)) return "rate_limit";
  if (UPSTREAM.test(text)) return "upstream";
  return "other";
}

/**
 * The one-line advice under a failed turn.
 *
 * It names the settings page the user actually has (模型与凭据), because "Settings →
 * Models" is not a screen in this product.
 */
export function modelErrorAdvice(error: string): string {
  switch (classifyModelError(error)) {
    case "model_not_found":
      return "这个模型 id 在服务商那边不存在：检查拼写，或在「设置 → 模型与凭据」里把它加上，然后重新发送。";
    case "credentials":
      return "检查「设置 → 模型与凭据」里这个服务商的地址与密钥，然后重新发送。";
    case "rate_limit":
      return "服务商限流或额度用完了：稍等一会儿再发送，或换一个模型。";
    case "upstream":
      return "服务商暂时不可用（上游 5xx 或超时）：稍后重新发送；一直失败再检查它的地址。";
    default:
      return "可以先重试；一直失败的话，检查「设置 → 模型与凭据」里这个服务商的配置。";
  }
}

/** The heading above the failure: a missing model is not a failed call. */
export function modelErrorTitle(error: string): string {
  return classifyModelError(error) === "model_not_found" ? "这个模型用不了" : "模型调用失败";
}
