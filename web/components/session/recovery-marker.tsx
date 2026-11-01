"use client";

import { useEffect, useState } from "react";
import { Play, Square } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { useOnChange } from "@/hooks/use-on-change";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import type { InterruptionMarker } from "@/lib/session/reduce";
import type { RecoveryView } from "@/lib/types";

function newId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `msg-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

/** The source label for an old marker whose view has not loaded yet. */
function causeLabel(cause: string): string {
  switch (cause) {
    case "stop":
      return "停止";
    case "restart":
      return "服务重启";
    case "process_exit":
      return "进程退出";
    default:
      return "";
  }
}

/**
 * The interruption marker: what happened, what survived, and one deliberate way
 * forward.
 *
 * It is rendered where the run stopped, not as a banner above the conversation,
 * because "the run ended here" is the fact a returning user is looking for. The
 * survived state is read from the server (`GET /recovery`), never guessed: a
 * sandbox that is gone says so instead of describing a worktree that no longer
 * exists.
 *
 * `active` is true only for the newest interruption with no run after it. Older
 * markers keep their sentence — history — and offer nothing, because "continue"
 * would silently restart work that was already continued or replaced.
 */
export function RecoveryMarker({
  sessionId,
  marker,
  active,
}: {
  sessionId: string;
  marker: InterruptionMarker;
  active: boolean;
}) {
  // The read is keyed by what it is about — the session, whether this marker is
  // the live one, and how many times it has become live — and `loading`/`view`
  // are *derived* from that key instead of being written at the top of the
  // effect. Setting state synchronously inside an effect is what
  // `react-hooks/set-state-in-effect` refuses (and what costs an extra frame):
  // the answer to "is this read still the one on screen" is the signature
  // comparison below.
  const [read, setRead] = useState<{ signature: string; view: RecoveryView | null }>({
    signature: "",
    view: null,
  });
  const [sending, setSending] = useState(false);
  const [sent, setSent] = useState(false);

  // A marker that becomes live again is a new thing to read: bumping the
  // generation is what makes the signature differ and the read start over.
  const [generation, setGeneration] = useState(0);
  if (useOnChange(active) && active) setGeneration((value) => value + 1);

  const signature = `${sessionId}:${active ? "on" : "off"}:${generation}`;
  const loading = active && read.signature !== signature;
  // An inactive marker shows none of it: the read belongs to the live one, and
  // a stale survival summary under an old marker would describe work that has
  // since been continued or replaced.
  const view = active && read.signature === signature ? read.view : null;

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    api.sessions
      .recovery(sessionId)
      .then((next) => {
        if (!cancelled) setRead({ signature, view: next });
      })
      .catch(() => {
        if (!cancelled) setRead({ signature, view: null });
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId, active, signature]);

  const sendContinue = async () => {
    if (sending || sent) return;
    setSending(true);
    try {
      await api.sessions.send(sessionId, {
        client_msg_id: newId(),
        text: "",
        continue_interrupted: true,
      });
      setSent(true);
      toast.success("已发送继续请求", {
        description: "消息里带上了上次的请求、中断原因和先核对当前状态的要求",
      });
    } catch (error) {
      toast.error("继续失败", { description: humanizeError(error) });
    } finally {
      setSending(false);
    }
  };

  const label = view?.cause_label || causeLabel(marker.cause);
  const blocked = active && view && !view.can_continue ? view.blocked_reason : "";
  const canContinue = active && view?.can_continue === true && !sent;

  return (
    <div
      data-testid="run-interrupted"
      data-recovery-marker="true"
      data-cause={marker.cause || "unknown"}
      role="status"
      className="rounded-lg border border-border bg-muted/40 p-3 text-xs"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="inline-flex items-center gap-1 rounded bg-background px-1.5 py-0.5 font-medium text-foreground">
          <Square className="size-3" aria-hidden="true" />
          已中断
        </span>
        <span data-testid="recovery-reason" className="font-medium text-foreground">
          {marker.reason}
        </span>
      </div>

      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground">
        {label ? <span data-testid="recovery-cause">来源：{label}</span> : null}
        {active ? (
          loading ? (
            <span data-testid="recovery-survival">正在读取工作区状态…</span>
          ) : view ? (
            <span data-testid="recovery-survival">{view.survival.summary}</span>
          ) : (
            <span data-testid="recovery-survival">无法读取恢复信息，请刷新页面</span>
          )
        ) : null}
      </div>

      {canContinue ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            data-testid="recovery-continue"
            disabled={sending}
            onClick={() => void sendContinue()}
          >
            <Play className="size-3.5" aria-hidden="true" />
            {sending ? "发送中…" : "继续"}
          </Button>
          <span className="text-[11px] text-muted-foreground">
            会发送一条消息：上次的请求 + 中断原因 + 先核对当前状态的要求
          </span>
        </div>
      ) : blocked ? (
        <p data-testid="recovery-blocked" className="mt-2 text-muted-foreground">
          {blocked}
        </p>
      ) : null}
    </div>
  );
}
