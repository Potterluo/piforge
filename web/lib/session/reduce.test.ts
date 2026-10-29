import { beforeEach, describe, expect, it } from "vitest";

import { EV, type PfEvent } from "@/lib/types";
import {
  initialSessionState,
  reduceEvent,
  runningToolCount,
  selectTimeline,
  sessionReducer,
  type SessionState,
} from "@/lib/session/reduce";

let seq = 0;

function event(type: string, payload?: unknown, extra: Partial<PfEvent> = {}): PfEvent {
  seq += 1;
  return {
    seq: extra.seq ?? seq,
    session_id: "s1",
    branch_id: "b1",
    kind: "main",
    type,
    ts: new Date(Date.UTC(2026, 0, 1, 0, 0, extra.seq ?? seq)).toISOString(),
    payload,
    ...extra,
  };
}

function reduceAll(events: PfEvent[]): SessionState {
  return events.reduce(reduceEvent, initialSessionState);
}

function delta(
  seq: number,
  messageId: string,
  contentIndex: number,
  text: string,
  block: "text" | "thinking" = "text",
): PfEvent {
  return event(
    EV.msgDelta,
    { message_id: messageId, block, content_index: contentIndex, delta: text },
    { seq },
  );
}

beforeEach(() => {
  seq = 0;
});

describe("message.delta", () => {
  it("creates a text block on the first delta and appends on the next", () => {
    const state = reduceAll([delta(1, "m1", 0, "你"), delta(2, "m1", 0, "好")]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0]).toMatchObject({ id: "m1", role: "assistant", streaming: true });
    expect(state.turns[0].blocks).toEqual([{ kind: "text", text: "你好", index: 0 }]);
    expect(state.lastSeq).toBe(2);
    expect(state.streaming).toBe(true);
  });

  it("keeps blocks separate per content_index and orders them by index", () => {
    const state = reduceAll([
      delta(1, "m1", 1, "先说结论", "text"),
      delta(2, "m1", 0, "我先想", "thinking"),
    ]);

    expect(state.turns[0].blocks).toEqual([
      { kind: "thinking", text: "我先想", index: 0 },
      { kind: "text", text: "先说结论", index: 1 },
    ]);
  });

  it("ignores an empty delta", () => {
    const state = reduceAll([delta(1, "m1", 0, "")]);
    expect(state.turns).toHaveLength(0);
  });
});

