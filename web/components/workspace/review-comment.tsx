"use client";

import * as React from "react";
import { MessageSquareIcon, SendIcon, Trash2Icon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { useOptionalSessionEventState } from "@/components/session/session-events-provider";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import {
  listDrafts,
  newDraftId,
  removeDraft,
  saveDraft,
  subscribeDrafts,
  type ReviewDraft,
} from "@/lib/review/comments";
import type { CommentAnchor } from "@/lib/git/patch-lines";
import { cn } from "@/lib/utils";

/**
 * Inline review comments (FR-12.3).
 *
 * The flow is one sentence long: point at a line, write why, send. What makes it
 * honest is where each piece lives:
 *
 *   - the draft is persisted per session (lib/review/comments.ts), so a reload
 *     while the run is still going does not throw away what the user typed;
 *   - sending goes through the ordinary message endpoint with a `review_comment`
 *     payload, so the agent gets one message through one path, and the server --
 *     not the browser -- composes the text and decides what to say about a line
 *     that moved;
 *   - while a run is in flight the message is a steer (it interrupts), because
 *     that is the whole point of commenting on the diff you are looking at.
 */

/** useReviewDrafts is the React view of the persisted draft store. */
export function useReviewDrafts(sessionId: string) {
  const [drafts, setDrafts] = React.useState<ReviewDraft[]>([]);

  React.useEffect(() => {
    setDrafts(listDrafts(sessionId));
    return subscribeDrafts(() => setDrafts(listDrafts(sessionId)));
  }, [sessionId]);

  const save = React.useCallback(
    (draft: ReviewDraft) => setDrafts(saveDraft(sessionId, draft)),
    [sessionId],
  );
  const remove = React.useCallback(
    (id: string) => setDrafts(removeDraft(sessionId, id)),
    [sessionId],
  );
  return { drafts, save, remove };
}

/** newId is the client's idempotency key for one send. */
function newId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `msg-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

/**
 * sendReviewDraft posts one draft as a message.
 *
 * `streaming_behavior` is only sent while a run is in flight: the server rejects
 * it otherwise, and an idle comment is an ordinary prompt.
 */
export async function sendReviewDraft(
  sessionId: string,
  draft: ReviewDraft,
  running: boolean,
): Promise<void> {
  await api.sessions.send(sessionId, {
    client_msg_id: newId(),
    // `text` is what the transcript would show if the server ever stops
    // composing; the server replaces it with the full path:line + quote.
    text: draft.body,
    ...(running ? { streaming_behavior: "steer" as const } : {}),
    review_comment: {
      path: draft.path,
      line: draft.line,
      end_line: draft.endLine,
      side: draft.side,
      hunk: draft.hunk,
      quote: draft.quote,
      body: draft.body,
    },
  });
}

/**
 * ReviewCommentEditor edits one anchor.
 *
 * Saving keeps it as a draft (and the diff grows a marker); sending posts it and
 * clears the draft, so a comment that was sent is not offered to send again.
 */
export function ReviewCommentEditor({
  sessionId,
  anchor,
  existing,
  origin = "diff",
  onClose,
}: {
  sessionId: string;
  anchor: CommentAnchor;
  existing?: ReviewDraft;
  /** "file" for a comment written in the file preview rather than the diff. */
  origin?: "diff" | "file";
  onClose: () => void;
}) {
  const { save, remove } = useReviewDrafts(sessionId);
  const events = useOptionalSessionEventState();
  const [text, setText] = React.useState(existing?.body ?? "");
  const [busy, setBusy] = React.useState(false);
  // Stable across re-renders: a fresh uuid per keystroke would make the stored
  // draft's identity depend on how many times the user typed before saving.
  const id = React.useMemo(
    () => existing?.id ?? newDraftId(),
    [existing?.id],
  );

  const draftOf = (body: string): ReviewDraft => ({
    id,
    path: anchor.path,
    line: anchor.line,
    endLine: anchor.endLine,
    side: anchor.side,
    hunk: anchor.hunk,
    quote: anchor.quote,
    body,
    origin: existing?.origin ?? "diff",
    createdAt: existing?.createdAt ?? new Date().toISOString(),
  });

  const store = () => {
    const trimmed = text.trim();
    if (!trimmed) {
      // Clearing the text removes the draft: an empty marker in the diff is noise.
      if (existing) remove(id);
      return null;
    }
    save(draftOf(trimmed));
    return draftOf(trimmed);
  };

  const send = async () => {
    const draft = store();
    if (!draft) {
      toast.error("先写点什么");
      return;
    }
    setBusy(true);
    try {
      await sendReviewDraft(sessionId, draft, Boolean(events?.state.running));
      remove(draft.id);
      toast.success(events?.state.running ? "已改口：这条评论已发给 agent" : "已发送给 agent");
      onClose();
    } catch (error) {
      toast.error("发送失败", { description: humanizeError(error) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      data-testid="review-comment-editor"
      data-path={anchor.path}
      data-line={anchor.line}
      className="flex flex-col gap-1.5 border-y border-sky-500/40 bg-sky-500/5 px-2 py-1.5"
    >
      <div className="flex min-w-0 items-center gap-1.5 text-[10px] text-muted-foreground">
        <MessageSquareIcon className="size-3 shrink-0" />
        <span className="min-w-0 truncate font-mono">
          {anchor.path}:{anchor.line}
        </span>
        {anchor.hunk ? <span className="truncate opacity-70">{anchor.hunk}</span> : null}
      </div>
      {anchor.quote.length > 0 ? (
        <pre
          data-testid="review-comment-quote"
          className="max-h-24 overflow-auto whitespace-pre rounded bg-muted/60 px-1.5 py-1 font-mono text-[10px]"
        >
          {anchor.quote.join("\n")}
        </pre>
      ) : null}
      <Textarea
        data-testid="review-comment-input"
        aria-label="行内评论"
        value={text}
        autoFocus
        onChange={(event) => setText(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            onClose();
          }
          if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
            event.preventDefault();
            void send();
          }
        }}
        placeholder="这条改动哪里不对，或者你想让 agent 怎么改…"
        className="min-h-12 text-xs"
      />
      <div className="flex flex-wrap items-center gap-1">
        <Button size="xs" data-testid="review-comment-save" disabled={busy} onClick={() => {
          store();
          onClose();
        }}>
          保存草稿
        </Button>
        <Button size="xs" variant="default" data-testid="review-comment-send" disabled={busy} onClick={() => void send()}>
          <SendIcon />
          {busy ? "发送中…" : "发给 agent"}
        </Button>
        <Button size="xs" variant="ghost" data-testid="review-comment-cancel" onClick={onClose}>
          <XIcon />
          取消
        </Button>
        <span className="ms-auto text-[10px] text-muted-foreground">⌘/Ctrl+↵ 直接发送</span>
      </div>
    </div>
  );
}

/**
 * ReviewCommentRow is a saved draft shown under its line.
 *
 * It is a marker and a send button in one place: the diff stays readable, and
 * "send this" never requires finding the comment again in another panel.
 */
export function ReviewCommentRow({
  sessionId,
  draft,
  onEdit,
}: {
  sessionId: string;
  draft: ReviewDraft;
  onEdit: () => void;
}) {
  const { remove } = useReviewDrafts(sessionId);
  const events = useOptionalSessionEventState();
  const [busy, setBusy] = React.useState(false);

  const send = async () => {
    setBusy(true);
    try {
      await sendReviewDraft(sessionId, draft, Boolean(events?.state.running));
      remove(draft.id);
      toast.success(events?.state.running ? "已改口：这条评论已发给 agent" : "已发送给 agent");
    } catch (error) {
      toast.error("发送失败", { description: humanizeError(error) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      data-testid="review-comment-row"
      data-path={draft.path}
      data-line={draft.line}
      className={cn("flex min-w-0 items-start gap-2 border-l-2 border-sky-500/60 bg-sky-500/5 px-2 py-1")}
    >
      <MessageSquareIcon className="mt-0.5 size-3 shrink-0 text-sky-600 dark:text-sky-400" />
      <button
        type="button"
        onClick={onEdit}
        className="min-w-0 flex-1 whitespace-pre-wrap break-words text-start text-[11px] hover:underline"
        title="编辑这条草稿"
      >
        {draft.body}
      </button>
      <Button size="xs" data-testid="review-comment-row-send" disabled={busy} onClick={() => void send()}>
        <SendIcon />
        {busy ? "发送中…" : "发给 agent"}
      </Button>
      <Button
        size="icon-xs"
        variant="ghost"
        aria-label="删除这条草稿"
        data-testid="review-comment-row-remove"
        onClick={() => remove(draft.id)}
      >
        <Trash2Icon />
      </Button>
    </div>
  );
}
