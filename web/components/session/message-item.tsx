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
import { Markdown } from "@/lib/session/markdown-view";
import type { Turn } from "@/lib/session/reduce";
import { cn } from "@/lib/utils";

import { ThinkingTrace } from "./thinking-trace";
import { ToolTrace } from "./tool-trace";

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

          {turn.blocks.map((block, index) => {
            if (block.kind === "thinking") {
              return (
                <ThinkingTrace key={`thinking-${index}`} text={block.text} running={turn.streaming} />
              );
            }
            if (block.kind === "tool") {
              return <ToolTrace key={`tool-${block.id}`} block={block} />;
            }
            return isUser ? (
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
            );
          })}

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
                模型调用失败
                {turn.retries ? (
                  <span className="ml-2 font-normal text-muted-foreground">
                    已自动重试 {turn.retries} 次
                  </span>
                ) : null}
              </p>
              <p className="mt-1 whitespace-pre-wrap break-words opacity-90">{turn.error}</p>
              <p className="mt-1 text-muted-foreground">
                检查「设置 → 模型」里这个 provider 的地址与密钥，然后重新发送。
              </p>
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
              {turn.model ? (
                // Which model answered is not decoration: a session can be pointed
                // at a custom id, and "it replied" says nothing about what replied.
                <span className="font-mono" data-testid="turn-model">
                  {turn.model}
                </span>
              ) : null}
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
