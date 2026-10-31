"use client";

import { useState } from "react";
import { Check, Copy, GitFork } from "lucide-react";

import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import {
  Message,
  MessageContent,
  MessageFooter,
  MessageGroup,
  MessageHeader,
} from "@/components/ui/message";
import { formatClock } from "@/lib/session/format";
import { modelErrorAdvice, modelErrorText, modelErrorTitle } from "@/lib/session/model-error";
import { Markdown } from "@/lib/session/markdown-view";
import type { Turn } from "@/lib/session/reduce";
import { cn } from "@/lib/utils";

import { ThinkingTrace } from "./thinking-trace";
import { ToolGroup } from "./tool-trace";

function plainText(turn: Turn): string {
  return turn.blocks
    .filter((block) => block.kind === "text")
    .map((block) => (block as { text: string }).text)
    .join("\n\n");
}

/**
 * One turn. Blocks are rendered in the model's order, which is why thinking and
 * tool calls appear exactly where they happened instead of being hoisted into
 * a separate panel (PRD §10.4.1 "全部就地折叠").
 */
/**
 * Renders a turn's blocks with tool calls collapsed into one row.
 *
 * The transcript is for what was said. A run that shells out twelve times used to put
 * twelve rows of machinery between the question and the answer; now it puts one, and the
 * calls are behind it. Nothing is hidden — the detail is one click away — but the shape of
 * the conversation stays readable.
 */
function renderBlocks(
  blocks: Turn["blocks"],
  streaming: boolean,
  isUser: boolean,
  compact: boolean | undefined,
  onFork: ((entryId: string) => void) | undefined,
  forkEntryId: string | undefined,
  canFork: boolean,
) {
  const out: React.ReactNode[] = [];
  for (let index = 0; index < blocks.length; index += 1) {
    const block = blocks[index];
    if (block.kind === "thinking") {
      out.push(<ThinkingTrace key={`thinking-${index}`} text={block.text} running={streaming} />);
      continue;
    }
    if (block.kind === "tool") {
      const group: (typeof blocks)[number][] = [block];
      while (index + 1 < blocks.length && blocks[index + 1].kind === "tool") {
        index += 1;
        group.push(blocks[index]);
      }
      // Keyed by the first call, not by the position: while a run streams, blocks are
      // appended and the index of this group moves, which remounted the component and
      // collapsed it under the user's finger. The call id is stable for the run.
      out.push(<ToolGroup key={`tools-${(block as { id: string }).id}`} blocks={group as never} />);
      continue;
    }
    out.push(
      isUser ? (
        <Bubble key={`text-${index}`} variant="secondary" align="end">
          <BubbleContent className={compact ? "px-2.5 py-1.5 text-xs" : undefined}>
            <p className="whitespace-pre-wrap">{block.text}</p>
          </BubbleContent>
        </Bubble>
      ) : (
        <Bubble key={`text-${index}`} variant="ghost">
          <BubbleContent>
            <Markdown text={block.text} />
          </BubbleContent>
        </Bubble>
      ),
    );
  }
  void onFork;
  void forkEntryId;
  void canFork;
  return out;
}

export function MessageItem({
  turn,
  compact = false,
  forkEntryId,
  onFork,
}: {
  turn: Turn;
  compact?: boolean;
  /** Pi entry id of this user message, when it is a valid fork origin. */
  forkEntryId?: string;
  onFork?: (entryId: string) => void;
}) {
  const [copied, setCopied] = useState(false);
  const isUser = turn.role === "user";
  const hasBlocks = turn.blocks.length > 0;
  const canFork = isUser && !compact && Boolean(forkEntryId) && Boolean(onFork);

  // A failed model call arrives as an assistant turn with no content. Rendering
  // it would draw a bare "PiForge" label and nothing else, which reads as a
  // broken page rather than a provider error.
  if (!isUser && !hasBlocks && !turn.error && !turn.streaming) return null;

  const copy = async () => {
    const text = plainText(turn);
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1_500);
    } catch {
      // Clipboard access is permission-gated; failing silently is correct here.
    }
  };

  return (
    <MessageGroup
      data-testid={isUser ? "turn-user" : "turn-assistant"}
      className={compact ? "gap-1" : undefined}
    >
      <Message align={isUser ? "end" : "start"}>
        <MessageContent className={compact ? "gap-1.5" : undefined}>
          <MessageHeader className={cn("gap-2", isUser && "justify-end")}>
            <span>{isUser ? "你" : "PiForge"}</span>
            {!isUser && turn.model ? (
              // Always visible, not hover-only: a session can be pointed at a
              // custom model id, and "it replied" says nothing about what replied.
              <span className="font-mono text-[11px] opacity-70" data-testid="turn-model">
                {turn.model}
              </span>
            ) : null}
            {canFork ? (
              <Button
                variant="ghost"
                size="icon-xs"
                data-testid="fork-button"
                aria-label="从这里分叉"
                title="从这里分叉"
                onClick={() => {
                  if (forkEntryId) onFork?.(forkEntryId);
                }}
              >
                <GitFork />
              </Button>
            ) : null}
          </MessageHeader>

          {renderBlocks(turn.blocks, turn.streaming, isUser, compact, onFork, forkEntryId, canFork)}

          {!hasBlocks && turn.streaming ? (
            <p className="animate-pulse text-xs text-muted-foreground">正在思考…</p>
          ) : null}

          {turn.error ? (
            <div
              data-testid="turn-error"
              role="alert"
              className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
            >
              <p className="font-medium">
                {modelErrorTitle(turn.error)}
                {turn.retries ? (
                  <span className="ml-2 font-normal text-muted-foreground">
                    已自动重试 {turn.retries} 次
                  </span>
                ) : null}
              </p>
              <p className="mt-1 whitespace-pre-wrap break-words opacity-90">{modelErrorText(turn.error)}</p>
              <p className="mt-1 text-muted-foreground">{modelErrorAdvice(turn.error)}</p>
            </div>
          ) : null}

          {turn.streaming && hasBlocks ? (
            <span
              aria-hidden="true"
              className="mt-1 inline-block h-3.5 w-1.5 animate-pulse rounded-[2px] bg-foreground align-middle"
            />
          ) : null}

          {compact ? null : (
            <MessageFooter className="gap-1 opacity-0 transition-opacity group-focus-within/message:opacity-100 group-hover/message:opacity-100">

              <span className="tabular-nums">{formatClock(turn.ts)}</span>
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label="复制这条消息"
                onClick={() => void copy()}
              >
                {copied ? <Check /> : <Copy />}
              </Button>
            </MessageFooter>
          )}
        </MessageContent>
      </Message>
    </MessageGroup>
  );
}
