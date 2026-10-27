"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import { ApiError } from "@/lib/types";

/**
 * One QueryClient per browser session.
 *
 * The API is a thin proxy over a Go control plane: a 4xx means the request
 * itself is wrong (bad id, unauthenticated, forbidden) and retrying it only
 * delays the error state the user needs to see. Only 5xx and network failures
 * are retried, twice, because those are the ones that recover on their own.
 */
function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: (failureCount, error) => {
          if (error instanceof ApiError && error.status < 500) return false;
          return failureCount < 2;
        },
        staleTime: 5_000,
        refetchOnWindowFocus: false,
      },
      mutations: { retry: false },
    },
  });
}

export function QueryProvider({ children }: { children: ReactNode }) {
  const [client] = useState(createQueryClient);

  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
