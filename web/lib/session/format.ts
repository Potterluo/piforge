import type { SandboxState, SessionKind, SessionStatus } from "@/lib/types";

/** `1234` → `1.2k`, `1_200_000` → `1.2M`. Used in context/stats chrome. */
export function formatTokens(n: number | undefined | null): string {
  if (!n || n < 0) return "0";
  if (n < 1_000) return String(n);
  if (n < 1_000_000) return `${(n / 1_000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** Sub-cent costs keep two significant digits instead of rounding to $0.00. */
export function formatCost(usd: number | undefined | null): string {
  if (!usd) return "$0.00";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}

/** Tool durations: below a second we keep milliseconds, above a minute minutes. */
export function formatDuration(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return "";
  if (ms < 1_000) return `${Math.max(0, Math.round(ms))}ms`;
  if (ms < 60_000) return `${(ms / 1_000).toFixed(ms < 10_000 ? 1 : 0)}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1_000);
  return `${minutes}m ${seconds.toString().padStart(2, "0")}s`;
}

/** Elapsed whole seconds since an ISO timestamp, clamped at zero. */
export function elapsedSeconds(since: string | number, now = Date.now()): number {
  const started = typeof since === "number" ? since : Date.parse(since);
  if (Number.isNaN(started)) return 0;
  return Math.max(0, Math.floor((now - started) / 1_000));
}

/** Compact relative time for session lists; falls back to a date after a week. */
export function formatRelative(ts: string | undefined | null, now = Date.now()): string {
  if (!ts) return "—";
  const then = Date.parse(ts);
  if (Number.isNaN(then)) return "—";
  const diff = now - then;
  if (diff < 60_000) return "刚刚";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  if (diff < 7 * 86_400_000) return `${Math.floor(diff / 86_400_000)} 天前`;
  return new Date(then).toLocaleDateString("zh-CN");
}

/** Wall-clock time shown in message hover chrome. */
export function formatClock(ts: string | undefined | null): string {
  if (!ts) return "";
  const then = Date.parse(ts);
  if (Number.isNaN(then)) return "";
  return new Date(then).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
  });
}

const STATUS_LABELS: Record<SessionStatus, string> = {
  active: "活跃",
  running: "运行中",
  idle: "空闲",
  paused: "已暂停",
  settled: "已完成",
  failed: "失败",
  archived: "已归档",
};

export function sessionStatusLabel(status: SessionStatus | undefined): string {
  return status ? (STATUS_LABELS[status] ?? status) : "未知";
}

/** Data attribute consumed by status dots; keeps colour out of the components. */
export function sessionStatusTone(
  status: SessionStatus | undefined,
): "running" | "idle" | "paused" | "failed" {
  switch (status) {
    case "running":
    case "active":
      return "running";
    case "paused":
    case "archived":
      return "paused";
    case "failed":
      return "failed";
    default:
      return "idle";
  }
}

const SANDBOX_LABELS: Record<SandboxState, string> = {
  CREATING: "创建中",
  STARTING: "启动中",
  READY: "就绪",
  BUSY: "忙碌",
  IDLE: "空闲",
  PAUSED: "已暂停",
  TERMINATED: "已终止",
  ERROR: "异常",
  MISSING: "缺失",
};

export function sandboxLabel(state: SandboxState | undefined): string {
  return state ? (SANDBOX_LABELS[state] ?? state) : "未知";
}

export function sessionKindLabel(kind: SessionKind | undefined): string {
  switch (kind) {
    case "branch":
      return "分支";
    case "sidecar":
      return "Sidecar";
    default:
      return "主会话";
  }
}

/** Short model id: `anthropic/claude-sonnet-4` → `claude-sonnet-4`. */
export function shortModel(modelRef: string | undefined | null): string {
  if (!modelRef) return "默认模型";
  const i = modelRef.indexOf("/");
  return i >= 0 ? modelRef.slice(i + 1) : modelRef;
}

/** First non-empty line of a tool argument, trimmed for a collapsed trace row. */
export function truncate(text: string, max = 96): string {
  const oneLine = text.replace(/\s+/g, " ").trim();
  return oneLine.length > max ? `${oneLine.slice(0, max - 1)}…` : oneLine;
}
