"use client";

import { Shield, ShieldOff, Users } from "lucide-react";

import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatRelative } from "@/lib/session/format";
import { useAdminUsers, useCurrentUser } from "@/lib/session/queries";

/**
 * Admin is not a superuser (PRD §3): this page shows accounts and roles, never
 * another user's conversations.
 */
export default function AdminUsersPage() {
  const me = useCurrentUser();
  const isAdmin = me.data?.user.role === "admin";
  const users = useAdminUsers(isAdmin);

  if (me.isPending) {
    return (
      <div className="flex flex-col gap-3 p-4">
        <Skeleton className="h-6 w-40" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  if (!isAdmin) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <Empty className="max-w-md border border-dashed border-border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <ShieldOff />
            </EmptyMedia>
            <EmptyTitle>需要管理员权限</EmptyTitle>
            <EmptyDescription>
              用户管理只对管理员开放。管理员也看不到其他人的会话内容，这是刻意的边界。
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <header className="flex flex-wrap items-start gap-3 border-b border-border p-4">
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className="flex items-center gap-2 text-base font-medium">
            <Shield className="size-4 text-muted-foreground" />
            用户管理
          </h1>
          <p className="max-w-2xl text-xs text-muted-foreground">
            只有两种角色：普通用户与管理员。管理员负责容量与共享配置，不读取他人会话。
          </p>
        </div>
      </header>

      <div className="p-4">
        {users.isPending ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : users.isError ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            用户列表加载失败
            <Button size="sm" variant="outline" onClick={() => void users.refetch()}>
              重试
            </Button>
          </div>
        ) : (users.data?.users ?? []).length === 0 ? (
          <Empty className="border border-dashed border-border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Users />
              </EmptyMedia>
              <EmptyTitle>还没有用户</EmptyTitle>
              <EmptyDescription>种子账号之外还没有其他用户。</EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>用户</TableHead>
                <TableHead>角色</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>创建时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(users.data?.users ?? []).map((user) => (
                <TableRow key={user.id}>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Avatar className="size-7">
                        <AvatarFallback>
                          {(user.display_name || user.email).slice(0, 1).toUpperCase()}
                        </AvatarFallback>
                      </Avatar>
                      <div className="flex min-w-0 flex-col">
                        <span className="truncate text-sm font-medium">{user.display_name}</span>
                        <span className="truncate font-mono text-xs text-muted-foreground">
                          {user.email}
                        </span>
                      </div>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={user.role === "admin" ? "default" : "outline"}>
                      {user.role === "admin" ? "管理员" : "普通用户"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <Badge variant={user.status === "active" ? "secondary" : "outline"}>
                      {user.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {formatRelative(user.created_at)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}
