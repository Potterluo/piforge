"use client";

import { useEffect, useState } from "react";

import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceDescription,
  TraceIcon,
  TraceLine,
  TraceTitle,
} from "@/components/ui/trace";
import { Brain } from "lucide-react";

/**
 * One reasoning block. Collapsed by default (PRD §10.4.1) with an elapsed
 * counter, because a long silence is the thing that makes users think the
 * agent hung. The title shimmers while the block is still growing — the
 * shimmer itself lives in the Trace primitive.
 *
 * The block carries no timestamp on the wire, so the clock starts when the
 * component first renders, which is the moment its first delta arrived.
 */
export function ThinkingTrace({ text, running }: { text: string; running: boolean }) {
  const [seconds, setSeconds] = useState(0);

  useEffect(() => {
    if (!running) return;
    const startedAt = Date.now();
    const timer = window.setInterval(() => {
      setSeconds(Math.floor((Date.now() - startedAt) / 1_000));
    }, 1_000);
    return () => window.clearInterval(timer);
  }, [running]);

  return (
    <Trace>
      <TraceCollapsible>
        <TraceCollapsibleTrigger>
          <TraceLine status={running ? "running" : undefined}>
            <TraceIcon>
              <Brain />
            </TraceIcon>
            <TraceTitle>思考</TraceTitle>
            <TraceDescription>{seconds > 0 ? `${seconds}s` : running ? "…" : ""}</TraceDescription>
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent>
          <p className="max-h-80 overflow-y-auto text-xs whitespace-pre-wrap">{text}</p>
        </TraceCollapsibleContent>
      </TraceCollapsible>
    </Trace>
  );
}
