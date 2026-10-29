"use client";

import { useSyncExternalStore } from "react";
import { useTheme } from "next-themes";
import { Monitor, Moon, Sun, UserRound } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Kbd } from "@/components/ui/kbd";
import { useCurrentUser } from "@/lib/session/queries";

const THEMES = [
  { value: "system", label: "跟随系统", icon: Monitor },
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
] as const;

/** True only after hydration, without a setState-in-effect render cascade. */
const subscribeMounted = () => () => {};
const clientMounted = () => true;
const serverMounted = () => false;

/**
 * General settings: appearance, the account in use, and the two keyboard
 * shortcuts the workbench relies on. The page exists so the settings navigation
 * has a 通用 entry that is a real destination, not a palette-only concept.
 */
export default function GeneralSettingsPage() {
  const { theme, setTheme } = useTheme();
  const mounted = useSyncExternalStore(subscribeMounted, clientMounted, serverMounted);
  const me = useCurrentUser();
  const user = me.data?.user;
  const current = mounted ? (theme ?? "system") : "system";

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto" data-testid="settings-general">
      <header className="flex flex-wrap items-start gap-3 border-b border-border p-4">
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className="text-base font-medium">通用</h1>
          <p className="max-w-2xl text-xs text-muted-foreground">
            外观、当前账号和快捷键。这里的设置只影响这个浏览器。
          </p>
        </div>
      </header>

      <div className="flex flex-col gap-4 p-4">
        <Card>
          <CardHeader>
            <CardTitle>外观</CardTitle>
            <CardDescription>深色是工作台的默认外观；跟随系统会随操作系统的设置切换。</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-wrap gap-2">
            {THEMES.map((item) => {
              const Icon = item.icon;
              const active = current === item.value;
              return (
                <Button
                  key={item.value}
                  variant={active ? "secondary" : "outline"}
                  size="sm"
                  data-testid={`theme-${item.value}`}
                  aria-pressed={active}
                  onClick={() => setTheme(item.value)}
                >
                  <Icon />
                  {item.label}
                </Button>
              );
            })}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>账号</CardTitle>
            <CardDescription>账号由管理员创建，不能在这里注册或改名。</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2 text-sm">
            <div className="flex items-center gap-2">
              <UserRound className="size-4 text-muted-foreground" />
              <span data-testid="general-email">{user?.email ?? "…"}</span>
              {user ? (
                <Badge variant="outline">{user.role === "admin" ? "管理员" : "普通用户"}</Badge>
              ) : null}
            </div>
            <p className="text-xs text-muted-foreground">
              需要修改密码或停用账号，请联系部署管理员。
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>快捷键</CardTitle>
            <CardDescription>命令面板里能找到应用的所有入口。</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2 text-sm">
            <div className="flex items-center gap-2">
              <Kbd>Ctrl</Kbd>
              <Kbd>K</Kbd>
              <span className="text-muted-foreground">打开命令面板</span>
            </div>
            <div className="flex items-center gap-2">
              <Kbd>Enter</Kbd>
              <span className="text-muted-foreground">发送消息</span>
            </div>
            <div className="flex items-center gap-2">
              <Kbd>Shift</Kbd>
              <Kbd>Enter</Kbd>
              <span className="text-muted-foreground">换行</span>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
