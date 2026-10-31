/**
 * Chinese wording for the errors an operator actually sees.
 *
 * The API is written in English and stays that way: its `code` is the contract,
 * its `message` is the diagnostic. This module is the one place that turns that
 * pair into something a person reading the Chinese UI should see. Nothing here
 * rewrites the wire error — `ApiError.message` keeps the raw text, which is what
 * server logs and the browser console still carry.
 *
 * The table is keyed on the error code first. Within a code the server's message
 * is matched, because `invalid_argument` covers a dozen different sentences and
 * a single string per code would be a worse lie than the English. An unmapped
 * English message is deliberately not passed through: it becomes a generic
 * Chinese sentence, and the raw text is logged. An unmapped message that already
 * contains Chinese is passed through untouched — the backend writes some of
 * those, and they are the best wording available.
 */
import { ApiError } from "@/lib/types";

interface MessageRule {
  code: string;
  pattern: RegExp;
  render: (match: RegExpMatchArray) => string;
}

const PROVIDER_MISSING = /^the provider "(.+?)" is not configured/;
const PROVIDER_NO_MODELS = /^the provider "(.+?)" lists no models/;
const PROVIDER_NO_KEY = /^the provider "(.+?)" has no API key/;

const RULES: MessageRule[] = [
  // Auth. These are the first thing a new user can hit.
  {
    code: "unauthenticated",
    pattern: /^invalid email or password$/i,
    render: () => "邮箱或密码不正确",
  },
  {
    code: "unauthenticated",
    pattern: /^authentication required$/i,
    render: () => "登录已过期，请重新登录",
  },
  {
    code: "unauthenticated",
    pattern: /^invalid api key/i,
    render: () => "这个 API Key 无效或已被吊销",
  },
  {
    code: "forbidden",
    pattern: /^account is disabled$/i,
    render: () => "账号已被停用，请联系管理员",
  },
  {
    code: "forbidden",
    pattern: /^admin role required$/i,
    render: () => "需要管理员权限",
  },
  {
    code: "forbidden",
    pattern: /^origin not allowed/i,
    render: () => "请求来源不被允许",
  },

  // Model configuration. The menu name must match the UI: 模型与凭据, which is
  // what /settings/providers is called. "Settings → Models" pointed at a page
  // that does not exist.
  {
    code: "no_model",
    pattern: /^no model provider is configured/i,
    render: () => "还没有配置模型服务，请到「设置 → 模型与凭据」添加一个",
  },
  {
    code: "no_model",
    pattern: PROVIDER_MISSING,
    render: (m) => `服务商「${m[1]}」尚未配置，请到「设置 → 模型与凭据」添加`,
  },
  {
    code: "no_model",
    pattern: PROVIDER_NO_MODELS,
    render: (m) => `服务商「${m[1]}」还没有可用模型，请到「设置 → 模型与凭据」添加模型 id`,
  },
  {
    code: "no_model",
    pattern: PROVIDER_NO_KEY,
    render: (m) => `服务商「${m[1]}」还没有 API Key，请到「设置 → 模型与凭据」填写`,
  },
  {
    code: "invalid_argument",
    pattern: /^this provider has no API key yet$/i,
    render: () => "这个服务商还没有 API Key，请先填写后再测试",
  },
  // The gateway's own vocabulary. A run that reaches the gateway with a keyless
  // provider answers `provider_unavailable` (HTTP 400), and the transcript used
  // to print the raw body: `502 {"error":{"code":"provider_unavailable",…}}`.
  {
    code: "provider_unavailable",
    pattern: /^provider "(.+?)" has no key configured$/i,
    render: (m) =>
      `服务商「${m[1]}」还没有配置 API Key，这一轮没有调用模型。请到「设置 → 模型与凭据」填写后重新发送`,
  },
  {
    code: "provider_unavailable",
    pattern: /^provider "(.+?)" is not available$/i,
    render: (m) =>
      `服务商「${m[1]}」不可用（可能已被删除或停用），请到「设置 → 模型与凭据」检查`,
  },
  {
    code: "provider_unavailable",
    pattern: /^stored key cannot be decrypted$/i,
    render: () => "已保存的 key 无法解密，请到「设置 → 模型与凭据」重新填写",
  },
  {
    code: "provider_unavailable",
    pattern: /^provider "(.+?)" has no base URL$/i,
    render: (m) => `服务商「${m[1]}」没有填写地址，请到「设置 → 模型与凭据」补充`,
  },
  {
    code: "provider_unavailable",
    pattern: /.*/,
    render: () => "模型服务不可用：这个服务商没有配置好，请到「设置 → 模型与凭据」检查后重试",
  },
  {
    code: "invalid_argument",
    pattern: /^the stored key cannot be decrypted$/i,
    render: () => "已保存的 key 无法解密，请重新填写",
  },
  {
    code: "internal",
    pattern: /^secret storage is not configured/i,
    render: () => "服务器没有配置密钥存储，请联系管理员",
  },

  // Workspace / sandbox.
  {
    code: "sandbox_unavailable",
    pattern: /^this session has no worktree yet$/i,
    render: () => "这个会话还没有工作区",
  },
  {
    code: "sandbox_unavailable",
    pattern: /^the session has no checkout yet$/i,
    render: () => "这个会话还没有工作区",
  },
  {
    code: "sandbox_unavailable",
    pattern: /^workspace service is not available$/i,
    render: () => "工作区服务不可用，请联系管理员",
  },
  {
    code: "sandbox_unavailable",
    pattern: /^no chrome binary is configured/i,
    render: () => "服务器没有配置浏览器",
  },
  {
    code: "sandbox_unavailable",
    pattern: /^terminal is not configured$/i,
    render: () => "服务器没有配置终端",
  },
  {
    code: "sandbox_unavailable",
    pattern: /^agent runtime is not configured$/i,
    render: () => "服务器没有配置 agent 运行时",
  },

  // Request-shape errors. They are rare in the UI, but when they happen they
  // must not arrive as English.
  {
    code: "invalid_argument",
    pattern: /^name is required$/i,
    render: () => "请填写名称",
  },
  {
    code: "invalid_argument",
    pattern: /^path is required$/i,
    render: () => "缺少文件路径",
  },
  {
    code: "invalid_argument",
    pattern: /^index is required$/i,
    render: () => "缺少 hunk 序号",
  },
  {
    code: "invalid_argument",
    pattern: /^index must not be negative$/i,
    render: () => "hunk 序号不能为负数",
  },
  {
    code: "invalid_argument",
    pattern: /^format must be md or html$/i,
    render: () => "导出格式只能是 md 或 html",
  },
  {
    code: "invalid_argument",
    pattern: /^client_msg_id is required$/i,
    render: () => "请求缺少消息 id",
  },
  {
    code: "invalid_argument",
    pattern: /^client_msg_id is too long$/i,
    render: () => "消息 id 过长",
  },
  {
    code: "invalid_argument",
    pattern: /^text or images are required$/i,
    render: () => "请输入内容或添加图片",
  },
  {
    code: "invalid_argument",
    pattern: /^unknown kind/i,
    render: () => "未知的会话类型",
  },
  {
    code: "invalid_argument",
    pattern: /^which tab\?$/i,
    render: () => "没有指定浏览器标签页",
  },
  {
    code: "invalid_argument",
    pattern: /^a valid email is required$/i,
    render: () => "请填写有效的邮箱",
  },
  {
    code: "invalid_argument",
    pattern: /^the password must be at least/i,
    render: (m) => {
      const digits = /(\d+)/.exec(m.input ?? "");
      return digits ? `密码至少 ${digits[1]} 位` : "密码太短";
    },
  },
  {
    code: "invalid_argument",
    pattern: /^role must be user or admin$/i,
    render: () => "角色只能是普通用户或管理员",
  },
  {
    code: "invalid_argument",
    pattern: /^you cannot disable your own account$/i,
    render: () => "不能停用自己的账号",
  },
  {
    code: "invalid_argument",
    pattern: /^repo_url /i,
    render: () => "仓库地址格式不支持：请填写 https 链接或 git@主机:路径",
  },
  {
    code: "invalid_argument",
    pattern: /^request body is required$/i,
    render: () => "请求内容为空",
  },
  {
    code: "invalid_argument",
    pattern: /^invalid JSON body/i,
    render: () => "请求格式不正确",
  },
  {
    code: "invalid_argument",
    pattern: /^could not read body/i,
    render: () => "无法读取请求内容",
  },
  {
    code: "invalid_argument",
    pattern: /^only a sidecar can be promoted$/i,
    render: () => "只有旁路对话可以提升为分支",
  },
  {
    code: "invalid_argument",
    pattern: /^this sidecar has no parent session$/i,
    render: () => "这个旁路对话没有来源会话",
  },
  {
    code: "conflict",
    pattern: /^at most 3 sidecars per session$/i,
    render: () => "每个会话最多 3 个旁路对话",
  },
  {
    code: "conflict",
    pattern: /^a conflict with an existing record/i,
    render: () => "已经有重名的记录",
  },
  {
    code: "internal",
    pattern: /^internal error$/i,
    render: () => "服务端内部错误，请稍后重试",
  },
];