describe("message.final", () => {
  it("keeps the provider error on the turn", () => {
    // A failed model call is an assistant message with no content. Without the
    // error the transcript can only draw an empty bubble, which is
    // indistinguishable from a broken page.
    const state = reduceAll([
      event(
        EV.msgFinal,
        {
          message_id: "m1",
          role: "assistant",
          blocks: [],
          stop_reason: "error",
          error: '502: provider "volc" is not available',
        },
        { seq: 1 },
      ),
    ]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0].error).toContain("not available");
  });

  it("clears the error when the message restarts", () => {
    const state = reduceAll([
      event(EV.msgFinal, { message_id: "m1", role: "assistant", blocks: [], error: "boom" }, { seq: 1 }),
      event(EV.msgStarted, { message_id: "m1", role: "assistant" }, { seq: 2 }),
    ]);

    expect(state.turns[0].error).toBeUndefined();
  });

  it("replaces the whole turn instead of appending", () => {
    const state = reduceAll([
      delta(1, "m1", 0, "流式输出"),
      event(
        EV.msgFinal,
        {
          message_id: "m1",
          role: "assistant",
          blocks: [{ kind: "text", text: "最终内容" }],
        },
        { seq: 2 },
      ),
    ]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0].blocks).toEqual([{ kind: "text", text: "最终内容", index: 0 }]);
  });

  it("does not turn streaming off", () => {
    const state = reduceAll([
      delta(1, "m1", 0, "流式"),
      event(EV.msgFinal, { message_id: "m1", blocks: [{ kind: "text", text: "终" }] }, { seq: 2 }),
    ]);

    expect(state.streaming).toBe(true);
    expect(state.turns[0].streaming).toBe(true);
  });

  it("preserves tool output that arrived before the final message", () => {
    const state = reduceAll([
      event(EV.toolStarted, { tool_call_id: "t1", name: "bash", args: { command: "go test" } }, { seq: 1 }),
      event(EV.toolProgress, { tool_call_id: "t1", chunk: "ok\n" }, { seq: 2 }),
      event(
        EV.toolEnded,
        { tool_call_id: "t1", is_error: false, duration_ms: 1200 },
        { seq: 3 },
      ),
      event(
        EV.msgFinal,
        {
          message_id: "turn-1",
          blocks: [
            { kind: "text", text: "跑完了" },
            { kind: "toolCall", tool_call: { id: "t1", name: "bash", arguments: { command: "go test" } } },
          ],
        },
        { seq: 4 },
      ),
    ]);

    const tools = state.turns[0].blocks.filter((block) => block.kind === "tool");
    expect(tools).toHaveLength(1);
    expect(tools[0]).toMatchObject({
      kind: "tool",
      id: "t1",
      chunk: "ok\n",
      done: true,
      durationMs: 1200,
    });
    expect(state.turns[0].blocks[0]).toMatchObject({ kind: "text", text: "跑完了" });
  });

  it("creates the turn when the final arrives without a prior delta", () => {
    const state = reduceAll([
      event(EV.msgFinal, { message_id: "m9", role: "user", blocks: [{ kind: "text", text: "你好" }] }, { seq: 1 }),
    ]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0].role).toBe("user");
    expect(state.turns[0].blocks).toEqual([{ kind: "text", text: "你好", index: 0 }]);
  });

  it("accepts a plain text payload as a fallback", () => {
    const state = reduceAll([
      event(EV.msgFinal, { message_id: "m2", text: "纯文本" }, { seq: 1 }),
    ]);
    expect(state.turns[0].blocks).toEqual([{ kind: "text", text: "纯文本", index: 0 }]);
  });
});

describe("tool events", () => {
  it("opens, streams and closes one tool block", () => {
    const state = reduceAll([
      event(EV.toolStarted, { tool_call_id: "t1", name: "bash", args: { command: "ls" } }, { seq: 1 }),
      event(EV.toolProgress, { tool_call_id: "t1", chunk: "a\n" }, { seq: 2 }),
      event(EV.toolProgress, { tool_call_id: "t1", chunk: "b\n" }, { seq: 3 }),
      event(EV.toolEnded, { tool_call_id: "t1", is_error: true, duration_ms: 250 }, { seq: 4 }),
    ]);

    expect(state.turns).toHaveLength(1);
    const block = state.turns[0].blocks[0];
    expect(block).toMatchObject({
      kind: "tool",
      id: "t1",
      name: "bash",
      chunk: "a\nb\n",
      done: true,
      isError: true,
      durationMs: 250,
    });
  });

  it("falls back to result_preview when no progress streamed", () => {
    const state = reduceAll([
      event(EV.toolStarted, { tool_call_id: "t1", name: "read" }, { seq: 1 }),
      event(EV.toolEnded, { tool_call_id: "t1", result_preview: "文件内容" }, { seq: 2 }),
    ]);

    const block = state.turns[0].blocks[0];
    expect(block).toMatchObject({ kind: "tool", chunk: "文件内容", done: true });
  });

  it("opens a block for a progress whose start was dropped", () => {
    const state = reduceAll([
      event(EV.toolProgress, { tool_call_id: "t9", name: "grep", chunk: "hit" }, { seq: 1 }),
    ]);

    expect(state.turns[0].blocks[0]).toMatchObject({
      kind: "tool",
      id: "t9",
      name: "grep",
      chunk: "hit",
      done: false,
    });
  });

  it("counts running tools", () => {
    const state = reduceAll([
      event(EV.toolStarted, { tool_call_id: "t1", name: "bash" }, { seq: 1 }),
      event(EV.toolStarted, { tool_call_id: "t2", name: "read" }, { seq: 2 }),
      event(EV.toolEnded, { tool_call_id: "t1" }, { seq: 3 }),
    ]);
    expect(runningToolCount(state)).toBe(1);
  });
});

