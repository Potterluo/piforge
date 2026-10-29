import type { ContextMarker, TimelineItem, Turn } from "@/lib/session/reduce";

/**
 * The transcript's rendering unit is a *run*, not a Pi message.
 *
 * Pi writes one assistant message per step of a run (thinking, tool call,
 * narration, final answer), so rendering a message per turn repeats the author,
 * the model and the clock six or seven times for one reply. Consecutive
 * assistant turns are therefore one visual block; a user turn, a marker or a
 * note ends it.
 */
export type TranscriptItem =
  | { kind: "turn"; key: string; ts: string; turn: Turn }
  | { kind: "run"; key: string; ts: string; turns: Turn[] }
  | { kind: "marker"; key: string; ts: string; marker: ContextMarker }
  | { kind: "note"; key: string; ts: string; text: string };

export function groupTranscript(items: TimelineItem[]): TranscriptItem[] {
  const out: TranscriptItem[] = [];
  for (const item of items) {
    if (item.kind === "turn" && item.turn.role === "assistant") {
      const last = out[out.length - 1];
      if (last?.kind === "run") {
        out[out.length - 1] = {
          ...last,
          // The run's clock is when it finished, which is the last assistant
          // message Pi wrote for it.
          ts: item.ts,
          turns: [...last.turns, item.turn],
        };
      } else {
        out.push({
          kind: "run",
          key: `run-${item.turn.id}`,
          ts: item.ts,
          turns: [item.turn],
        });
      }
      continue;
    }
    if (item.kind === "turn") {
      out.push({ kind: "turn", key: item.key, ts: item.ts, turn: item.turn });
    } else if (item.kind === "marker") {
      out.push({ kind: "marker", key: item.key, ts: item.ts, marker: item.marker });
    } else {
      out.push({ kind: "note", key: item.key, ts: item.ts, text: item.text });
    }
  }
  return out;
}
