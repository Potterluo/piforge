"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { KeyRound, Plus, Shield, ShieldOff, SlidersHorizontal, UserX, Users } from "lucide-react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api/client";
import type { User } from "@/lib/types";
import { formatRelative } from "@/lib/session/format";
import { qk, useAdminUsers, useCurrentUser } from "@/lib/session/queries";

/**
 * Admin is not a superuser (PRD §3): this page shows accounts and roles, never
 * another user's conversations. It is also the only way to onboard somebody,
 * because the product has no public signup.
 */
export default function AdminUsersPage() {
  const me = useCurrentUser();
  const isAdmin = me.data?.user.role === "admin";
  const users = useAdminUsers(isAdmin);
  const client = useQueryClient();

  const [creating, setCreating] = useState(false);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"user" | "admin">("user");
  const [resetting, setResetting] = useState<User | null>(null);
  const [featureUser, setFeatureUser] = useState<User | null>(null);
  const [newPassword, setNewPassword] = useState("");

  const refresh = () => void client.invalidateQueries({ queryKey: qk.users });

  const createUser = useMutation({
    mutationFn: () =>
      api.admin.createUser({
        email: email.trim(),
        password,
        display_name: displayName.trim() || undefined,
        role,
      }),
    onSuccess: () => {
      toast.success("账号已创建");
      setCreating(false);
      setEmail("");
      setDisplayName("");
      setPassword("");
      setRole("user");
      refresh();
    },
    onError: (error: Error) => toast.error(error.message),
  });

  const updateUser = useMutation({
    mutationFn: (input: { id: string; role?: "user" | "admin"; status?: "active" | "disabled"; password?: string }) =>
      api.admin.updateUser(input.id, {
        role: input.role,
        status: input.status,
        password: input.password,
      }),
    onSuccess: (_data, variables) => {
      const who = (users.data?.users ?? []).find((user) => user.id === variables.id)?.email;
      const what = variables.role
        ? variables.role === "admin"
          ? "已设为管理员"
          : "已降为普通用户"
        : variables.status
          ? variables.status === "disabled"
            ? "已停用"
            : "已启用"
          : "密码已重置";
      toast.success(who ? `${who} · ${what}` : what);
      refresh();
    },
    onError: (error: Error) => toast.error(error.message),
  });

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
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h1 className="flex items-center gap-2 text-base font-medium">
            <Shield className="size-4 text-muted-foreground" />
            用户管理
          </h1>
          <p className="max-w-2xl text-xs text-muted-foreground">
            只有两种角色：普通用户与管理员。管理员负责容量与共享配置，不读取他人会话。
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)} data-testid="admin-create-user">
          <Plus />
          新建用户
        </Button>
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
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(users.data?.users ?? []).map((user) => {
                const isSelf = user.id === me.data?.user.id;
                return (
                  <TableRow key={user.id} data-testid={`admin-user-${user.email}`}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <Avatar className="size-7">
                          <AvatarFallback>
                            {(user.display_name || user.email).slice(0, 1).toUpperCase()}
                          </AvatarFallback>
                        </Avatar>
                        <div className="flex min-w-0 flex-col">
                          <span className="truncate text-sm font-medium">
                            {user.display_name || user.email.split("@")[0]}
                          </span>
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
                        {user.status === "active" ? "正常" : "已停用"}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatRelative(user.created_at)}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={isSelf || updateUser.isPending}
                          title={isSelf ? "不能修改自己的角色" : "切换角色"}
                          onClick={() =>
                            updateUser.mutate({
                              id: user.id,
                              role: user.role === "admin" ? "user" : "admin",
                            })
                          }
                        >
                          {user.role === "admin" ? <ShieldOff /> : <Shield />}
                          {user.role === "admin" ? "降为普通" : "设为管理员"}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          data-testid="user-features"
                          title="功能开关"
                          onClick={() => setFeatureUser(user)}
                        >
                          <SlidersHorizontal />
                          功能
                        </Button>
                        {user.status === "active" ? (
                          // Disabling locks somebody out mid-session, so it asks
                          // first. Enabling is harmless and stays one click.
                          <AlertDialog>
                            <AlertDialogTrigger
                              render={
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  disabled={isSelf || updateUser.isPending}
                                  title={isSelf ? "不能停用自己" : "停用"}
                                />
                              }
                            >
                              <UserX />
                              停用
                            </AlertDialogTrigger>
                            <AlertDialogContent>
                              <AlertDialogHeader>
                                <AlertDialogTitle>停用 {user.email}？</AlertDialogTitle>
                                <AlertDialogDescription>
                                  该账号会被立即登出，已有的会话无法再打开。项目、会话和沙箱数据都会保留，随时可以启用。
                                </AlertDialogDescription>
                              </AlertDialogHeader>
                              <AlertDialogFooter>
                                <AlertDialogCancel>取消</AlertDialogCancel>
                                <AlertDialogAction
                                  onClick={() => updateUser.mutate({ id: user.id, status: "disabled" })}
                                >
                                  停用
                                </AlertDialogAction>
                              </AlertDialogFooter>
                            </AlertDialogContent>
                          </AlertDialog>
                        ) : (
                          <Button
                            size="sm"
                            variant="ghost"
                            disabled={updateUser.isPending}
                            title="启用"
                            onClick={() => updateUser.mutate({ id: user.id, status: "active" })}
                          >
                            <UserX />
                            启用
                          </Button>
                        )}
                        <Button
                          size="sm"
                          variant="ghost"
                          title="重置密码"
                          onClick={() => {
                            setNewPassword("");
                            setResetting(user);
                          }}
                        >
                          <KeyRound />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </div>

      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>新建用户</DialogTitle>
            <DialogDescription>密码至少 8 位，创建后可以随时由管理员重置。</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="new-user-email">邮箱</FieldLabel>
              <Input
                id="new-user-email"
                data-testid="new-user-email"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
                placeholder="someone@example.com"
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="new-user-name">显示名（可选）</FieldLabel>
              <Input
                id="new-user-name"
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
                placeholder="张三"
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="new-user-password">初始密码</FieldLabel>
              <Input
                id="new-user-password"
                data-testid="new-user-password"
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="new-user-role">角色</FieldLabel>
              <Select
                items={[
                  { value: "user", label: "普通用户" },
                  { value: "admin", label: "管理员" },
                ]}
                value={role}
                onValueChange={(value) => setRole(value as "user" | "admin")}
              >
                <SelectTrigger id="new-user-role" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="user">普通用户</SelectItem>
                  <SelectItem value="admin">管理员</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreating(false)}>
              取消
            </Button>
            <Button
              data-testid="new-user-submit"
              disabled={createUser.isPending || !email.includes("@") || password.length < 8}
              onClick={() => createUser.mutate()}
            >
              创建
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <FeatureDialog user={featureUser} onClose={() => setFeatureUser(null)} />

      <Dialog open={resetting !== null} onOpenChange={(open) => (open ? null : setResetting(null))}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>重置密码</DialogTitle>
            <DialogDescription>
              {resetting?.email} 的密码会被替换，该账号已登录的会话立即失效。
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="reset-password">新密码</FieldLabel>
            <Input
              id="reset-password"
              data-testid="reset-password-input"
              type="password"
              value={newPassword}
              onChange={(event) => setNewPassword(event.target.value)}
            />
            <FieldDescription data-testid="reset-password-hint">
              {newPassword.length === 0
                ? "至少 8 位。"
                : newPassword.length < 8
                  ? `还差 ${8 - newPassword.length} 位（至少 8 位）。`
                  : "长度符合要求。"}
            </FieldDescription>
          </Field>
          <DialogFooter>
            <Button variant="outline" onClick={() => setResetting(null)}>
              取消
            </Button>
            <Button
              disabled={updateUser.isPending || newPassword.length < 8}
              onClick={() => {
                if (!resetting) return;
                updateUser.mutate(
                  { id: resetting.id, password: newPassword },
                  { onSuccess: () => setResetting(null) },
                );
              }}
            >
              重置
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
/**
 * Per-user feature switches (PRD FR-16.7). Each row is enforced by the server, so
 * turning one off is a real decision rather than a hidden button.
 */
function FeatureDialog({ user, onClose }: { user: User | null; onClose: () => void }) {
  const queryClient = useQueryClient();
  const features = useQuery({
    queryKey: ["admin", "features", user?.id],
    queryFn: () => api.admin.userFeatures(user!.id),
    enabled: Boolean(user),
  });

  const setFlag = useMutation({
    mutationFn: (input: { flag: string; enabled: boolean }) =>
      api.admin.setUserFeature(user!.id, input.flag, input.enabled),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "features", user?.id] });
      void queryClient.invalidateQueries({ queryKey: ["features"] });
    },
    onError: (error) =>
      toast.error("保存失败", {
        description: error instanceof Error ? error.message : "未知错误",
      }),
  });

  const labels: Record<string, string> = {
    export: "导出会话（Markdown / HTML）",
    browser: "沙箱内置浏览器",
    sidecar: "Sidecar 旁路会话",
    // Defaults off, so a missing value and a switched-off switch must read the same.
    parallel: "并行运行分支（同一会话下多个分支同时跑）",
  };

  return (
    <Dialog open={Boolean(user)} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>功能开关</DialogTitle>
          <DialogDescription>
            {user?.email} 的这些功能由服务端强制生效：关掉之后，对应的接口会直接拒绝，而不只是隐藏按钮。
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {Object.keys(labels).map((flag) => (
            <div key={flag} className="flex items-center justify-between gap-3">
              <span className="text-xs">{labels[flag]}</span>
              <Switch
                data-testid={`feature-${flag}`}
                checked={features.data?.features[flag] ?? flag !== "parallel"}
                disabled={features.isPending || setFlag.isPending}
                onCheckedChange={(checked) => setFlag.mutate({ flag, enabled: checked })}
              />
            </div>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}