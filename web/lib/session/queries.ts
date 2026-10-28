"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, type CreateProjectInput, type CreateSessionInput, type SendMessageInput } from "@/lib/api/client";
import type { Session } from "@/lib/types";

/** Query keys live in one place so invalidation cannot drift from fetching. */
export const qk = {
  me: ["me"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["project", id] as const,
  sessions: (projectId: string) => ["sessions", projectId] as const,
  session: (id: string) => ["session", id] as const,
  stats: (id: string) => ["stats", id] as const,
  providers: ["providers"] as const,
  skills: ["skills"] as const,
  mcp: ["mcp"] as const,
  users: ["admin", "users"] as const,
};

/** The signed-in user. A 401 here is the app's "not logged in" signal. */
export function useCurrentUser() {
  return useQuery({
    queryKey: qk.me,
    queryFn: () => api.auth.me(),
    staleTime: 60_000,
    retry: false,
  });
}

export function useProjects() {
  return useQuery({
    queryKey: qk.projects,
    queryFn: () => api.projects.list(),
    staleTime: 15_000,
  });
}

export function useProject(projectId: string) {
  return useQuery({
    queryKey: qk.project(projectId),
    queryFn: () => api.projects.get(projectId),
    enabled: projectId.length > 0,
  });
}

export function useProjectSessions(projectId: string | undefined) {
  return useQuery({
    queryKey: qk.sessions(projectId ?? "none"),
    queryFn: () => api.sessions.list({ project: projectId }),
    enabled: Boolean(projectId),
    staleTime: 10_000,
  });
}

export function useSession(sessionId: string) {
  return useQuery({
    queryKey: qk.session(sessionId),
    queryFn: () => api.sessions.get(sessionId),
    enabled: sessionId.length > 0,
  });
}

export function useProviders() {
  return useQuery({
    queryKey: qk.providers,
    queryFn: () => api.providers.list(),
    staleTime: 30_000,
  });
}

export function useCreateProject() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateProjectInput) => api.projects.create(input),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: qk.projects });
    },
  });
}

export function useCreateSession(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateSessionInput = {}) => api.sessions.create(projectId, input),
    onSuccess: (data: { session: Session }) => {
      void client.invalidateQueries({ queryKey: qk.sessions(projectId) });
      client.setQueryData(qk.session(data.session.id), data);
    },
  });
}

export function useSendMessage(sessionId: string) {
  return useMutation({
    mutationFn: (input: SendMessageInput) => api.sessions.send(sessionId, input),
  });
}

export function useAdminUsers(enabled: boolean) {
  return useQuery({
    queryKey: qk.users,
    queryFn: () => api.admin.users(),
    enabled,
    retry: false,
  });
}
/**
 * The caller's feature flags. Used to hide what the server would refuse anyway —
 * the server is the enforcement point, this is the courtesy.
 */
export function useFeatures() {
  return useQuery({
    queryKey: ["features"],
    queryFn: () => api.features(),
    // An admin can turn a feature off while this tab is open. A flag that only
    // applies after a reload is a flag the server enforces and the UI ignores.
    refetchInterval: 15_000,
    refetchOnWindowFocus: true,
    staleTime: 5_000,
  });
}

/** One flag, defaulting to "on" until the server says otherwise. */
export function useFeature(flag: string): boolean {
  const features = useFeatures();
  return features.data?.features[flag] ?? true;
}