"use client";

import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { SidebarMenuButton } from "@/components/ui/sidebar";
import {
  Dialog,
  DialogContent,
  DialogDescription,
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
import { api } from "@/lib/api/client";
import type { SupportNotice } from "@/lib/types";

/**
 * The owner's side of support mode (PRD FR-16.8): a receipt for every time an
 * admin read a conversation. It is a badge in the footer rather than a page,
 * because it should be noticed when it happens, not found later.
 */
export function SupportNotices() {
  const [open, setOpen] = React.useState(false);
  const queryClient = useQueryClient();
  const notices = useQuery({
    queryKey: ["support-notices"],
    queryFn: () => api.support.notices(),
    refetchInterval: 60_000,
  });

  const unseen = notices.data?.unseen ?? 0;
  const rows: SupportNotice[] = notices.data?.notices ?? [];

  const markSeen = useMutation({
    mutationFn: (id: string) => api.support.seen(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["support-notices"] }),
  });

  const markAll = () => {
    for (const row of rows) {
      if (!row.seen_at) markSeen.mutate(row.id);
    }
  };

  return (
    <>
      {/* A sidebar row, not a bare Button: in the icon rail a plain button keeps its
          label and spills "通知" out of the 48px column. This one folds to the bell
          and explains itself on hover like every other rail affordance. */}
      <SidebarMenuButton
        tooltip={unseen > 0 ? `通知（${unseen} 条未读）` : "通知"}
        aria-label={unseen > 0 ? `通知，${unseen} 条未读` : "通知"}
        data-testid="support-notices"
        onClick={() => {
          setOpen(true);
          if (unseen > 0) markAll();
        }}
      >
        <Bell />
        <span>通知</span>
        {unseen > 0 ? (
          <Badge
            variant="destructive"
            className="ml-auto text-[10px] leading-none group-data-[collapsible=icon]:hidden"
          >
            {unseen}
          </Badge>
        ) : null}
      </SidebarMenuButton>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>通知</DialogTitle>
            <DialogDescription>
              管理员只读查看过你的会话时会留下记录。这里没有别的通知类型——它不是消息中心。
            </DialogDescription>
          </DialogHeader>

          {rows.length === 0 ? (
            <Empty className="border border-dashed border-border">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <Bell />
                </EmptyMedia>
                <EmptyTitle>没有记录</EmptyTitle>
                <EmptyDescription>还没有管理员查看过你的会话。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <ul className="flex max-h-96 flex-col gap-2 overflow-y-auto" data-testid="support-notice-list">
              {rows.map((row) => (
                <li key={row.id} className="rounded-lg border border-border p-3 text-xs">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{row.admin_email || "管理员"}</span>
                    <span className="text-muted-foreground">只读查看了</span>
                    <span className="truncate">{row.session_title || row.session_id}</span>
                    <span className="ml-auto text-[11px] text-muted-foreground">
                      {new Date(row.created_at).toLocaleString("zh-CN")}
                    </span>
                  </div>
                  {row.reason ? (
                    <p className="mt-1 text-muted-foreground">原因：{row.reason}</p>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