describe("completion signal", () => {
  it("turn.ended does not stop streaming", () => {
    const state = reduceAll([
      event(EV.turnStarted, {}, { seq: 1 }),
      delta(2, "turn-1", 0, "说完了"),
      event(EV.turnEnded, {}, { seq: 3 }),
    ]);

    expect(state.streaming).toBe(true);
    expect(state.running).toBe(true);
    expect(state.turns[0].streaming).toBe(true);
  });

  it("run.settled is the only event that stops streaming", () => {
    const state = reduceAll([
      event(EV.turnStarted, {}, { seq: 1 }),
      delta(2, "turn-1", 0, "说完了"),
      event(EV.turnEnded, {}, { seq: 3 }),
      event(EV.runSettled, { aborted: false }, { seq: 4 }),
    ]);

    expect(state.streaming).toBe(false);
    expect(state.running).toBe(false);
    expect(state.turns[0].streaming).toBe(false);
  });
});

describe("sequence discipline", () => {
  it("ignores a duplicate seq", () => {
    const first = delta(1, "m1", 0, "一次");
    const state = reduceAll([first, first]);

    expect(state.turns[0].blocks[0]).toMatchObject({ text: "一次" });
    expect(state.lastSeq).toBe(1);
    expect(state.needsResync).toBe(false);
  });

  it("ignores an out-of-order seq", () => {
    const state = reduceAll([delta(2, "m1", 0, "后到"), delta(1, "m1", 0, "先到")]);
    expect(state.turns[0].blocks[0]).toMatchObject({ text: "后到" });
    expect(state.lastSeq).toBe(2);
  });

  it("flags a gap but still applies the event", () => {
    const state = reduceAll([delta(1, "m1", 0, "一"), delta(4, "m1", 0, "四")]);

    expect(state.needsResync).toBe(true);
    expect(state.lastSeq).toBe(4);
    expect(state.turns[0].blocks[0]).toMatchObject({ text: "一四" });
  });

  it("does not flag a gap for the first event of a resumed stream", () => {
    const state = reduceAll([delta(57, "m1", 0, "中途加入")]);
    expect(state.needsResync).toBe(false);
    expect(state.lastSeq).toBe(57);
  });
});

describe("queue, compaction and usage", () => {
  it("normalises the two queues with their behaviour labels", () => {
    const state = reduceAll([
      event(EV.queueChanged, { steering: ["改口"], follow_up: ["下一轮", "再下一轮"] }, { seq: 1 }),
    ]);

    expect(state.queue).toEqual([
      { text: "改口", behavior: "steer" },
      { text: "下一轮", behavior: "follow_up" },
      { text: "再下一轮", behavior: "follow_up" },
    ]);
  });

  it("stores a compaction marker", () => {
    const state = reduceAll([
      event(
        EV.compaction,
        {
          reason: "threshold",
          summary: "保留了最近的工具调用",
          tokens_before: 120000,
          estimated_tokens_after: 40000,
          first_kept_entry_id: "entry_71",
        },
        { seq: 1 },
      ),
    ]);

    expect(state.markers).toHaveLength(1);
    expect(state.markers[0]).toMatchObject({
      reason: "threshold",
      tokensBefore: 120000,
      tokensAfter: 40000,
      firstKeptEntryId: "entry_71",
    });
  });

  it("tracks live context usage", () => {
    const state = reduceAll([
      event(EV.contextUsage, { context_tokens: 1200, context_window: 8000, context_percent: 15 }, { seq: 1 }),
    ]);
    expect(state.usage).toEqual({ tokens: 1200, window: 8000, percent: 15 });
  });

  it("surfaces a platform error in the stream", () => {
    const state = reduceAll([
      event(EV.platformErr, { message: "bridge 断开" }, { seq: 1 }),
    ]);
    expect(state.error).toBe("bridge 断开");
  });
});

