"use client";

import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { toast } from "sonner";

import { api, eventsUrl } from "@/lib/api/client";
import type { PfEvent } from "@/lib/types";
import { initialSessionState, sessionReducer, type SessionState } from "@/lib/session/reduce";

export type StreamStatus = "connecting" | "open" | "reconnecting" | "offline";

export interface SessionEventsResult {
  state: SessionState;
  status: StreamStatus;
  /** Pi's current leaf entry, kept current by every resync; branch UI highlights it. */
  leafEntryId: string | null;
}

const MAX_RESYNCS = 2;
const BASE_DELAY_MS = 1_000;
const MAX_DELAY_MS = 15_000;

/**
 * Every named event the server can emit.
 *
 * `EventSource.onmessage` only fires for records WITHOUT an `event:` field. The
 * control plane labels each record with its type (`event: message.delta`), so
 * `onmessage` alone receives nothing — every type must be subscribed
 * explicitly. Missing one means the UI silently stops updating for that event;
 * that is exactly how the run indicator once got stuck on "settled".
 */
export const SSE_EVENT_TYPES = [
  "run.started",
  "run.settled",
  "run.retry",
  "run.aborted",
  "turn.started",
  "turn.ended",
  "message.started",
  "message.delta",
  "message.final",
  "tool.started",
  "tool.progress",
  "tool.ended",
  "queue.updated",
  "context.compaction",
  "context.usage",
  "pty.stream",
  "platform.error",
] as const;

function parseEvent(raw: string): PfEvent | null {
  try {
    const value = JSON.parse(raw) as unknown;
    if (!value || typeof value !== "object") return null;
    const event = value as Partial<PfEvent>;
    if (typeof event.seq !== "number" || typeof event.type !== "string") return null;
    return event as PfEvent;
  } catch {
    return null;
  }
}

/**
 * Subscribes to a session's SSE stream and keeps a render model in sync.
 *
 * `EventSource` only replays automatically on its *own* reconnect, using the
 * `Last-Event-ID` header it remembers. We close and recreate the source on
 * every failure so the retry delay is ours, which means the cursor has to be
 * passed explicitly as `?after_seq=` — otherwise every reconnect would replay
 * the whole session from the beginning.
 *
 * A seq gap means events were lost while we were connected (a bridge restart,
 * a dropped frame). There is no partial repair for that, so the hook drops the
 * local transcript, re-reads `entries()` to confirm the session still exists
 * and picks up Pi's leaf, then replays the event log from zero.
 */
export function useSessionEvents(sessionId: string | null): SessionEventsResult {
  const [state, dispatch] = useReducer(sessionReducer, initialSessionState);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [leafEntryId, setLeafEntryId] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);

  const lastSeqRef = useRef(0);
  const resyncsRef = useRef(0);
  const resyncingRef = useRef(false);

  const resync = useCallback(async () => {
    if (!sessionId || resyncingRef.current) return;
    resyncingRef.current = true;
    try {
      const { leaf_id } = await api.sessions.entries(sessionId);
      setLeafEntryId(leaf_id);
      lastSeqRef.current = 0;
      dispatch({ type: "reset" });
      setGeneration((value) => value + 1);
    } catch (error) {
      setStatus("offline");
      toast.error("会话事件流需要刷新", {
        description: error instanceof Error ? error.message : "无法读取会话记录",
      });
    } finally {
      resyncingRef.current = false;
    }
  }, [sessionId]);

  useEffect(() => {
    if (!state.needsResync) {
      // A gap that has been repaired means the stream is healthy again. Without this
      // reset, two resyncs early in a session latched the badge at 已断开 for the rest
      // of the page's life while events kept arriving.
      resyncsRef.current = 0;
      return;
    }
    if (resyncsRef.current >= MAX_RESYNCS) {
      // Out of automatic repairs: say so once, and let the reconnect path (which sets
      // its own status) take over rather than declaring the stream dead forever.
      setStatus("offline");
      return;
    }
    resyncsRef.current += 1;
    void resync();
  }, [state.needsResync, resync]);

  useEffect(() => {
    // No session yet (the sidecar dock mounts before one exists): subscribing to an
    // empty id opened `/v1/sessions/events`, which 404'd and retried with backoff.
    if (!sessionId) {
      const timer = window.setTimeout(() => setStatus("connecting"), 0);
      return () => window.clearTimeout(timer);
    }
    if (typeof EventSource === "undefined") {
      // Deferred so this stays a subscription effect, not a render-time
      // cascading update (and so the SSR pass sees "connecting").
      const timer = window.setTimeout(() => setStatus("offline"), 0);
      return () => window.clearTimeout(timer);
    }

    let disposed = false;
    let source: EventSource | null = null;
    let timer: number | undefined;
    let attempt = 0;

    const connect = () => {
      if (disposed) return;
      setStatus(attempt === 0 ? "connecting" : "reconnecting");
      source = new EventSource(eventsUrl(sessionId, lastSeqRef.current));

      source.onopen = () => {
        attempt = 0;
        setStatus("open");
      };

      const handle = (message: MessageEvent<string>) => {
        const parsed = parseEvent(message.data);
        if (!parsed) return;
        // Anything arriving proves the stream works: a badge that still says 已断开
        // while events land is worse than no badge at all.
        resyncsRef.current = 0;
        setStatus((current) => (current === "open" ? current : "open"));

        // Prefer the SSE id when the server sent one: it is what the browser
        // would replay with, so the two cursors must not disagree.
        const reported = Number(message.lastEventId);
        const event = Number.isFinite(reported) && reported > 0 ? { ...parsed, seq: reported } : parsed;
        if (event.seq <= lastSeqRef.current) return;

        lastSeqRef.current = event.seq;
        dispatch({ type: "event", event });
      };

      // Named events carry their type in the `event:` field, so they never
      // reach `onmessage`; subscribe to each one. `onmessage` stays as a
      // fallback for unnamed records (and for proxies that strip the label).
      for (const type of SSE_EVENT_TYPES) {
        source.addEventListener(type, handle as EventListener);
      }
      source.onmessage = handle;

      source.onerror = () => {
        if (disposed) return;
        source?.close();
        source = null;
        attempt += 1;
        setStatus("reconnecting");
        const delay = Math.min(BASE_DELAY_MS * 2 ** (attempt - 1), MAX_DELAY_MS);
        timer = window.setTimeout(connect, delay);
      };
    };

    connect();

    return () => {
      disposed = true;
      source?.close();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [sessionId, generation]);

  return { state, status, leafEntryId };
}
