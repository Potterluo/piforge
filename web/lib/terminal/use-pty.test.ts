import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { usePtySession } from "./use-pty";

/**
 * The terminal's WebSocket send path, without a browser.
 *
 * The acceptance pass typed `node calc.test.js; echo EXIT=$?` at 25 ms/char and
 * the shell received `t.js; echo EXIT=$?`. The relay was not at fault (a direct
 * socket probe delivered all 26 bytes at 25 ms/char and as a burst); the client
 * threw every keystroke away while its socket was not OPEN. These tests pin the
 * contract: queue while connecting, flush in order, never coalesce, and never
 * add a round-trip to a keystroke sent on an open socket.
 */

class FakeSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  static instances: FakeSocket[] = [];

  readyState = FakeSocket.CONNECTING;
  binaryType = "blob";
  url: string;
  sent: Uint8Array[] = [];
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }

  send(data: Uint8Array | string): void {
    if (this.readyState !== FakeSocket.OPEN) throw new Error("InvalidStateError");
    if (typeof data === "string") throw new Error("expected a binary frame");
    this.sent.push(data);
  }

  open(): void {
    this.readyState = FakeSocket.OPEN;
    this.onopen?.(new Event("open"));
  }

  close(code = 1000, reason = ""): void {
    this.readyState = FakeSocket.CLOSED;
    this.onclose?.({ code, reason } as CloseEvent);
  }

  /** Drops the connection the way a network failure does (no clean close). */
  drop(): void {
    this.readyState = FakeSocket.CLOSED;
    this.onclose?.({ code: 1006, reason: "" } as CloseEvent);
  }

  texts(): string[] {
    const decoder = new TextDecoder();
    return this.sent.map((chunk) => decoder.decode(chunk));
  }
}

const decodeAll = (socket: FakeSocket) => socket.texts();

describe("usePtySession input buffer", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("queues every keystroke while connecting and flushes them in order, one frame each", () => {
    const { result } = renderHook(() => usePtySession({ sessionId: "ses_1" }));

    expect(FakeSocket.instances).toHaveLength(1);
    const socket = FakeSocket.instances[0];
    expect(socket.readyState).toBe(FakeSocket.CONNECTING);

    const typed = "node calc.test.js; echo EXIT=$?";
    act(() => {
      for (const key of typed) result.current.send(key);
    });
    // Nothing may be lost, and nothing may be written to a socket that is not
    // open (the browser would throw).
    expect(socket.sent).toHaveLength(0);

    act(() => socket.open());

    // In order, and one frame per keystroke: coalescing would reorder under a
    // pasted chunk and hides exactly the bug this guards.
    expect(decodeAll(socket)).toEqual([...typed]);
    expect(FakeSocket.instances[0].sent).toHaveLength(typed.length);
  });

  it("sends immediately once open, without adding a round-trip", () => {
    const { result } = renderHook(() => usePtySession({ sessionId: "ses_2" }));
    const socket = FakeSocket.instances[0];
    act(() => socket.open());

    act(() => result.current.send("l"));
    act(() => result.current.send("s"));

    expect(decodeAll(socket)).toEqual(["l", "s"]);
    // No queue, no timer: the frame is on the wire before send() returns.
    expect(socket.sent).toHaveLength(2);
  });

  it("buffers input typed while reconnecting and flushes it after the retry", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => usePtySession({ sessionId: "ses_3" }));
    const first = FakeSocket.instances[0];
    act(() => first.open());

    act(() => first.drop());
    expect(result.current.status).toBe("reconnecting");

    act(() => {
      result.current.send("e");
      result.current.send("c");
      result.current.send("h");
      result.current.send("o");
    });
    expect(first.sent).toHaveLength(0);

    // The hook backs off 500 ms before its first retry.
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(FakeSocket.instances).toHaveLength(2);
    const second = FakeSocket.instances[1];
    expect(second.readyState).toBe(FakeSocket.CONNECTING);

    act(() => second.open());
    expect(decodeAll(second)).toEqual(["e", "c", "h", "o"]);
  });

  it("drops the buffer when the terminal itself is closed", () => {
    const { result } = renderHook(() => usePtySession({ sessionId: "ses_4" }));
    const socket = FakeSocket.instances[0];

    act(() => {
      result.current.send("x");
      result.current.terminate();
    });
    expect(result.current.status).toBe("closed");

    // A late open of the abandoned socket must not resurrect the text: the tab
    // is gone, and typing into it would be input for a shell nobody sees.
    act(() => socket.open());
    expect(socket.sent).toHaveLength(0);
  });
});
