"use client";

import * as React from "react";
import { useMutation } from "@tanstack/react-query";
import { Eye, ShieldAlert } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import type { SupportNotice } from "@/lib/types";

/**
 * Support mode (PRD FR-16.8): an admin reads one session, on the record.
 *
 * The screen is deliberately awkward — a session id has to be pasted and a reason
 * typed — because the alternative is a "browse everything" view, which is the
 * thing this product promises not to have. The owner gets a receipt either way.
 */
export default function SupportPage() {
  const [sessionId, setSessionId] = React.useState("");
  const [reason, setReason] = React.useState("");

  const [access, setAccess] = React.useState<SupportNotice | null>(null);
  const [transcript, setTranscript] = React.useState<{ role: string; text: string }[]>([]);

  const open = useMutation({
    mutationFn: () => api.admin.supportAccess(sessionId.trim(), reason.trim()),
    onSuccess: (data) => {
      setAccess(data.access);
      setTranscript(data.transcript ?? []);
      toast.success("已开启只读查看", { description: "会话所有者会看到一条记录。" });
    },
    onError: (error) =>
      toast.error("无法查看", {
        description: humanizeError(error),
      }),
  });

  const canSubmit = sessionId.trim().length > 0 && reason.trim().length > 0;

  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4" data-testid="support-page">
      <div className="flex flex-col gap-1">
        <h1 className="text-base font-medium">只读排障</h1>
        <p className="max-w-2xl text-xs text-muted-foreground">
          管理员默认看不到别人的会话。这里可以对某一个会话开启只读查看：必须填写原因，30 分钟后自动失效，
          并且会话所有者会收到一条记录，包含你的邮箱和原因。这个页面不能修改任何东西。
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm">
            <ShieldAlert className="size-4" />
            开启一次只读查看
          </CardTitle>
          <CardDescription>会话 id 由用户提供，或被邀请来排查的那次运行。</CardDescription>
        </CardHeader>
        <CardContent>
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              if (!canSubmit) return;
              open.mutate();
            }}
          >
            <Field>
              <FieldLabel htmlFor="support-session">会话 id</FieldLabel>
              <Input
                id="support-session"
                data-testid="support-session"
                value={sessionId}
                onChange={(event) => setSessionId(event.target.value)}
                placeholder="ses_…"
                className="font-mono text-xs"
                required
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="support-reason">原因</FieldLabel>
              <Textarea
                id="support-reason"
                data-testid="support-reason"
                value={reason}
                onChange={(event) => setReason(event.target.value)}
                rows={2}
                placeholder="用户报告 run 卡在 99%，需要看最后一次工具调用的输出"
                required
              />
              <FieldDescription>
                这句话会原样出现在所有者的通知里——写给他看的，不是写给系统看的。
              </FieldDescription>
            </Field>
            <div>
              <Button type="submit" data-testid="support-submit" disabled={!canSubmit || open.isPending}>
                <Eye />
                查看
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {access ? (
        <Card data-testid="support-transcript">
          <CardHeader>
            <CardTitle className="flex flex-wrap items-center gap-2 text-sm">
              转写
              <Badge variant="outline">{access.session_title || access.session_id}</Badge>
              <Badge variant="secondary">只读</Badge>
            </CardTitle>
            <CardDescription>
              {access.reason} · 有效期至 {new Date(access.expires_at).toLocaleTimeString("zh-CN")}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {transcript.length === 0 ? (
              <p className="text-xs text-muted-foreground">这个会话还没有消息。</p>
            ) : (
              transcript.map((line, index) => (
                <div key={index} className="flex flex-col gap-1">
                  <span className="text-[11px] text-muted-foreground">
                    {line.role === "user" ? "用户" : "助手"}
                  </span>
                  <p className="whitespace-pre-wrap text-xs">{line.text}</p>
                </div>
              ))
            )}
          </CardContent>
        </Card>
      ) : null}
    </div>
  );
}
