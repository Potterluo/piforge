"use client";

import { useQuery } from "@tanstack/react-query";
import { Activity, Boxes, Cpu, MessagesSquare, Server, Users } from "lucide-react";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Item, ItemContent, ItemDescription, ItemMedia, ItemTitle } from "@/components/ui/item";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api/client";
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
                  ? status.error.message
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
              label="运行中的 agent"
              value={String(data.agents_running)}
              hint={`${data.sessions_live} 个未结束的会话`}
              testId="stat-agents"
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
            <CardContent className="grid gap-3 sm:grid-cols-3">
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

          <p className="text-[11px] text-muted-foreground">
            事件记录共 {data.events.toLocaleString("zh-CN")} 条。事件是会话的追加日志，删会话会一并删除。
          </p>
        </>
      ) : null}
    </div>
  );
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
