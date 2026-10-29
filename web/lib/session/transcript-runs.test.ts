import { describe, expect, it } from "vitest";

import type { TimelineItem, Turn } from "./reduce";
import { groupTranscript } from "./transcript-runs";

function turn(id: string, role: Turn["role"]): Turn {
  return { id, role, blocks: [], streaming: false, ts: "2026-01-01T00:00:00Z" };
}

function item(t: Turn): TimelineItem {
  return { kind: "turn", key: t.id, ts: t.ts, turn: t };
}

describe("groupTranscript", () => {
  it("renders one assistant block per run, not one per Pi message", () => {
    const items: TimelineItem[] = [
      item(turn("u1", "user")),
      item(turn("a1", "assistant")),
      item(turn("a2", "assistant")),
      item(turn("a3", "assistant")),
      item(turn("u2", "user")),
      item(turn("a4", "assistant")),
    ];

    const grouped = groupTranscript(items);
    expect(grouped.map((entry) => entry.kind)).toEqual(["turn", "run", "turn", "run"]);
    const firstRun = grouped[1];
    expect(firstRun.kind === "run" && firstRun.turns.map((t) => t.id)).toEqual(["a1", "a2", "a3"]);
    // The run's clock is its last message: that is when the answer finished.
    expect(firstRun.ts).toBe("2026-01-01T00:00:00Z");
  });

  it("does not merge across a marker or a user turn", () => {
    const items: TimelineItem[] = [
      item(turn("a1", "assistant")),
      {
        kind: "note",
        key: "n1",
        ts: "2026-01-01T00:00:00Z",
        text: "已推送",
      },
      item(turn("a2", "assistant")),
    ];

    expect(groupTranscript(items).map((entry) => entry.kind)).toEqual(["run", "note", "run"]);
  });
});