describe("runtime status", () => {
  it("marks running on run.started and opens a user turn on message.started", () => {
    const user = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgStarted, { message_id: "u1", role: "user" }, { seq: 2 }),
    ]);

    expect(user.running).toBe(true);
    expect(user.turns[0]).toMatchObject({ id: "u1", role: "user", streaming: false });
  });

  it("opens an assistant turn on message.started", () => {
    const state = reduceAll([event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 1 })]);
    expect(state.turns[0]).toMatchObject({ id: "a1", role: "assistant", streaming: true });
  });
});

describe("selectors", () => {
  it("keeps turn order and injects markers before later turns", () => {
    const events: PfEvent[] = [
      event(EV.msgStarted, { message_id: "u1", role: "user" }, { seq: 1, ts: "2026-01-01T00:00:01.000Z" }),
      event(EV.compaction, { reason: "manual" }, { seq: 2, ts: "2026-01-01T00:00:03.000Z" }),
      event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 3, ts: "2026-01-01T00:00:04.000Z" }),
    ];
    const state = reduceAll(events);
    const timeline = selectTimeline(state);

    expect(
      timeline.map((item) =>
        item.kind === "turn" ? item.turn.id : item.kind === "note" ? item.text : item.marker.reason,
      ),
    ).toEqual([
      "u1",
      "manual",
      "a1",
    ]);
  });

  it("appends markers that happened after the last turn", () => {
    const events: PfEvent[] = [
      event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 1, ts: "2026-01-01T00:00:01.000Z" }),
      event(EV.compaction, { reason: "overflow" }, { seq: 2, ts: "2026-01-01T00:00:05.000Z" }),
    ];
    const timeline = selectTimeline(reduceAll(events));
    expect(timeline.map((item) => item.kind)).toEqual(["turn", "marker"]);
  });
});

