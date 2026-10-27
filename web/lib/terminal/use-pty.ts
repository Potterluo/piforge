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

  const onData = React.useCallback((handler: (chunk: string) => void) => {
    listeners.current.add(handler);
    return () => {
      listeners.current.delete(handler);
    };
  }, []);

  React.useEffect(() => {
    if (!enabled || typeof window === "undefined") {
      setStatus("closed");
      return () => {};
    }

    let disposed = false;

    const connect = () => {
      if (disposed) return;
      setStatus(attempt.current === 0 ? "connecting" : "reconnecting");

      let ws: WebSocket;
      try {
        ws = new WebSocket(ptyUrl(sessionId, tab, offset.current));
      } catch (err) {
        setError(err instanceof Error ? err.message : "无法建立终端连接");
        setStatus("error");
        return;
      }
      ws.binaryType = "arraybuffer";
      socket.current = ws;

      ws.onopen = () => {
        attempt.current = 0;
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
              setStatus("closed");
            } else if (message.type === "error") {
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
          setStatus("closed");
          return;
        }
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
      if (timer.current !== undefined) window.clearTimeout(timer.current);
      socket.current?.close(1000, "unmount");
      socket.current = null;
    };
  }, [sessionId, tab, enabled, generation]);

  const send = React.useCallback((data: string) => {
    const ws = socket.current;
    if (ws?.readyState !== WebSocket.OPEN) return;
    ws.send(new TextEncoder().encode(data));
  }, []);

  const resize = React.useCallback((cols: number, rows: number) => {
    const ws = socket.current;
    if (ws?.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: "resize", cols, rows }));
  }, []);

  const reconnect = React.useCallback(() => {
    setGeneration((value) => value + 1);
  }, []);

  return { status, error, onData, send, resize, reconnect };
}
