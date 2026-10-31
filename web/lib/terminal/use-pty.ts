"use client";

import * as React from "react";

/**
 * Live PTY session over the control plane's WebSocket relay.
 *
 * The master side of the pty lives inside the sandbox; this hook is only a byte
 * relay plus a resume cursor. Two details matter:
 *
 *  - every reconnect passes `from_offset`, which is the absolute byte offset the
 *    server reported as `end`. The server replays its ring buffer from there,
 *    so a dropped connection never leaves a hole in the scrollback (PRD FR-10.3);
 *  - keystrokes go out as binary frames, control messages (resize, ping) as
 *    JSON text frames, mirroring the server's protocol.
 *
 * Input is queued while there is no OPEN socket and flushed in order when one
 * opens. A WebSocket cannot send before its handshake completes (the browser
 * throws), so dropping those bytes was the old behaviour — and it is exactly how
 * a fast typist lost the first characters of a command ("node calc.test.js"
 * arriving as "t.js"). The queue holds strings, one per `send`, so the flush is
 * byte-for-byte and in order; nothing is coalesced and nothing waits for a round
 * trip once the socket is open.
 */

export type PtyStatus = "connecting" | "open" | "reconnecting" | "closed" | "error";

export interface PtySession {
  status: PtyStatus;
  /** Error text when status is "error". */
  error: string | null;
  /** Bytes received since the last reset; feed straight into xterm. */
  onData: (handler: (chunk: string) => void) => () => void;
  send: (data: string) => void;
  resize: (cols: number, rows: number) => void;
  reconnect: () => void;
  /** Ends the PTY itself (tab closed), as opposed to dropping the socket. */
  terminate: () => void;
}

interface Options {
  sessionId: string;
  tab?: string;
  enabled?: boolean;
}

const MAX_BACKOFF = 10_000;

/**
 * Builds the socket URL.
 *
 * Same-origin is the production shape: the app and the control plane sit behind
 * one reverse proxy that upgrades `/v1/*`. Next's own dev/prod server does not
 * forward upgrade requests, so local development and the e2e suite point the
 * socket straight at the Go server with NEXT_PUBLIC_PIFORGE_WS_BASE. Cookies
 * still flow because both hosts are the same site.
 */
function ptyUrl(sessionId: string, tab: string, fromOffset: number | null): string {
  const configured = process.env.NEXT_PUBLIC_PIFORGE_WS_BASE?.trim();
  let base: string;
  if (configured) {
    base = configured.replace(/\/$/, "");
  } else {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    base = `${proto}//${window.location.host}`;
  }
  const params = new URLSearchParams({ tab });
  if (fromOffset !== null && fromOffset > 0) params.set("from_offset", String(fromOffset));
  return `${base}/v1/sessions/${sessionId}/pty?${params.toString()}`;
}

