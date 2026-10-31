"use client";

import { useState } from "react";
import { Check, Copy } from "lucide-react";

import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import {
  Message,
  MessageContent,
  MessageFooter,
  MessageGroup,
  MessageHeader,
} from "@/components/ui/message";
import { formatClock, shortModel } from "@/lib/session/format";
import { modelErrorAdvice, modelErrorText, modelErrorTitle } from "@/lib/session/model-error";
import { Markdown } from "@/lib/session/markdown-view";
import type { Block, Turn } from "@/lib/session/reduce";
import { cn } from "@/lib/utils";

import { ThinkingTrace } from "./thinking-trace";
import { ToolGroup } from "./tool-trace";

function plainText(turns: Turn[]): string {
  return turns
    .flatMap((turn) => turn.blocks)
    .filter((block) => block.kind === "text")
    .map((block) => (block as { text: string }).text)
    .join("\n\n");
}

/**
 * One run, rendered as one assistant block.
 *
 * Pi writes a message per step, so a seven-step run used to draw seven
 * "PiForge deepseek-v4-flash" headers, seven footers and three one-row tool
 * groups in the middle of a single answer. The run is the unit a person reads:
 * the author appears once, the model once, the clock once, and every tool call
 * in the run folds into the single summary row that was already there.
 *
 * Each turn keeps its own `data-testid="turn-assistant"` wrapper so tests (and
 * the DOM) still expose the individual Pi messages; only the chrome around them
 * is shared.
 */
export function AssistantRun({
  turns,
  sessionModel,
  className,
}: {
  turns: Turn[];
  /** The model the session is pinned to, if any. Inherited sessions have none. */
  sessionModel?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  const blocks = turns.flatMap((turn) => turn.blocks);
  const hasBlocks = blocks.length > 0;
  const streaming = turns.some((turn) => turn.streaming);
  const tools = blocks.filter((block): block is Extract<Block, { kind: "tool" }> => block.kind === "tool");
  const runModel = [...turns].reverse().find((turn) => turn.model)?.model;
  // A pinned model is already named in the session header, so repeating it on
  // every run is noise. A session that follows the default has no model name in
  // the header yet, and the run is where the actual model belongs.
  const showModel = Boolean(runModel) && (!sessionModel || shortModel(sessionModel) !== shortModel(runModel));
  const last = turns[turns.length - 1];
  const runTs = last?.ts;

  if (!hasBlocks && !streaming && turns.every((turn) => !turn.error)) return null;

  const copy = async () => {
    const text = plainText(turns);
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1_500);
    } catch {
      // Clipboard access is permission-gated; failing silently is correct here.
    }
  };

  let toolsDrawn = false;

  return (
    <MessageGroup data-testid="run-block" className={cn("gap-2", className)}>
      <Message align="start">
        <MessageContent>
          <MessageHeader className="gap-2" data-testid="run-header">
            <span>PiForge</span>
            {showModel ? (
              <span data-testid="run-model" className="font-mono text-[11px] opacity-70">
                <span data-testid="turn-model">{runModel}</span>
              </span>
            ) : null}
          </MessageHeader>

          {turns.map((turn) => (
            <div key={turn.id} data-testid="turn-assistant" className="flex min-w-0 flex-col gap-2">
              {turn.blocks.map((block, blockIndex) => {
                if (block.kind === "thinking") {
                  return (
                    <ThinkingTrace key={`thinking-${turn.id}-${blockIndex}`} text={block.text} running={turn.streaming} />
                  );
                }
                if (block.kind === "tool") {
                  if (toolsDrawn) return null;
                  toolsDrawn = true;
                  return <ToolGroup key={`tools-${tools[0]?.id ?? turn.id}`} blocks={tools} />;
                }
                return (
                  <Bubble key={`text-${turn.id}-${blockIndex}`} variant="ghost">
                    <BubbleContent>
                      <Markdown text={block.text} />
                    </BubbleContent>
                  </Bubble>
                );
              })}

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
            </div>
          ))}

          {!hasBlocks && streaming ? (
            <p className="animate-pulse text-xs text-muted-foreground">正在思考…</p>
          ) : null}

          {streaming && hasBlocks ? (
            <span
              aria-hidden="true"
              className="mt-1 inline-block h-3.5 w-1.5 animate-pulse rounded-[2px] bg-foreground align-middle"
            />
          ) : null}

          <MessageFooter className="gap-1 opacity-0 transition-opacity group-focus-within/message:opacity-100 group-hover/message:opacity-100">
            <span data-testid="run-time" className="tabular-nums">
              {formatClock(runTs)}
            </span>
            <Button variant="ghost" size="icon-xs" aria-label="复制这次运行的回复" onClick={() => void copy()}>
              {copied ? <Check /> : <Copy />}
            </Button>
          </MessageFooter>
        </MessageContent>
      </Message>
    </MessageGroup>
  );
}