describe("normalizer payload shapes", () => {
  it("streams deltas without a message id into the active message", () => {
    const state = reduceAll([
      event(EV.msgStarted, { role: "assistant", provider: "fake", model: "m" }, { seq: 1 }),
      event(EV.msgDelta, { kind: "thinking", index: 0, delta: "想" }, { seq: 2 }),
      event(EV.msgDelta, { kind: "text", index: 1, delta: "你" }, { seq: 3 }),
      event(EV.msgDelta, { kind: "text", index: 1, delta: "好" }, { seq: 4 }),
    ]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0].blocks).toEqual([
      { kind: "thinking", text: "想", index: 0 },
      { kind: "text", text: "你好", index: 1 },
    ]);
  });

  it("does not split a stream into one turn per delta", () => {
    const state = reduceAll([
      event(EV.msgStarted, { role: "assistant" }, { seq: 1 }),
      ...Array.from({ length: 20 }, (_, i) =>
        event(EV.msgDelta, { kind: "text", index: 0, delta: String(i) }, { seq: i + 2 }),
      ),
    ]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0].blocks[0]).toMatchObject({
      text: "012345678910111213141516171819",
    });
  });

  it("falls back to the newest assistant turn for a delta with no cursor", () => {
    const state = reduceAll([
      event(EV.msgStarted, { role: "assistant" }, { seq: 1 }),
      event(EV.msgFinal, { role: "assistant", blocks: [{ kind: "text", text: "完成" }] }, { seq: 2 }),
      event(EV.msgDelta, { kind: "text", index: 0, delta: "补充" }, { seq: 3 }),
    ]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0].blocks[0]).toMatchObject({ text: "完成补充" });
  });

  it("ignores tool-argument deltas", () => {
    const state = reduceAll([
      event(EV.msgStarted, { role: "assistant" }, { seq: 1 }),
      event(EV.msgDelta, { kind: "toolArgs", index: 0, delta: '{"comm' }, { seq: 2 }),
    ]);

    expect(state.turns[0].blocks).toHaveLength(0);
  });

  it("does not create an empty turn for turn.started", () => {
    const state = reduceAll([event(EV.turnStarted, { index: 1 }, { seq: 1 })]);
    expect(state.turns).toHaveLength(0);
    expect(state.running).toBe(true);
  });

  it("reads tool progress from the normalizer's text field", () => {
    const state = reduceAll([
      event(EV.toolStarted, { tool_call_id: "t1", name: "bash", args: { command: "ls" } }, { seq: 1 }),
      event(EV.toolProgress, { tool_call_id: "t1", name: "bash", text: "a\n" }, { seq: 2 }),
      event(EV.toolEnded, { tool_call_id: "t1", text: "a\nb\n", duration_ms: 12 }, { seq: 3 }),
    ]);

    expect(state.turns[0].blocks[0]).toMatchObject({
      chunk: "a\nb\n",
      done: true,
      durationMs: 12,
    });
  });

  it("ignores the start half of a compaction and keeps the end", () => {
    const state = reduceAll([
      event(EV.compaction, { phase: "start", reason: "threshold" }, { seq: 1 }),
      event(EV.compaction, { phase: "end", reason: "threshold", summary: "压缩完成" }, { seq: 2 }),
    ]);

    expect(state.markers).toHaveLength(1);
    expect(state.markers[0]).toMatchObject({ summary: "压缩完成", reason: "threshold" });
  });

  it("derives context tokens from the nested usage object", () => {
    const state = reduceAll([
      event(
        EV.contextUsage,
        {
          usage: { input: 1000, output: 50, cacheRead: 500, cacheWrite: 0 },
          context_window: 10_000,
        },
        { seq: 1 },
      ),
    ]);

    expect(state.usage).toEqual({ tokens: 1500, window: 10_000, percent: 15 });
  });

  it("does not duplicate a tool block that message.final already declared", () => {
    const state = reduceAll([
      event(EV.msgStarted, { role: "assistant" }, { seq: 1 }),
      event(
        EV.msgFinal,
        {
          role: "assistant",
          text: "跑一下",
          blocks: [
            { kind: "text", index: 0, text: "跑一下" },
            { kind: "toolCall", index: 1, tool_call: { id: "t1", name: "bash", arguments: { command: "ls" } } },
          ],
        },
        { seq: 2 },
      ),
      event(EV.toolStarted, { tool_call_id: "t1", name: "bash", args: { command: "ls -la" } }, { seq: 3 }),
      event(EV.toolProgress, { tool_call_id: "t1", text: "a\n" }, { seq: 4 }),
      event(EV.toolEnded, { tool_call_id: "t1", text: "a\nb\n", duration_ms: 30 }, { seq: 5 }),
    ]);

    const tools = state.turns[0].blocks.filter((block) => block.kind === "tool");
    expect(tools).toHaveLength(1);
    expect(tools[0]).toMatchObject({
      id: "t1",
      name: "bash",
      args: { command: "ls -la" },
      chunk: "a\nb\n",
      done: true,
      durationMs: 30,
    });
  });

  it("clears the active message on final so the next message starts a turn", () => {
    const state = reduceAll([
      event(EV.msgStarted, { role: "assistant" }, { seq: 1 }),
      event(EV.msgDelta, { kind: "text", index: 0, delta: "第一" }, { seq: 2 }),
      event(EV.msgFinal, { role: "assistant", blocks: [{ kind: "text", text: "第一" }] }, { seq: 3 }),
      event(EV.msgStarted, { role: "assistant" }, { seq: 4 }),
      event(EV.msgDelta, { kind: "text", index: 0, delta: "第二" }, { seq: 5 }),
    ]);

    expect(state.turns).toHaveLength(2);
    expect(state.turns[1].blocks[0]).toMatchObject({ text: "第二" });
  });
});