export function usePtySession({ sessionId, tab = "main", enabled = true }: Options): PtySession {
  const [status, setStatus] = React.useState<PtyStatus>("connecting");
  const [error, setError] = React.useState<string | null>(null);
  const listeners = React.useRef(new Set<(chunk: string) => void>());
  const socket = React.useRef<WebSocket | null>(null);
  const offset = React.useRef<number | null>(null);
  const attempt = React.useRef(0);
  const timer = React.useRef<number | undefined>(undefined);
  const [generation, setGeneration] = React.useState(0);

  // Keystrokes typed while the socket was CONNECTING (first connect, or a
  // reconnect after a drop). One entry per `send` call, flushed in order.
  const pending = React.useRef<string[]>([]);
  // A resize requested before the socket was open: the window size would
  // otherwise stay at the server default and the shell would wrap at the wrong
  // column for the whole session.
  const pendingSize = React.useRef<{ cols: number; rows: number } | null>(null);
  // False once input can no longer reach a shell (tab closed, shell exited,
  // connection failed): queueing into that is a leak, not a buffer.
  const acceptInput = React.useRef(false);
  // The (session, tab) the queue belongs to; a different key must not inherit it.
  const queueKey = React.useRef(`${sessionId}:${tab}`);

  const flushPending = React.useCallback((ws: WebSocket) => {
    const queued = pending.current;
    pending.current = [];
    for (let index = 0; index < queued.length; index += 1) {
      try {
        ws.send(new TextEncoder().encode(queued[index]));
      } catch {
        // The socket died mid-flush. Put the unsent tail back in front of
        // anything typed since, so the next connection writes it in order.
        pending.current = queued.slice(index).concat(pending.current);
        return;
      }
    }
    const size = pendingSize.current;
    if (size) {
      pendingSize.current = null;
      try {
        ws.send(JSON.stringify({ type: "resize", cols: size.cols, rows: size.rows }));
      } catch {
        pendingSize.current = size;
      }
    }
  }, []);

  const onData = React.useCallback((handler: (chunk: string) => void) => {
    listeners.current.add(handler);
    return () => {
      listeners.current.delete(handler);
    };
  }, []);

  React.useEffect(() => {
    if (!enabled || typeof window === "undefined") {
      acceptInput.current = false;
      setStatus("closed");
      return () => {};
    }

    // A new session/tab is a new shell: text typed for the previous one is not
    // input for this one.
    const key = `${sessionId}:${tab}`;
    if (queueKey.current !== key) {
      queueKey.current = key;
      pending.current = [];
      pendingSize.current = null;
    }
    acceptInput.current = true;

    let disposed = false;

    const connect = () => {
      if (disposed) return;
      setStatus(attempt.current === 0 ? "connecting" : "reconnecting");

      let ws: WebSocket;
      try {
        ws = new WebSocket(ptyUrl(sessionId, tab, offset.current));
      } catch (err) {
        acceptInput.current = false;
        setError(err instanceof Error ? err.message : "无法建立终端连接");
        setStatus("error");
        return;
      }
      ws.binaryType = "arraybuffer";
      socket.current = ws;

      ws.onopen = () => {
        if (disposed) return;
        attempt.current = 0;
        // Flush before flipping the status: the server buffers these frames
        // until its read loop starts, so nothing typed early can be lost, and a
        // keystroke typed after this still goes straight out.
        flushPending(ws);
        setStatus("open");
        setError(null);
      };

      ws.onmessage = (event: MessageEvent<string | ArrayBuffer>) => {
        if (typeof event.data === "string") {
          try {
            const message = JSON.parse(event.data) as {
              type?: string;
              end?: number;
              message?: string;
            };
            if (message.type === "ready" && typeof message.end === "number") {
              // The ring's current end: from here the server streams live.
              offset.current = message.end;
            } else if (message.type === "exit") {
              acceptInput.current = false;
              setStatus("closed");
            } else if (message.type === "error") {
              acceptInput.current = false;
              setError(message.message ?? "终端错误");
              setStatus("error");
            }
          } catch {
            // A non-JSON text frame is still terminal output.
            for (const handler of listeners.current) handler(event.data);
          }
          return;
        }
        const chunk = new TextDecoder().decode(new Uint8Array(event.data));
        for (const handler of listeners.current) handler(chunk);
      };

      ws.onclose = (event) => {
        socket.current = null;
        if (disposed) return;
        if (event.code === 1000) {
          // A clean close is a deliberate end of this shell (tab closed, shell
          // exited). There is nothing left to type into.
          acceptInput.current = false;
          setStatus("closed");
          return;
        }
        // A dropped connection is not an ended shell: keep accepting input so
        // keystrokes typed during the backoff are flushed on the next socket.
        attempt.current += 1;
        setStatus("reconnecting");
        const delay = Math.min(500 * 2 ** (attempt.current - 1), MAX_BACKOFF);
        timer.current = window.setTimeout(connect, delay);
      };

      ws.onerror = () => {
        // `onclose` follows and owns the retry decision.
      };
    };

    connect();

    return () => {
      disposed = true;
      acceptInput.current = false;
      pending.current = [];
      pendingSize.current = null;
      if (timer.current !== undefined) window.clearTimeout(timer.current);
      socket.current?.close(1000, "unmount");
      socket.current = null;
    };
  }, [sessionId, tab, enabled, generation]);

  const send = React.useCallback((data: string) => {
    if (!data) return;
    const ws = socket.current;
    if (ws?.readyState === WebSocket.OPEN) {
      // Straight to the wire: the open path must not gain a queue (or a tick)
      // of latency.
      ws.send(new TextEncoder().encode(data));
      return;
    }
    if (!acceptInput.current) return;
    // Not open yet: the handshake may still be in flight, or a reconnect may be
    // backing off. Hold the bytes; `onopen` writes them, in this order.
    pending.current.push(data);
  }, []);

  const resize = React.useCallback((cols: number, rows: number) => {
    if (cols < 1 || rows < 1) return;
    const ws = socket.current;
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "resize", cols, rows }));
      return;
    }
    // Remember the latest size; only the newest one matters when it opens.
    pendingSize.current = { cols, rows };
  }, []);

  const reconnect = React.useCallback(() => {
    setGeneration((value) => value + 1);
  }, []);

  // `close` is the protocol's "this shell is no longer wanted" control frame.
  // Closing the socket alone is what a dropped connection looks like, and the
  // server deliberately keeps the PTY alive for that (FR-10.3). Closing a tab
  // must mean the shell, not the connection.
  const terminate = React.useCallback(() => {
    // This shell is going away: buffered keystrokes have no destination, and
    // flushing them into a future socket would type into a shell the user
    // already closed.
    acceptInput.current = false;
    pending.current = [];
    pendingSize.current = null;
    const ws = socket.current;
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "close" }));
    }
    ws?.close(1000, "tab closed");
    socket.current = null;
    setStatus("closed");
  }, []);

  return { status, error, onData, send, resize, reconnect, terminate };
}
