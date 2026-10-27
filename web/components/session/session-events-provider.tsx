"use client";

import { createContext, useContext, type ReactNode } from "react";

import {
  useSessionEvents,
  type SessionEventsResult,
} from "@/lib/session/use-session-events";

const SessionEventsContext = createContext<SessionEventsResult | null>(null);

/**
 * One SSE subscription per session, shared by the header, the stream and the
 * composer. Opening a second EventSource for the same session would double the
 * replay work and could apply deltas twice.
 */
export function SessionEventsProvider({
  sessionId,
  children,
}: {
  sessionId: string;
  children: ReactNode;
}) {
  const value = useSessionEvents(sessionId);
  return <SessionEventsContext.Provider value={value}>{children}</SessionEventsContext.Provider>;
}

export function useSessionEventState(): SessionEventsResult {
  const value = useContext(SessionEventsContext);
  if (!value) {
    throw new Error("useSessionEventState 必须在 SessionEventsProvider 内使用");
  }
  return value;
}
