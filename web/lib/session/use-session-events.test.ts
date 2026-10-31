import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SSE_EVENT_TYPES, useSessionEvents } from "./use-session-events";

/**
 * Regression test for the bug that made the whole workbench look dead.
 *
 * The server labels every SSE record with `event: <type>`. `EventSource` only
 * routes unnamed records to `onmessage`, so a client that sets `onmessage`
 * alone receives nothing at all. These tests pin that every declared type is
 * subscribed, and that an event delivered through a named channel actually
 * reaches the reducer.
 */

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  readonly url: string;
  readonly listeners = new Map<string, Set<(event: MessageEvent<string>) => void>>();
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, handler: (event: MessageEvent<string>) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(handler);
  }

  removeEventListener(type: string, handler: (event: MessageEvent<string>) => void) {
    this.listeners.get(type)?.delete(handler);
  }

  close() {
    this.closed = true;
  }

  emit(type: string, data: unknown, id = "1") {
    const message = { data: JSON.stringify(data), lastEventId: id } as MessageEvent<string>;
    for (const handler of this.listeners.get(type) ?? []) handler(message);
    // Named events must NOT also hit onmessage; that would double-dispatch.
  }
}

function event(seq: number, type: string, payload: unknown = {}) {
  return {
    seq,
    session_id: "ses_test",
    branch_id: "ses_test",
    kind: "main",
    type,
    ts: new Date().toISOString(),
    payload,
    run_id: "run_1",
  };
}

/**
 * Waits out the opening replay window.
 *
 * A fresh connection replays the whole stored log in one burst, and events from
 * that burst are history: a replayed `run.started` must not make the header
 * claim a run (that is the fork bug). The hook only treats an event as live
 * once the stream has been quiet for REPLAY_GAP_MS, so tests that exercise live
 * behaviour wait for that.
 */
async function streamIsLive(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 400));
}

describe("useSessionEvents", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("subscribes to every named event type the server emits", async () => {
    renderHook(() => useSessionEvents("ses_test"));
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));

    const source = FakeEventSource.instances[0];
    for (const type of SSE_EVENT_TYPES) {
      expect(source.listeners.get(type)?.size ?? 0, `no listener for ${type}`).toBeGreaterThan(0);
    }
  });

  it("marks the run as running when a named run.started arrives", async () => {
    const { result } = renderHook(() => useSessionEvents("ses_test"));
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    await streamIsLive();

    FakeEventSource.instances[0].emit("run.started", event(1, "run.started"));

    await waitFor(() => expect(result.current.state.running).toBe(true));
  });

  it("does not claim a run from the opening replay of stored history", async () => {
    // A fork's copied transcript can end in the middle of a run. Replaying it
    // used to set `running`, and the header said 运行中 for minutes while the
    // status API said idle. History proves a run began, not that one is running.
    const { result } = renderHook(() => useSessionEvents("ses_test"));
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));

    FakeEventSource.instances[0].emit("run.started", event(1, "run.started"), "1");
    FakeEventSource.instances[0].emit(
      "tool.started",
      event(2, "tool.started", { tool_call_id: "t1", name: "bash", args: { command: "ls" } }),
      "2",
    );
    await new Promise((resolve) => setTimeout(resolve, 100));

    expect(result.current.state.running).toBe(false);
    expect(result.current.state.turns.length).toBeGreaterThan(0);
  });

  it("only ends the run on run.settled, never on turn.ended", async () => {
    const { result } = renderHook(() => useSessionEvents("ses_test"));
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    const source = FakeEventSource.instances[0];
    await streamIsLive();

    source.emit("run.started", event(1, "run.started"), "1");
    await waitFor(() => expect(result.current.state.running).toBe(true));

    source.emit("turn.ended", event(2, "turn.ended", { index: 1 }), "2");
    // Still running: retries, compaction and steering can follow a turn end.
    expect(result.current.state.running).toBe(true);

    source.emit("run.settled", event(3, "run.settled", { aborted: false }), "3");
    await waitFor(() => expect(result.current.state.running).toBe(false));
  });

  it("accumulates deltas and replaces them with the final message", async () => {
    const { result } = renderHook(() => useSessionEvents("ses_test"));
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    const source = FakeEventSource.instances[0];
    await streamIsLive();

    source.emit("run.started", event(1, "run.started"), "1");
    source.emit("message.started", event(2, "message.started", { role: "assistant" }), "2");
    source.emit(
      "message.delta",
      event(3, "message.delta", {
        message_id: "m1",
        block: "text",
        content_index: 0,
        delta: "Tests pass.",
      }),
      "3",
    );
    await waitFor(() => expect(result.current.state.turns.length).toBeGreaterThan(0));

    const partial = result.current.state.turns.at(-1)!;
    expect(JSON.stringify(partial)).toContain("Tests pass.");

    source.emit(
      "message.final",
      event(4, "message.final", {
        role: "assistant",
        text: "Tests pass. The race is in lru.go:88.",
        thinking: "",
        blocks: [{ kind: "text", text: "Tests pass. The race is in lru.go:88." }],
        stop_reason: "stop",
      }),
      "4",
    );

    await waitFor(() => {
      const turn = result.current.state.turns.at(-1)!;
      expect(JSON.stringify(turn)).toContain("lru.go:88");
    });
  });
});
