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
/**
 * The same state, or null outside the provider.
 *
 * Used by panels that merely benefit from knowing a run settled (the file tree
 * refetches then). Making those panels throw would mean a workspace panel can
 * only be rendered inside a live session, which is not true: they are also
 * mounted in isolation by tests and by the settings preview.
 */
export function useOptionalSessionEventState(): SessionEventsResult | null {
  return useContext(SessionEventsContext);
}