/** Relative timestamps for the git history list ("3 分钟前"). Pure, testable. */

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * Formats a commit timestamp as a short relative string.
 *
 * Anything older than a month falls back to an absolute `YYYY-MM-DD` date,
 * which is what `git log --date=short` would have shown anyway.
 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return iso;

  const delta = now - then;
  if (delta < 0) return "刚刚";
  if (delta < MINUTE) return "刚刚";
  if (delta < HOUR) return `${Math.floor(delta / MINUTE)} 分钟前`;
  if (delta < DAY) return `${Math.floor(delta / HOUR)} 小时前`;
  if (delta < 30 * DAY) return `${Math.floor(delta / DAY)} 天前`;

  return new Date(then).toISOString().slice(0, 10);
}

/** Absolute local `YYYY-MM-DD HH:mm` for tooltips. */
export function absoluteTime(iso: string): string {
  const parsed = Date.parse(iso);
  if (Number.isNaN(parsed)) return iso;
  const date = new Date(parsed);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