describe("sessionReducer", () => {
  it("resets to the initial state", () => {
    const state = reduceAll([delta(1, "m1", 0, "x")]);
    expect(sessionReducer(state, { type: "reset" })).toEqual(initialSessionState);
  });

  it("clears the queue without touching the transcript", () => {
    const state = reduceAll([event(EV.queueChanged, { follow_up: ["x"] }, { seq: 1 })]);
    const cleared = sessionReducer(state, { type: "clear-queue" });
    expect(cleared.queue).toEqual([]);
    expect(cleared.lastSeq).toBe(1);
  });
});
describe("selectTimeline", () => {
  it("collapses repeated provider failures into one turn", () => {
    // Pi retries by itself; the transcript should say "failed, retried" once
    // rather than stack identical error boxes.
    const state = reduceAll([
      event(EV.msgFinal, { message_id: "m1", role: "assistant", blocks: [], error: "502: down" }, { seq: 1 }),
      event(EV.msgFinal, { message_id: "m2", role: "assistant", blocks: [], error: "502: down" }, { seq: 2 }),
      event(EV.msgFinal, { message_id: "m3", role: "assistant", blocks: [], error: "502: down" }, { seq: 3 }),
    ]);

    const timeline = selectTimeline(state);
    expect(timeline).toHaveLength(1);
    const item = timeline[0];
    expect(item.kind).toBe("turn");
    if (item.kind === "turn") {
      expect(item.turn.retries).toBe(2);
      expect(item.turn.error).toBe("502: down");
    }
  });

  it("keeps failures of different kinds apart", () => {
    const state = reduceAll([
      event(EV.msgFinal, { message_id: "m1", role: "assistant", blocks: [], error: "502" }, { seq: 1 }),
      event(EV.msgFinal, { message_id: "m2", role: "assistant", blocks: [], error: "429" }, { seq: 2 }),
    ]);

    expect(selectTimeline(state)).toHaveLength(2);
  });
});
describe("model attribution", () => {
  it("remembers which model produced the turn", () => {
    // A session can be pointed at a custom model id; the transcript has to say
    // what actually answered it.
    const state = reduceAll([
      event(EV.msgFinal, {
        message_id: "m1",
        role: "assistant",
        blocks: [{ kind: "text", text: "hi" }],
        provider: "piforge",
        model: "deepseek-v4-flash",
      }, { seq: 1 }),
    ]);

    // The gateway is an implementation detail: the composer says `volc/...`, so a
    // transcript that says `piforge/...` reads as a contradiction.
    expect(state.turns[0].model).toBe("deepseek-v4-flash");
  });

  it("keeps the model when a later message omits it", () => {
    const state = reduceAll([
      event(EV.msgFinal, { message_id: "m1", role: "assistant", blocks: [], provider: "p", model: "m" }, { seq: 1 }),
      event(EV.msgFinal, { message_id: "m1", role: "assistant", blocks: [{ kind: "text", text: "x" }] }, { seq: 2 }),
    ]);

    expect(state.turns[0].model).toBe("p/m");
  });
});
describe("an interrupted run", () => {
  // QA-14: stopping a run painted 模型调用失败 / "This operation was aborted" /
  // credential advice, plus a red "1 个失败" tool group. A stop is a normal
  // outcome, so the reducer must not record a failure for it.
  const abortedRun = () =>
    reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 2 }),
      event(
        EV.msgFinal,
        { message_id: "a1", role: "assistant", blocks: [{ kind: "text", text: "I'll run it" }] },
        { seq: 3 },
      ),
      event(
        EV.toolStarted,
        { tool_call_id: "t1", name: "bash", args: { command: "sleep 30" } },
        { seq: 4 },
      ),
      event(
        EV.toolEnded,
        { tool_call_id: "t1", name: "bash", is_error: true, text: "Command aborted" },
        { seq: 5 },
      ),
      event(EV.msgFinal, { message_id: "a2", role: "assistant", blocks: [] }, { seq: 6 }),
      event(EV.runAborted, { reason: "aborted" }, { seq: 7 }),
      event(EV.runSettled, { aborted: true }, { seq: 8 }),
    ]);

  it("marks the run as stopped instead of failing it", () => {
    const state = abortedRun();
    expect(state.aborted).toBe(true);
    expect(state.error).toBeNull();
    // No turn carries an error: the backend drops the abort message's error text
    // and nothing else may invent one.
    expect(state.turns.filter((turn) => turn.error)).toHaveLength(0);
  });

  it("does not report the aborted tool call as a failure", () => {
    const state = abortedRun();
    const tool = state.turns.flatMap((turn) => turn.blocks).find((block) => block.kind === "tool");
    expect(tool).toMatchObject({ id: "t1", done: true, aborted: true });
  });

  it("gives a tool the abort cut short a terminal, non-failing state", () => {
    // The peer may never report the tool's end when the stop lands: without this
    // the row stays "运行中" forever, which is worse than the red group it replaced.
    const state = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 2 }),
      event(EV.toolStarted, { tool_call_id: "t1", name: "bash", args: {} }, { seq: 3 }),
      event(EV.runAborted, {}, { seq: 4 }),
      event(EV.runSettled, { aborted: true }, { seq: 5 }),
    ]);

    const tool = state.turns.flatMap((turn) => turn.blocks).find((block) => block.kind === "tool");
    expect(tool).toMatchObject({ done: true, aborted: true });
    expect(tool?.kind === "tool" && tool.isError).toBeFalsy();
    expect(runningToolCount(state)).toBe(0);
  });

  it("drops the abort echo Pi reports as a provider error", () => {
    // Real Pi: stopReason "error" + "This operation was aborted". The control
    // plane strips it now, but a run that was stopped must never paint a failure
    // even if an error text arrives first.
    const state = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 2 }),
      event(
        EV.msgFinal,
        {
          message_id: "a1",
          role: "assistant",
          blocks: [],
          stop_reason: "error",
          error: "This operation was aborted",
        },
        { seq: 3 },
      ),
      event(EV.runAborted, { reason: "aborted" }, { seq: 4 }),
      event(EV.runSettled, { aborted: true }, { seq: 5 }),
    ]);

    expect(state.aborted).toBe(true);
    expect(state.turns.filter((turn) => turn.error)).toHaveLength(0);
  });

  it("keeps a real failure from earlier in the same run", () => {
    const state = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgStarted, { message_id: "a0", role: "assistant" }, { seq: 2 }),
      event(
        EV.msgFinal,
        { message_id: "a0", role: "assistant", blocks: [], stop_reason: "error", error: "502: upstream down" },
        { seq: 3 },
      ),
      event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 4 }),
      event(EV.runAborted, {}, { seq: 5 }),
      event(EV.runSettled, { aborted: true }, { seq: 6 }),
    ]);

    expect(state.turns.filter((turn) => turn.error)).toHaveLength(1);
    expect(state.turns.find((turn) => turn.error)?.error).toContain("502");
  });

  it("folds a tool result into the trace instead of the transcript", () => {
    // "Command aborted" used to render under the PiForge label as if the model
    // had said it: a tool result is not an assistant message.
    const state = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgStarted, { message_id: "t1", role: "toolResult" }, { seq: 2 }),
      event(
        EV.msgFinal,
        { message_id: "t1", role: "toolResult", text: "Command aborted", blocks: [{ kind: "text", text: "Command aborted" }] },
        { seq: 3 },
      ),
      event(EV.runSettled, { aborted: false }, { seq: 4 }),
    ]);

    expect(state.turns).toHaveLength(0);
    expect(selectTimeline(state)).toHaveLength(0);
  });

  it("clears the stopped marker when the next run starts", () => {
    const next = reduceAll([
      event(EV.runAborted, {}, { seq: 1 }),
      event(EV.runSettled, { aborted: true }, { seq: 2 }),
      event(EV.runStarted, {}, { seq: 3 }),
    ]);
    expect(next.aborted).toBe(false);
  });
});

