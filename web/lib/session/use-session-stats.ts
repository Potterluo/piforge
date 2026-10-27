"use client";

import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api/client";
import { qk } from "@/lib/session/queries";

/**
 * Context-window usage for the meter under the session header.
 *
 * Five seconds only while a run is in flight: idle sessions do not consume
 * context, and polling them would keep the sandbox warm for no reason.
 */
export function useSessionStats(sessionId: string, running = true) {
  return useQuery({
    queryKey: qk.stats(sessionId),
    queryFn: () => api.sessions.stats(sessionId),
    enabled: sessionId.length > 0,
    refetchInterval: running ? 5_000 : false,
    staleTime: 4_000,
    retry: 1,
  });
}
