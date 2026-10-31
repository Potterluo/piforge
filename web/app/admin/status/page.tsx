"use client";

import { useQuery } from "@tanstack/react-query";
import { Activity, Boxes, Cpu, GitBranch, Globe, HardDrive, MessagesSquare, Server, Users } from "lucide-react";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Item, ItemContent, ItemDescription, ItemMedia, ItemTitle } from "@/components/ui/item";
import { Skeleton } from "@/components/ui/skeleton";
import { agentMetrics } from "@/lib/admin/status-metrics";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { ApiError } from "@/lib/types";

/**
 * Runtime status (PRD FR-16.5): the one screen an operator opens when something
 * feels wrong. Counts only — a self-hosted tool does not need a metrics pipeline,
 * it needs to answer "how many agents are actually running right now".
 */
export default function AdminStatusPage() {
  const status = useQuery({
    queryKey: ["admin", "status"],
    queryFn: () => api.admin.status(),
    refetchInterval: 15_000,
  });

  const data = status.data;
  const forbidden = status.error instanceof ApiError && status.error.status === 403;
  // Two numbers, two labels: see lib/admin/status-metrics.ts. The page used to
  // print the process count as 「运行中的 agent」, so an idle fork raised it.
  const agents = data
    ? agentMetrics(data)
    : {
        runs: { label: "进行中的运行", value: 0, hint: "", testId: "stat-runs" },
        processes: { label: "agent 进程", value: 0, hint: "", testId: "stat-agent-processes" },
      };

  return (
    <div className="flex flex-col gap-5 p-4" data-testid="admin-status">
      <div className="flex flex-col gap-1">
        <h1 className="text-base font-medium">运行状态</h1>
        <p className="text-xs text-muted-foreground">
          每 15 秒刷新。这些数字直接来自数据库，agent 进程数来自控制面自己的订阅表。
        </p>
      </div>

      {status.isPending ? (
        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          {[0, 1, 2, 3].map((index) => (
            <Skeleton key={index} className="h-24 w-full" />
          ))}
        </div>
      ) : status.isError ? (
        <Card>
          <CardHeader>
            <CardTitle>{forbidden ? "需要管理员权限" : "读取失败"}</CardTitle>
            <CardDescription>
              {forbidden
                ? "这个页面只有管理员能打开。"
                : status.error instanceof Error
                  ? humanizeError(status.error)
                  : "未知错误"}
            </CardDescription>
          </CardHeader>
        </Card>
      ) : data ? (
        <>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <Stat
              icon={<Users />}
              label="活跃用户"
              value={`${data.users_active} / ${data.users}`}
              hint="已启用的账号 / 全部账号"
              testId="stat-users"
            />
            <Stat
              icon={<Cpu />}
              label={agents.runs.label}
              value={String(agents.runs.value)}
              hint={agents.runs.hint}
              testId={agents.runs.testId}
            />
            <Stat
              icon={<Boxes />}
              label="沙箱"
              value={`${data.sandboxes_up} / ${data.sandboxes}`}
              hint="就绪或忙碌 / 已创建"
              testId="stat-sandboxes"
            />
            <Stat
              icon={<MessagesSquare />}
              label="会话"
              value={String(data.sessions)}
              hint={`${data.projects} 个项目`}
              testId="stat-sessions"
            />
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="text-sm">配置</CardTitle>
              <CardDescription>这些东西不需要健康检查，但看错数字的人需要。</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Item variant="outline" data-testid="stat-parallel-limit">
                <ItemMedia variant="icon">
                  <GitBranch />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle data-testid="parallel-limit-value">
                    {data.parallel_limit}
                  </ItemTitle>
                  <ItemDescription>
                    并行分支上限（1–{data.parallel_limit_max}，来自 -parallel-limit）
                  </ItemDescription>
                </ItemContent>
              </Item>
              <Item variant="outline" data-testid={agents.processes.testId}>
                <ItemMedia variant="icon">
                  <Cpu />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle data-testid="agent-processes-value">
                    {agents.processes.value}
                  </ItemTitle>
                  <ItemDescription>
                    {agents.processes.label}：{agents.processes.hint}
                  </ItemDescription>
                </ItemContent>
              </Item>
              <Item variant="outline" data-testid="stat-browsers">
                <ItemMedia variant="icon">
                  <Globe />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle data-testid="browsers-running-value">{data.browsers_running}</ItemTitle>
                  <ItemDescription>
                    沙箱浏览器
                    {data.browser_sandboxes.length > 0
                      ? ` · ${data.browser_sandboxes.length} 个沙箱`
                      : ""}
                    {data.browser_idle_after
                      ? `（无查看者 ${data.browser_idle_after} 后停止）`
                      : ""}
                  </ItemDescription>
                </ItemContent>
              </Item>
              <Item variant="outline">
                <ItemMedia variant="icon">
                  <Server />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle>{data.providers}</ItemTitle>
                  <ItemDescription>provider（含共享）</ItemDescription>
                </ItemContent>
              </Item>
              <Item variant="outline">
                <ItemMedia variant="icon">
                  <Activity />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle>{data.skills}</ItemTitle>
                  <ItemDescription>skill</ItemDescription>
                </ItemContent>
              </Item>
              <Item variant="outline">
                <ItemMedia variant="icon">
                  <Boxes />
                </ItemMedia>
                <ItemContent>
                  <ItemTitle>{data.mcp_servers}</ItemTitle>
                  <ItemDescription>MCP server</ItemDescription>
                </ItemContent>
              </Item>
            </CardContent>
          </Card>

          <Card data-testid="stat-storage">
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5 text-sm">
                <HardDrive className="size-4" />
                运行时目录
              </CardTitle>
              <CardDescription>
                数据目录下每个由服务端创建的目录：直接子项数 / 递归字节。目录有主就有删除路径，删不掉时由 janitor
                兜底；这里看到的是它清扫的对象，共 {formatBytes(data.storage_total_bytes)}。
              </CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-1">
              {data.storage.map((row) => (
                <div
                  key={row.name}
                  data-testid={`stat-storage-${row.name}`}
                  className="flex items-baseline justify-between gap-3 border-b border-border/60 py-1.5 text-xs last:border-0"
                >
                  <span className="font-mono text-[11px]">{row.name}/</span>
                  <span className="flex items-baseline gap-2 text-right text-muted-foreground">
                    <span className="tabular-nums">{row.entries.toLocaleString("zh-CN")} 项</span>
                    <span className="tabular-nums">{formatBytes(row.bytes)}</span>
                    {row.truncated ? <span className="text-amber-600">（已达扫描上限）</span> : null}
                  </span>
                </div>
              ))}
              {/* FR-5.8: the directory block says how big the sandboxes root is;
                  this says which sandbox is close to its budget. A deployment that
                  turned enforcement off still reports the measurement. */}
              {data.sandbox_usage.length > 0 ? (
                <div className="mt-2 flex flex-col gap-1" data-testid="stat-storage-quotas">
                  <p className="text-[11px] text-muted-foreground">
                    每个沙箱的磁盘用量 / 配额
                    {data.sandbox_disk_quota > 0
                      ? `（配额 ${formatBytes(data.sandbox_disk_quota)}，来自 -sandbox-disk-quota）`
                      : "（配额未启用，仅显示用量）"}
                  </p>
                  {data.sandbox_usage.map((row) => {
                    const percent =
                      row.quota_bytes > 0 ? Math.round((row.bytes / row.quota_bytes) * 100) : null;
                    const near = percent !== null && percent >= 80;
                    return (
                      <div
                        key={row.sandbox_id}
                        data-testid={`stat-sandbox-${row.sandbox_id}`}
                        data-over={row.over ? "true" : "false"}
                        className="flex items-baseline justify-between gap-3 border-b border-border/60 py-1 text-[11px] last:border-0"
                      >
                        <span className="truncate font-mono text-[10px]" title={`${row.user_id} / ${row.project_id}`}>
                          {row.sandbox_id}
                        </span>
                        <span
                          className={`flex items-baseline gap-2 text-right tabular-nums ${
                            row.over ? "text-destructive" : near ? "text-amber-600" : "text-muted-foreground"
                          }`}
                        >
                          <span>{formatBytes(row.bytes)}</span>
                          {row.quota_bytes > 0 ? (
                            <span>
                              / {formatBytes(row.quota_bytes)} · {percent}%
                            </span>
                          ) : null}
                          {row.over ? <span>已超出</span> : near ? <span>接近上限</span> : null}
                        </span>
                      </div>
                    );
                  })}
                </div>
              ) : null}
            </CardContent>
          </Card>

          <p className="text-[11px] text-muted-foreground">
            事件记录共 {data.events.toLocaleString("zh-CN")} 条。事件是会话的追加日志，删会话会一并删除。
          </p>
        </>
      ) : null}
    </div>
  );
}

/** Bytes the way an operator reads them: no decimal for B/KiB, one above. */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
}

function Stat({
  icon,
  label,
  value,
  hint,
  testId,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  hint: string;
  testId: string;
}) {
  return (
    <Card data-testid={testId} className="gap-1 py-3">
      <CardContent className="flex flex-col gap-1">
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <span className="[&_svg]:size-3.5">{icon}</span>
          {label}
        </div>
        <span className="text-xl font-medium tabular-nums">{value}</span>
        <span className="text-[11px] text-muted-foreground">{hint}</span>
      </CardContent>
    </Card>
  );
}