describe("a retried provider failure", () => {
  // QA-14 (minor): a transient gateway 502 that the turn recovered from stayed in
  // the transcript as a terminal 模型调用失败.
  it("drops the failed attempt once the same run answers", () => {
    const state = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgFinal, { message_id: "m1", role: "assistant", blocks: [], error: "502: upstream_error" }, { seq: 2 }),
      event(
        EV.msgFinal,
        { message_id: "m2", role: "assistant", blocks: [{ kind: "text", text: "可以" }] },
        { seq: 3 },
      ),
      event(EV.runSettled, { aborted: false }, { seq: 4 }),
    ]);

    expect(state.turns.filter((turn) => turn.error)).toHaveLength(0);
    expect(selectTimeline(state)).toHaveLength(1);
  });

  it("keeps a failure that never recovered", () => {
    const state = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgFinal, { message_id: "m1", role: "assistant", blocks: [], error: "502: upstream_error" }, { seq: 2 }),
      event(EV.runSettled, { aborted: false }, { seq: 3 }),
    ]);

    expect(state.turns).toHaveLength(1);
    expect(state.turns[0].error).toContain("502");
  });

  it("drops it when the retry landed after a new agent run started", () => {
    // Real Pi shape (QA-14 evidence): the failure settles, then run.retry starts
    // a *new* agent run whose answer is a second assistant turn. The failed turn
    // is therefore before the answer, not trailing.
    const state = reduceAll([
      event(EV.runStarted, {}, { seq: 1 }),
      event(EV.msgStarted, { message_id: "u1", role: "user" }, { seq: 2 }),
      event(EV.msgFinal, { message_id: "u1", role: "user", text: "hi", blocks: [{ kind: "text", text: "hi" }] }, { seq: 3 }),
      event(EV.msgStarted, { message_id: "a1", role: "assistant" }, { seq: 4 }),
      event(
        EV.msgFinal,
        { message_id: "a1", role: "assistant", blocks: [], error: '502: {"code":"upstream_error"}' },
        { seq: 5 },
      ),
      event(EV.turnEnded, {}, { seq: 6 }),
      event(EV.runRetry, { phase: "start", attempt: 1 }, { seq: 7 }),
      event(EV.runStarted, {}, { seq: 8 }),
      event(EV.msgStarted, { message_id: "a2", role: "assistant" }, { seq: 9 }),
      event(
        EV.msgFinal,
        { message_id: "a2", role: "assistant", blocks: [{ kind: "text", text: "可以" }] },
        { seq: 10 },
      ),
      event(EV.runSettled, { aborted: false }, { seq: 11 }),
    ]);

    expect(state.turns.filter((turn) => turn.error)).toHaveLength(0);
    const timeline = selectTimeline(state);
    expect(timeline.map((item) => (item.kind === "turn" ? item.turn.role : item.kind))).toEqual([
      "user",
      "assistant",
    ]);
  });
});

// A push is a fact about the session that happened outside the conversation. Recording
// it as an event and never rendering it is a claim nobody can check.
it("turns git events into timeline notes", () => {
  const state = reduceAll([
    event(EV.msgStarted, { message_id: "u1", role: "user" }, { seq: 1, ts: "2026-01-01T00:00:01.000Z" }),
    event("git.pushed" as never, { detail: "main abc1234" }, { seq: 2, ts: "2026-01-01T00:00:02.000Z" }),
    event("git.pulled" as never, { detail: "main def5678" }, { seq: 3, ts: "2026-01-01T00:00:03.000Z" }),
  ]);
  const notes = selectTimeline(state).filter((item) => item.kind === "note");
  expect(notes.map((item) => (item.kind === "note" ? item.text : ""))).toEqual([
    "已推送 main abc1234",
    "已拉取 main def5678",
  ]);
});