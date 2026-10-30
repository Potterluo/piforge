"use client";

import * as React from "react";
import { AlertTriangle, ExternalLink, GitPullRequest, Settings2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { useGitData } from "@/components/workspace/git-data";
import { useAsync } from "@/components/workspace/use-async";
import api from "@/lib/api/client";
import { fileStatusWord, fileSummary, forgeLabel, stepLabel, tokenHint } from "@/lib/git/pr";
import { ApiError, type CreatePRResult, type PRPreview } from "@/lib/types";

/**
 * 「创建 Pull Request」(FR-12.6): change → test → share, finished in the app.
 *
 * The dialog is a preview first, not a button that pushes on faith. The server
 * computes what would be sent (branch, commits, files), pre-fills the title from
 * the session's first message and the body from the latest commit, and reports
 * when the forge already has a PR for the branch. Only 创建 pushes.
 *
 * Failures are labelled by step (配置 / 推送 / 创建) because the fix for each
 * lives somewhere else: the project's forge config, the remote's state, or the
 * forge's own answer — whose text is shown unchanged.
 */
export function CreatePullRequest({ sessionId }: { sessionId: string }) {
  const git = useGitData();
  const [open, setOpen] = React.useState(false);
  // The success view lives above the preview, not inside it: opening a PR
  // refreshes git state, which reloads the preview, and a result held inside
  // the reloaded body would be replaced by the "already exists" view before
  // the user could read the link.
  const [result, setResult] = React.useState<CreatePRResult | null>(null);
  // `revision` is bumped by the git panel after a commit/stage, so reopening
  // the dialog reads the branch as it is now rather than at mount time.
  const preview = useAsync(`pr:${sessionId}:${git.revision}`, () => api.git.prPreview(sessionId), {
    enabled: open && result === null,
  });

  return (
    <>
      <Button
        size="xs"
        variant="outline"
        data-testid="git-create-pr"
        onClick={() => {
          setResult(null);
          setOpen(true);
        }}
      >
        <GitPullRequest />
        创建 Pull Request
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-xl" data-testid="create-pr-dialog">
          <DialogHeader>
            <DialogTitle>创建 Pull Request</DialogTitle>
            <DialogDescription>
              先把这条分支推到远端，再在代码托管平台上开一个 PR。推送失败时不会创建 PR。
            </DialogDescription>
          </DialogHeader>

          {result ? (
            <PRResult result={result} onClose={() => setOpen(false)} />
          ) : preview.loading ? (
            <p
              data-testid="create-pr-loading"
              className="flex items-center gap-2 py-6 text-xs text-muted-foreground"
            >
              <Spinner /> 正在读取这条分支…
            </p>
          ) : preview.error ? (
            <div data-testid="create-pr-preview-error" className="space-y-2">
              <p className="rounded-md bg-destructive/10 p-2 text-xs text-destructive">
                {preview.error}
              </p>
              <DialogFooter>
                <Button variant="ghost" onClick={() => setOpen(false)}>
                  关闭
                </Button>
                <Button variant="outline" onClick={preview.reload}>
                  重试
                </Button>
              </DialogFooter>
            </div>
          ) : preview.data ? (
            <PRBody
              preview={preview.data}
              sessionId={sessionId}
              onClose={() => setOpen(false)}
              onReload={preview.reload}
              onCreated={setResult}
            />
          ) : null}
        </DialogContent>
      </Dialog>
    </>
  );
}

/** The three states of the dialog body: already-open, form, or success. */
function PRBody({
  preview,
  sessionId,
  onClose,
  onReload,
  onCreated,
}: {
  preview: PRPreview;
  sessionId: string;
  onClose: () => void;
  onReload: () => void;
  onCreated: (result: CreatePRResult) => void;
}) {
  if (preview.pull_request) {
    return (
      <div data-testid="create-pr-existing" className="space-y-3">
        <p className="rounded-md bg-muted p-2 text-xs text-muted-foreground">
          这条分支在代码托管平台上已经有一个 Pull Request。再点创建不会产生第二个，所以这里直接给你现有的链接。
        </p>
        <PRLink pr={preview.pull_request} />
        <DialogFooter>
          <Button variant="outline" data-testid="create-pr-close" onClick={onClose}>
            关闭
          </Button>
        </DialogFooter>
      </div>
    );
  }
  // The key resets the form when the branch or its commit count changed while
  // the dialog was open; otherwise the user's edit would silently apply to a
  // different branch.
  return (
    <PRForm
      key={`${preview.branch}:${preview.commit_count}:${preview.base_branch}`}
      preview={preview}
      sessionId={sessionId}
      onClose={onClose}
      onReload={onReload}
      onCreated={onCreated}
    />
  );
}

function PRForm({
  preview,
  sessionId,
  onClose,
  onReload,
  onCreated,
}: {
  preview: PRPreview;
  sessionId: string;
  onClose: () => void;
  onReload: () => void;
  onCreated: (result: CreatePRResult) => void;
}) {
  const git = useGitData();
  const [title, setTitle] = React.useState(preview.title);
  const [body, setBody] = React.useState(preview.body);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<{
    step: string;
    message: string;
    forgeStatus?: number;
  } | null>(null);
  const [configOpen, setConfigOpen] = React.useState(!preview.forge?.has_token);
  const [savedForge, setSavedForge] = React.useState(false);

  const warnings = preview.warnings ?? [];
  const sameBranch = preview.branch === preview.base_branch;
  const nothingToSend = preview.commit_count === 0;
  const blocked = sameBranch || nothingToSend;
  const files = preview.files ?? [];

  const submit = async () => {
    const trimmed = title.trim();
    if (!trimmed) {
      setError({ step: "form", message: "标题不能为空。" });
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const created = await api.git.createPR(sessionId, {
        title: trimmed,
        body,
        base_branch: preview.base_branch,
      });
      git.refresh();
      onCreated(created);
    } catch (cause) {
      setError(describeFailure(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-3">
      <div className="rounded-lg border bg-muted/30 p-2.5 text-xs">
        <div className="flex items-center justify-between gap-2">
          <span data-testid="create-pr-branch" className="font-mono">
            {preview.branch} → {preview.base_branch}
          </span>
          <span data-testid="create-pr-commits" className="text-muted-foreground">
            {preview.commit_count} 个提交
          </span>
        </div>
        <p className="mt-1 text-[11px] text-muted-foreground">
          {preview.remote
            ? preview.ahead > 0
              ? `会先推送 ${preview.ahead} 个提交到 origin`
              : "远端分支已是最新，推送会直接成功"
            : "还没有远端（origin），推送这一步会失败"}
          {preview.dirty ? " · 未提交的改动不会包含在内" : ""}
        </p>
      </div>

      <div>
        <p className="mb-1 text-[11px] font-medium">将要包含的改动</p>
        <p data-testid="create-pr-summary" className="text-[11px] text-muted-foreground">
          {fileSummary(files, preview.additions, preview.deletions)}
        </p>
        <ul className="mt-1 max-h-40 space-y-0.5 overflow-auto rounded-md border p-1.5 text-[11px]">
          {files.slice(0, 8).map((file) => (
            <li key={file.path} data-testid="create-pr-file" className="flex items-center gap-2">
              <span className="w-10 shrink-0 text-muted-foreground">
                {fileStatusWord(file.status)}
              </span>
              <span className="min-w-0 flex-1 truncate font-mono">{file.path}</span>
              <span className="shrink-0 tabular-nums text-muted-foreground">
                +{file.additions} −{file.deletions}
              </span>
            </li>
          ))}
          {files.length === 0 ? (
            <li className="px-1 text-muted-foreground">相对 {preview.base_branch} 没有文件改动</li>
          ) : null}
        </ul>
        {files.length > 8 ? (
          <p className="mt-1 text-[10px] text-muted-foreground">还有 {files.length - 8} 个文件</p>
        ) : null}
      </div>

      {warnings.length > 0 ? (
        <ul
          data-testid="create-pr-warnings"
          className="space-y-1 rounded-md bg-amber-500/10 p-2 text-[11px] text-amber-700 dark:text-amber-400"
        >
          {warnings.map((warning) => (
            <li key={warning} className="flex gap-1.5">
              <AlertTriangle className="mt-0.5 size-3 shrink-0" />
              <span>{warning}</span>
            </li>
          ))}
        </ul>
      ) : null}

      <Field>
        <FieldLabel htmlFor="pr-title">标题</FieldLabel>
        <Input
          id="pr-title"
          data-testid="create-pr-title"
          value={title}
          disabled={busy}
          onChange={(event) => setTitle(event.target.value)}
          placeholder="这次改动做了什么"
        />
      </Field>
      <Field>
        <FieldLabel htmlFor="pr-body">描述</FieldLabel>
        <Textarea
          id="pr-body"
          data-testid="create-pr-body"
          value={body}
          disabled={busy}
          rows={4}
          onChange={(event) => setBody(event.target.value)}
          placeholder="评审需要知道的背景（可留空）"
        />
      </Field>

      <div className="rounded-md border p-2">
        <button
          type="button"
          data-testid="forge-config-toggle"
          className="flex w-full items-center gap-1.5 text-left text-[11px] font-medium"
          onClick={() => setConfigOpen((current) => !current)}
        >
          <Settings2 className="size-3" />
          代码托管平台：{forgeLabel(preview.forge?.kind)}
          <span className="ml-auto font-normal text-muted-foreground">
            {preview.forge?.has_token
              ? `${preview.forge.repo} · 已配置`
              : preview.forge?.repo
                ? `${preview.forge.repo} · 缺少 token`
                : "未配置"}
          </span>
        </button>
        {configOpen ? (
          <ForgeConfigForm
            projectId={preview.project_id}
            initial={preview.forge ?? null}
            onSaved={() => {
              setSavedForge(true);
              setConfigOpen(false);
              // The token changed, so the "already exists" question can now be
              // asked; re-read instead of guessing.
              onReload();
            }}
          />
        ) : null}
        {savedForge ? (
          <p className="mt-1 text-[10px] text-muted-foreground">配置已保存，正在刷新…</p>
        ) : null}
      </div>

      {error ? (
        <div
          data-testid="create-pr-error"
          className="space-y-1 rounded-md bg-destructive/10 p-2 text-[11px] text-destructive"
        >
          <p className="flex items-center gap-1.5 font-medium">
            <AlertTriangle className="size-3" />
            <span data-testid="create-pr-error-step">{stepLabel(error.step)}</span>
            {error.forgeStatus ? (
              <span data-testid="create-pr-forge-status" className="font-mono">
                HTTP {error.forgeStatus}
              </span>
            ) : null}
          </p>
          <p data-testid="create-pr-error-message" className="break-words">
            {error.message}
          </p>
        </div>
      ) : null}

      <DialogFooter>
        <Button variant="ghost" disabled={busy} onClick={onClose}>
          取消
        </Button>
        <Button
          data-testid="create-pr-submit"
          disabled={busy || blocked || !preview.forge?.has_token}
          onClick={() => void submit()}
        >
          {busy ? <Spinner /> : <GitPullRequest />}
          {busy ? "推送并创建…" : "推送并创建 PR"}
        </Button>
      </DialogFooter>
      {blocked ? (
        <p data-testid="create-pr-blocked" className="text-[11px] text-destructive">
          {sameBranch
            ? `当前就在基准分支 ${preview.base_branch} 上，先新建一条分支再提交。`
            : `相对 ${preview.base_branch} 没有新提交，PR 会是空的。`}
        </p>
      ) : null}
    </div>
  );
}

function PRResult({ result, onClose }: { result: CreatePRResult; onClose: () => void }) {
  return (
    <div className="space-y-3">
      <p
        data-testid="create-pr-status"
        className="rounded-md bg-emerald-500/10 p-2 text-xs text-emerald-700 dark:text-emerald-400"
      >
        {result.created ? "Pull Request 已创建。" : "这条分支已经有 Pull Request，没有再创建第二个。"}
        {result.pushed.branch
          ? ` 分支 ${result.pushed.branch} 已在远端（${result.pushed.commit.slice(0, 7)}）。`
          : ""}
      </p>
      <PRLink pr={result.pull_request} />
      {result.warnings?.length ? (
        <ul
          data-testid="create-pr-result-warnings"
          className="space-y-1 rounded-md bg-amber-500/10 p-2 text-[11px] text-amber-700 dark:text-amber-400"
        >
          {result.warnings.map((warning) => (
            <li key={warning}>{warning}</li>
          ))}
        </ul>
      ) : null}
      <DialogFooter>
        <Button variant="outline" data-testid="create-pr-close" onClick={onClose}>
          完成
        </Button>
      </DialogFooter>
    </div>
  );
}

function PRLink({ pr }: { pr: CreatePRResult["pull_request"] }) {
  return (
    <a
      data-testid="create-pr-link"
      href={pr.url}
      target="_blank"
      rel="noopener noreferrer"
      className="flex items-center gap-1.5 break-all rounded-md border p-2 font-mono text-[11px] underline underline-offset-2"
    >
      <ExternalLink className="size-3 shrink-0" />
      {pr.url}
    </a>
  );
}

/** Turn anything thrown by the API into the step + sentence the dialog shows. */
function describeFailure(cause: unknown): { step: string; message: string; forgeStatus?: number } {
  if (cause instanceof ApiError) {
    const details = cause.details ?? {};
    const step = typeof details.step === "string" ? details.step : "unknown";
    const forgeStatus =
      typeof details.forge_status === "number" ? details.forge_status : undefined;
    // `message` already carries the server's step sentence and the forge's own
    // error text; nothing is re-worded here.
    return { step, message: cause.message, forgeStatus };
  }
  if (cause instanceof Error && cause.message) {
    return { step: "unknown", message: cause.message };
  }
  return { step: "unknown", message: "创建 Pull Request 失败，请重试。" };
}

/**
 * The forge wiring form (FR-12.6).
 *
 * It lives in the dialog because a project with no forge configured has no
 * other place to explain what is missing — and "open a PR" is exactly when a
 * person discovers the need. Saving is a separate request; the token field is
 * empty on every open by design (the server never returns it).
 */
function ForgeConfigForm({
  projectId,
  initial,
  onSaved,
}: {
  projectId: string;
  initial: PRPreview["forge"] | null;
  onSaved: () => void;
}) {
  const [kind, setKind] = React.useState(initial?.kind === "gitlab" ? "gitlab" : "github");
  const [apiBase, setApiBase] = React.useState(initial?.api_base ?? "");
  const [repo, setRepo] = React.useState(initial?.repo ?? "");
  const [base, setBase] = React.useState(initial?.base_branch ?? "");
  const [token, setToken] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const hasToken = Boolean(initial?.has_token);

  const save = async (clearToken = false) => {
    setBusy(true);
    setError(null);
    try {
      await api.projects.setForge(projectId, {
        kind: kind === "gitlab" ? "gitlab" : "github",
        api_base: apiBase.trim(),
        repo: repo.trim(),
        base_branch: base.trim(),
        // Omitted means "keep the saved token"; "" is an explicit clear.
        ...(clearToken ? { token: "" } : token.trim() ? { token: token.trim() } : {}),
      });
      onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "保存失败，请重试。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div data-testid="forge-config" className="mt-2 space-y-2 border-t pt-2">
      <div className="grid grid-cols-[90px_1fr] items-center gap-2">
        <FieldLabel htmlFor="forge-kind">类型</FieldLabel>
        <select
          id="forge-kind"
          data-testid="forge-config-kind"
          value={kind}
          disabled={busy}
          onChange={(event) => setKind(event.target.value)}
          className="h-8 w-full rounded-lg border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring"
        >
          <option value="github">GitHub / Gitea</option>
          <option value="gitlab">GitLab</option>
        </select>

        <FieldLabel htmlFor="forge-api">API 地址</FieldLabel>
        <Input
          id="forge-api"
          data-testid="forge-config-api"
          value={apiBase}
          disabled={busy}
          placeholder={
            kind === "gitlab" ? "https://gitlab.example.com/api/v4" : "https://api.github.com"
          }
          onChange={(event) => setApiBase(event.target.value)}
        />

        <FieldLabel htmlFor="forge-repo">仓库</FieldLabel>
        <Input
          id="forge-repo"
          data-testid="forge-config-repo"
          value={repo}
          disabled={busy}
          placeholder="owner/name"
          onChange={(event) => setRepo(event.target.value)}
        />

        <FieldLabel htmlFor="forge-base">基准分支</FieldLabel>
        <Input
          id="forge-base"
          data-testid="forge-config-base"
          value={base}
          disabled={busy}
          placeholder="留空 = 项目默认分支"
          onChange={(event) => setBase(event.target.value)}
        />

        <FieldLabel htmlFor="forge-token">Token</FieldLabel>
        <Input
          id="forge-token"
          data-testid="forge-config-token"
          type="password"
          value={token}
          disabled={busy}
          autoComplete="off"
          placeholder={hasToken ? "已保存，留空保持不变" : "只写一次，之后不回显"}
          onChange={(event) => setToken(event.target.value)}
        />
      </div>
      <p className="text-[10px] text-muted-foreground">{tokenHint(hasToken)}</p>
      {error ? (
        <p data-testid="forge-config-error" className="text-[11px] text-destructive">
          {error}
        </p>
      ) : null}
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          data-testid="forge-config-save"
          disabled={busy || !apiBase.trim() || !repo.trim() || (!hasToken && !token.trim())}
          onClick={() => void save()}
        >
          {busy ? <Spinner /> : null}
          保存配置
        </Button>
        {hasToken ? (
          <Button
            size="sm"
            variant="ghost"
            data-testid="forge-config-clear"
            disabled={busy}
            onClick={() => void save(true)}
          >
            清除 token
          </Button>
        ) : null}
      </div>
    </div>
  );
}
