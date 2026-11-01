"use client";

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Download, Search, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { api, auditExportUrl, type AuditQuery } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { ApiError, type AuditRow, type AuditVerifyResult } from "@/lib/types";

const PAGE_SIZE = 50;

/**
 * The operator's forensic audit trail (docs/AUDIT.md).
 *
 * Every tool an agent runs writes two append-only rows: intent (tool, bounded
 * redacted preview, ids, sandbox, model, workspace commit) and observation
 * (status, exit code, duration, bytes). The run facts (tokens, cost) are joined
 * from audit_runs, and the rows survive the session, project and user they
 * describe.
 *
 * It is not the tenant's transcript: arguments are a hash plus a bounded preview
 * with key-shaped fields redacted and file content reduced to sha256 + size.
 * The chain button walks prev_hash/hash over the current filter and names the
 * first edited row, sequence gap or broken link.
 */
export default function AdminAuditPage() {
  const [tool, setTool] = useState("");
  const [outcome, setOutcome] = useState<"any" | "ok" | "error" | "abandoned" | "running">("any");
  const [user, setUser] = useState("");
  const [since, setSince] = useState("");
  const [search, setSearch] = useState("");
  const [minDuration, setMinDuration] = useState("");
  const [offset, setOffset] = useState(0);
  const [verifying, setVerifying] = useState(false);
  const [verified, setVerified] = useState<AuditVerifyResult | null>(null);
  const [verifyError, setVerifyError] = useState("");

  const filter = useMemo(() => {
    const next: AuditQuery = { limit: PAGE_SIZE, offset };
    if (tool.trim()) next.tool = tool.trim();
    if (outcome !== "any") next.outcome = outcome;
    if (user) next.user_id = user;
    // A date input is a local calendar day; the API wants an instant. "Since
    // midnight" is what an operator means by picking a day.
    if (since) next.since = new Date(`${since}T00:00:00`).toISOString();
    if (search.trim()) next.q = search.trim();
    const min = Number.parseInt(minDuration, 10);
    if (Number.isFinite(min) && min > 0) next.min_duration_ms = min;
    return next;
  }, [tool, outcome, user, since, search, minDuration, offset]);

  const users = useQuery({ queryKey: ["admin", "users"], queryFn: () => api.admin.users() });
  const audit = useQuery({
    queryKey: ["admin", "audit", filter],
    queryFn: () => api.admin.audit(filter),
    // Paging and filtering keep the previous page on screen while the next one
    // loads, so the table does not flash empty on every keystroke.
    placeholderData: keepPreviousData,
  });

  const forbidden = audit.error instanceof ApiError && audit.error.status === 403;
  const rows = audit.data?.calls ?? [];
  const total = audit.data?.total ?? 0;
  const page = Math.floor(offset / PAGE_SIZE) + 1;
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  async function runVerify() {
    setVerifying(true);
    setVerifyError("");
    try {
      setVerified(
        await api.admin.auditVerify({
          since: filter.since,
          until: filter.until,
        }),
      );
    } catch (error) {
      setVerified(null);
      setVerifyError(error instanceof Error ? humanizeError(error) : "未知错误");
    } finally {
      setVerifying(false);
    }
  }

  return (
    <div className="flex flex-col gap-5 p-4" data-testid="admin-audit">
      <div className="flex flex-col gap-1">
        <h1 className="text-base font-medium">审计</h1>
        <p className="text-xs text-muted-foreground">
          每个 agent 的工具调用（谁、什么、何时、成败、模型、提交、用量），跨租户只读。
          参数只记录哈希与脱敏预览；文件内容只记 sha256 与字节数。删除会话或项目不会删除审计行。
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">筛选</CardTitle>
          <CardDescription>
            共 {total.toLocaleString("zh-CN")} 条匹配；当前第 {page} / {pages} 页。
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-end gap-2">
          <div className="flex flex-col gap-1">
            <label className="text-[11px] text-muted-foreground" htmlFor="audit-tool">
              工具名
            </label>
            <Input
              id="audit-tool"
              data-testid="audit-tool"
              className="h-8 w-40"
              placeholder="bash"
              value={tool}
              onChange={(event) => {
                setTool(event.target.value);
                setOffset(0);
              }}
            />
          </div>
          <div className="flex flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">结果</span>
            <Select
              value={outcome}
              onValueChange={(value) => {
                setOutcome(value as typeof outcome);
                setOffset(0);
              }}
            >
              <SelectTrigger className="h-8 w-32" data-testid="audit-outcome">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="any">全部</SelectItem>
                <SelectItem value="ok">成功</SelectItem>
                <SelectItem value="error">失败</SelectItem>
                <SelectItem value="abandoned">未回报</SelectItem>
                <SelectItem value="running">进行中</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">账号</span>
            <Select
              value={user || "any"}
              onValueChange={(value) => {
                setUser(!value || value === "any" ? "" : value);
                setOffset(0);
              }}
            >
              <SelectTrigger className="h-8 w-56" data-testid="audit-user">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="any">全部账号</SelectItem>
                {(users.data?.users ?? []).map((account) => (
                  <SelectItem key={account.id} value={account.id}>
                    {account.email}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-[11px] text-muted-foreground" htmlFor="audit-since">
              起始日期
            </label>
            <Input
              id="audit-since"
              data-testid="audit-since"
              type="date"
              className="h-8 w-40"
              value={since}
              onChange={(event) => {
                setSince(event.target.value);
                setOffset(0);
              }}
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-[11px] text-muted-foreground" htmlFor="audit-search">
              预览搜索
            </label>
            <Input
              id="audit-search"
              data-testid="audit-search"
              className="h-8 w-48"
              placeholder="路径 / 命令片段"
              value={search}
              onChange={(event) => {
                setSearch(event.target.value);
                setOffset(0);
              }}
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-[11px] text-muted-foreground" htmlFor="audit-min-duration">
              最短耗时 (ms)
            </label>
            <Input
              id="audit-min-duration"
              data-testid="audit-min-duration"
              className="h-8 w-28"
              inputMode="numeric"
              placeholder="0"
              value={minDuration}
              onChange={(event) => {
                setMinDuration(event.target.value);
                setOffset(0);
              }}
            />
          </div>
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              data-testid="audit-refresh"
              onClick={() => void audit.refetch()}
            >
              <Search /> 查询
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="audit-verify"
              disabled={verifying}
              onClick={() => void runVerify()}
            >
              <ShieldCheck /> {verifying ? "校验中…" : "校验链"}
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="audit-export-csv"
              render={<a href={auditExportUrl(filter, "csv")} download />}
            >
              <Download /> CSV
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="audit-export-jsonl"
              render={<a href={auditExportUrl(filter, "jsonl")} download />}
            >
              <Download /> JSONL
            </Button>
          </div>
        </CardContent>
      </Card>

      {verified || verifyError ? (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">链校验</CardTitle>
            <CardDescription>
              {verifyError
                ? verifyError
                : verified && verified.ok
                  ? `完整：检查 ${verified.checked} 行、${verified.sessions} 个会话，未发现断点。`
                  : verified?.first_break
                    ? `首个断点：第 ${verified.first_break.seq} 行，原因 ${verified.first_break.reason} — ${verified.first_break.detail}`
                    : "没有可检查的行。"}
            </CardDescription>
          </CardHeader>
          {verified?.anchors?.length ? (
            <CardContent className="text-[11px] text-muted-foreground">
              {verified.anchors.map((anchor) => (
                <p key={anchor.session_id}>
                  会话 {anchor.session_id} 从第 {anchor.seq} 行开始（{anchor.reason}），链在该处锚定。
                </p>
              ))}
            </CardContent>
          ) : null}
        </Card>
      ) : null}

      {audit.isPending ? (
        <Skeleton className="h-64 w-full" />
      ) : audit.isError ? (
        <Card>
          <CardHeader>
            <CardTitle>{forbidden ? "需要管理员权限" : "读取失败"}</CardTitle>
            <CardDescription>
              {forbidden
                ? "这个页面只有管理员能打开。"
                : audit.error instanceof Error
                  ? humanizeError(audit.error)
                  : "未知错误"}
            </CardDescription>
          </CardHeader>
        </Card>
      ) : (
        <Card>
          <CardContent className="flex flex-col gap-1 pt-4">
            {rows.length === 0 ? (
              <p className="py-6 text-center text-xs text-muted-foreground">
                没有匹配的工具调用。
              </p>
            ) : (
              rows.map((row) => <AuditLine key={row.id} row={row} />)
            )}
            <div className="mt-3 flex items-center justify-between">
              <span className="text-[11px] text-muted-foreground">
                {offset + 1}–{offset + rows.length} / {total.toLocaleString("zh-CN")}
              </span>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={offset === 0}
                  data-testid="audit-prev"
                  onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                >
                  上一页
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={offset + PAGE_SIZE >= total}
                  data-testid="audit-next"
                  onClick={() => setOffset(offset + PAGE_SIZE)}
                >
                  下一页
                </Button>
              </div>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

/** One row: when, who, where, what, with which model and revision, and how it ended. */
function AuditLine({ row }: { row: AuditRow }) {
  const outcome = row.status || (row.failed === undefined ? "running" : row.failed ? "error" : "ok");
  const tone =
    outcome === "error" || outcome === "abandoned"
      ? "text-destructive"
      : outcome === "running"
        ? "text-amber-600"
        : "text-muted-foreground";
  const preview = row.args_preview ? JSON.stringify(row.args_preview) : "";
  return (
    <div
      data-testid={`audit-row-${row.id}`}
      data-outcome={outcome}
      data-seq={row.seq}
      className="flex flex-col gap-1 border-b border-border/60 py-1.5 text-[11px] last:border-0"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <span className="flex min-w-0 items-baseline gap-2">
          <span className="font-mono tabular-nums text-muted-foreground">
            {new Date(row.started_at).toLocaleString("zh-CN")}
          </span>
          <span className="font-mono font-medium">{row.tool}</span>
          {row.namespace ? (
            <span className="font-mono text-[10px] text-muted-foreground">{row.namespace}</span>
          ) : null}
          <Badge variant="outline" className="text-[10px]">
            {outcome}
          </Badge>
          <span className="font-mono text-[10px] text-muted-foreground">seq {row.seq}</span>
        </span>
        <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-muted-foreground">
          <span className="truncate" title={row.user_id}>
            {row.user_email || row.user_id}
          </span>
          <span className="truncate" title={row.project_id}>
            {row.project || row.project_id}
          </span>
          <span className="font-mono text-[10px]" title={row.session_id}>
            {row.session_id}
          </span>
          <span className={`tabular-nums ${tone}`}>
            {row.duration_ms !== undefined ? `${row.duration_ms} ms` : "—"}
            {row.result_bytes !== undefined ? ` · ${row.result_bytes} B` : ""}
          </span>
        </span>
      </div>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-[10px] text-muted-foreground">
        <span className="font-mono" title={row.model_ref}>
          模型 {row.model_ref || row.run_model_ref || "—"}
        </span>
        <span className="font-mono" title={row.commit_sha}>
          提交 {row.commit_sha ? row.commit_sha.slice(0, 12) : "—"}
        </span>
        {row.exit_code !== undefined ? (
          <span className="font-mono tabular-nums">退出码 {row.exit_code}</span>
        ) : null}
        <span className="tabular-nums">
          用量 {row.run_tokens_in} in / {row.run_tokens_out} out
          {row.run_cost_usd > 0 ? ` · $${row.run_cost_usd.toFixed(4)}` : ""}
        </span>
        <span className="font-mono" title={row.sandbox_id}>
          沙箱 {row.sandbox_id || "—"}
        </span>
        <span className="font-mono" title={row.hash}>
          链 {row.hash ? row.hash.slice(0, 10) : "—"}
          {row.preview_truncated ? " · 预览已截断" : ""}
        </span>
      </div>
      {preview ? (
        <code
          data-testid={`audit-preview-${row.id}`}
          title={preview}
          className="truncate font-mono text-[10px] text-muted-foreground/80"
        >
          {preview}
        </code>
      ) : null}
    </div>
  );
}
