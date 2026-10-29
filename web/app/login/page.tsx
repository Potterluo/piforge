"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { LogIn } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";

const SEEDED_ACCOUNTS = [
  { email: "admin@piforge.local", role: "管理员" },
  { email: "dev@piforge.local", role: "普通用户" },
];

export default function LoginPage() {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("dev@piforge.local");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Before hydration a submit falls through to the browser's native form GET,
  // which would put the password in the URL. Keeping the button disabled until
  // React is attached makes that impossible.
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!email.trim() || !password) {
      setError("请填写邮箱和密码");
      toast.error("请填写邮箱和密码");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await api.auth.login(email.trim(), password);
      await queryClient.invalidateQueries({ queryKey: ["me"] });
      router.push("/");
    } catch (err) {
      const message = humanizeError(err);
      setError(message);
      toast.error("登录失败", { description: message });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-svh flex-1 items-center justify-center bg-background p-6">
      <div className="flex w-full max-w-sm flex-col gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="text-xl font-semibold tracking-tight">PiForge</h1>
          <p className="text-sm text-muted-foreground">
            基于 Pi Agent 的多用户 Web Coding Agent 工作台
          </p>
        </div>

        <Card>
          <CardHeader>
            <CardTitle>登录</CardTitle>
            <CardDescription>使用内部账号继续</CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={submit} className="flex flex-col gap-4">
              <FieldGroup>
                <Field>
                  <FieldLabel htmlFor="login-email">邮箱</FieldLabel>
                  <Input
                    id="login-email"
                    data-testid="login-email"
                    type="email"
                    autoComplete="username"
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                    placeholder="dev@piforge.local"
                    required
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="login-password">密码</FieldLabel>
                  <Input
                    id="login-password"
                    data-testid="login-password"
                    type="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    required
                  />
                </Field>
              </FieldGroup>

              {error ? (
                <p
                  data-testid="login-error"
                  role="alert"
                  className="rounded-md border border-destructive/30 bg-destructive/10 px-2.5 py-2 text-xs text-destructive"
                >
                  {error}
                </p>
              ) : null}

              <Button
                type="submit"
                data-testid="login-submit"
                disabled={submitting || !mounted}
              >
                {submitting ? <Spinner /> : <LogIn />}
                登录
              </Button>
            </form>
          </CardContent>
        </Card>

        <div className="flex flex-col gap-1 rounded-lg border border-dashed border-border p-3 text-xs text-muted-foreground">
          <p className="font-medium text-foreground">预置账号</p>
          {SEEDED_ACCOUNTS.map((account) => (
            <p key={account.email} className="font-mono">
              {account.email}
              <span className="ml-2 font-sans">{account.role}</span>
            </p>
          ))}
          <FieldDescription className="mt-1">
            初始密码在首次启动后端时打印到它的日志里（只打印一次），请向部署同学索取。
          </FieldDescription>
        </div>
      </div>
    </div>
  );
}
