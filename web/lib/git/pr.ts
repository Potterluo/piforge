/**
 * Wording for the pull-request dialog (FR-12.6).
 *
 * The server returns `step` in the error details precisely so the UI can label
 * a failure with where it happened. Keeping the mapping here (not inline in the
 * component) means a test can assert the labels without rendering a dialog, and
 * an unknown step still reads as a sentence rather than "undefined".
 */
import type { PRFile } from "@/lib/types";

const STEP_LABELS: Record<string, string> = {
  config: "第 1 步 · 配置",
  branch: "检查分支",
  form: "检查表单",
  base: "检查基准分支",
  push: "第 2 步 · 推送",
  create: "第 3 步 · 创建 PR",
};

/** Human label for the step a failure happened in. */
export function stepLabel(step: unknown): string {
  if (typeof step === "string" && STEP_LABELS[step]) return STEP_LABELS[step];
  return "出错的步骤";
}

/** "3 个文件 · +12 −4" — the summary above the file list. */
export function fileSummary(files: PRFile[] | undefined, additions: number, deletions: number): string {
  const count = files?.length ?? 0;
  if (count === 0) return "没有文件改动";
  return `${count} 个文件 · +${additions} −${deletions}`;
}

/** The status word the file list renders next to a path. */
export function fileStatusWord(status: string): string {
  switch (status) {
    case "added":
      return "新增";
    case "deleted":
      return "删除";
    case "renamed":
      return "重命名";
    default:
      return "修改";
  }
}

/** What kind of host the config points at. */
export function forgeLabel(kind: string | undefined): string {
  return kind === "gitlab" ? "GitLab" : "GitHub / Gitea";
}

/** Where the token lives, in one sentence, for the config form's hint. */
export function tokenHint(hasToken: boolean): string {
  return hasToken
    ? "已保存 token（不会回显）。留空表示继续使用它。"
    : "token 只保存在服务端的加密存储里，之后不会再显示。";
}
