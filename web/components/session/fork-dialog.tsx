"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { GitFork } from "lucide-react";

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
import { api } from "@/lib/api/client";
import { truncate } from "@/lib/session/format";

/**
 * The dialog body is mounted only while the dialog is open, so its label state
 * resets naturally on close instead of being cleared from an effect.
 */
function ForkForm({
  sessionId,
  projectId,
  entryId,
  preview,
  onClose,
}: {
  sessionId: string;
  projectId: string;
  entryId: string;
  preview?: string;
  onClose: () => void;
}) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [label, setLabel] = useState("");

  const fork = useMutation({
    mutationFn: () => api.sessions.fork(sessionId, entryId, label.trim()),
    onSuccess: (data) => {
      void queryClient.invalidateQueries({ queryKey: ["session-tree", sessionId] });
      void queryClient.invalidateQueries({ queryKey: ["session-fork-points", sessionId] });
      onClose();
      toast.success("分支已创建", { description: data.session.title });
      router.push(`/p/${projectId}/s/${data.session.id}`);
    },
    onError: (error) => {
      toast.error("分叉失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    },
  });

  return (
    <>
      {preview ? (
        <p className="rounded-md bg-muted p-2 text-xs text-muted-foreground">
          {truncate(preview, 240)}
        </p>
      ) : null}

      <Field>
        <FieldLabel htmlFor="fork-label">分支名称</FieldLabel>
        <Input
          id="fork-label"
          data-testid="fork-label"
          value={label}
          onChange={(event) => setLabel(event.target.value)}
          placeholder="例如：fix-cache-v2"
          autoFocus
        />
      </Field>

      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          取消
        </Button>
        <Button
          data-testid="fork-submit"
          disabled={fork.isPending || label.trim().length === 0}
          onClick={() => fork.mutate()}
        >
          {fork.isPending ? <Spinner /> : <GitFork />}
          创建分支
        </Button>
      </DialogFooter>
    </>
  );
}

/**
 * "从这里分叉" — one dialog for both entry points (the user-message hover
 * action and the branch tree). Pi only forks at user messages, so the caller
 * always passes a user entry id.
 */
export function ForkDialog({
  open,
  onOpenChange,
  sessionId,
  projectId,
  entryId,
  preview,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sessionId: string;
  projectId: string;
  entryId: string | null;
  preview?: string;
}) {
  return (
    <Dialog open={open && entryId !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>从这里分叉</DialogTitle>
          <DialogDescription>
            新分支会拿到独立 worktree 和独立 Pi 进程，当前会话保持原样。分叉点之前的上下文会被复制。
          </DialogDescription>
        </DialogHeader>
        {entryId ? (
          <ForkForm
            sessionId={sessionId}
            projectId={projectId}
            entryId={entryId}
            preview={preview}
            onClose={() => onOpenChange(false)}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