const CJK = /[\u3400-\u9fff]/;

/** The message exactly as the server sent it, for logs and diagnostics. */
export function rawErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  return "";
}

/**
 * Chinese text for an error the UI is about to render.
 *
 * The raw English is available through `rawErrorMessage` and stays on
 * `ApiError.message`; unmapped English is logged once here rather than shown.
 */
export function humanizeError(error: unknown, fallback = "操作失败，请稍后重试"): string {
  if (error instanceof ApiError) {
    for (const rule of RULES) {
      if (rule.code !== error.code) continue;
      const match = rule.pattern.exec(error.message);
      if (match) {
        if (process.env.NODE_ENV !== "production") {
          console.debug("[piforge] api error", error.code, error.message, "→", rule.render(match));
        }
        return rule.render(match);
      }
    }
    // Some handlers already answer in Chinese ("至少选两个会话才能对比"). Those
    // are better than anything this table could invent.
    if (CJK.test(error.message)) return error.message;
    console.warn("[piforge] unmapped API error", error.code, error.message);
    return fallback;
  }

  if (error instanceof Error) {
    if (/failed to fetch|networkerror|load failed/i.test(error.message)) {
      return "无法连接后端，请检查网络或服务是否在运行";
    }
    if (CJK.test(error.message)) return error.message;
    return fallback;
  }

  if (typeof error === "string" && error) {
    return CJK.test(error) ? error : fallback;
  }
  return fallback;
}

/**
 * A repo URL the product can clone: empty (a blank workspace), an https URL, or
 * the scp-like `git@host:path` form. Kept in sync with the Go handler so the
 * form can reject locally and the API still refuses a bypass.
 */
export function repoUrlError(raw: string): string | null {
  const value = raw.trim();
  if (!value) return null;
  if (/^https:\/\/[^\s/]+(\/[^\s]*)?$/i.test(value)) {
    try {
      const url = new URL(value);
      if (url.hostname && !url.username && !url.password) return null;
    } catch {
      /* fall through to the error */
    }
  }
  if (/^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$/.test(value)) return null;
  // The local sandbox provider clones a bare repository by absolute path; the
  // e2e push/pull tests use one. A relative fragment still fails.
  if (/^(\/|[A-Za-z]:[\\/]|\\\\)/.test(value) && !/[\t\r\n]/.test(value)) return null;
  return "仓库地址格式不支持：请填写 https 链接或 git@主机:路径";
}
