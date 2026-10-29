"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, type CreateProjectInput, type CreateSessionInput, type SendMessageInput, type UpdateProjectInput } from "@/lib/api/client";
import { ApiError, type Session } from "@/lib/types";

/** Query keys live in one place so invalidation cannot drift from fetching. */
export const qk = {
  me: ["me"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["project", id] as const,
  sessions: (projectId: string) => ["sessions", projectId] as const,
  /** Cross-project search, kept apart from the project list's cache entry. */
  sessionSearch: (query: string, limit: number) => ["sessions", "search", query, limit] as const,
  session: (id: string) => ["session", id] as const,
  sessionSkills: (id: string) => ["session-skills", id] as const,
  stats: (id: string) => ["stats", id] as const,
  sandbox: (id: string) => ["sandbox", id] as const,
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
    // The list carries each project's sandbox state, which changes when a sandbox
    // is provisioned. A stale badge ("沙箱 · 未创建" over a working terminal) is the
    // kind of detail that makes an operator distrust every other number.
    staleTime: 15_000,
    refetchInterval: 30_000,
    refetchOnWindowFocus: true,
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

/**
 * Sessions matching a query across every project.
 *
 * The server already filters on title and label (`GET /v1/sessions?q=`), so this
 * is the same endpoint the project sidebar uses, without `project`. An empty
 * query is allowed for callers that want "recent sessions" (the palette).
 */
export function useSessionSearch(
  query: string,
  options: { enabled?: boolean; limit?: number } = {},
) {
  const needle = query.trim();
  const limit = options.limit ?? 20;
  return useQuery({
    queryKey: qk.sessionSearch(needle, limit),
    queryFn: () => api.sessions.list({ q: needle || undefined, limit }),
    enabled: options.enabled ?? true,
    // A search is cheap and the result list is small; a short stale window keeps
    // a keystroke from refetching what is already on screen.
    staleTime: 5_000,
  });
}

export function useSession(sessionId: string) {
  return useQuery({
    queryKey: qk.session(sessionId),
    queryFn: () => api.sessions.get(sessionId),
    enabled: sessionId.length > 0,
    // A 404 means the session does not exist; retrying it three times only
    // delays the not-found page. Every other failure keeps its retries.
    retry: (attempt, error) => !(error instanceof ApiError && error.status === 404) && attempt < 2,
  });
}

/**
 * The skill versions a session was mounted with (FR-14.5).
 *
 * Deliberately not refetched on every keystroke: the record is write-once, so
 * once a session has answered there is nothing newer to learn. A session that
 * has not been provisioned yet answers `[]`, so the UI must distinguish "not
 * yet" from "none" by the worktree state, not by polling this.
 */
export function useSessionSkills(sessionId: string) {
  return useQuery({
    queryKey: qk.sessionSkills(sessionId),
    queryFn: () => api.sessions.skills(sessionId),
    enabled: sessionId.length > 0,
    staleTime: 60_000,
  });
}

/** The whole account's skill library, for the `/` palette. */
export function useSkills() {
  return useQuery({
    queryKey: qk.skills,
    queryFn: () => api.skills.list(),
    staleTime: 30_000,
  });
}

export function useProviders() {
  return useQuery({
    queryKey: qk.providers,
    queryFn: () => api.providers.list(),
    staleTime: 30_000,
  });
}

/**
 * The first-run answer (GET /v1/setup).
 *
 * Short stale time and a key of its own: after an admin creates the shared
 * provider the panel must disappear without a reload, and `providers` is
 * invalidated at the same time by the one-click flow.
 */
export function useSetup() {
  return useQuery({
    queryKey: ["setup"],
    queryFn: () => api.setup.get(),
    staleTime: 10_000,
    refetchOnWindowFocus: true,
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

/**
 * Renames (or re-points) a project. Both the project list and the project detail
 * are updated, because the sidebar, the header and the home page each read one
 * of them — a rename that only changes the page you are on is not a rename.
 */
export function useUpdateProject(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: UpdateProjectInput) => api.projects.update(projectId, input),
    onSuccess: (data) => {
      client.setQueryData(qk.project(projectId), data);
      void client.invalidateQueries({ queryKey: qk.projects });
    },
  });
}

/**
 * Deletes a project, its sessions and its sandbox record.
 *
 * The caller owns navigation: the project page returns home, while a maintenance
 * action elsewhere may simply stay put. The project and session caches are
 * removed rather than invalidated, so a deleted id is not briefly re-fetched
 * into a 404 flash on the way out.
 */
export function useDeleteProject() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (projectId: string) => api.projects.remove(projectId),
    onSuccess: (_data, projectId) => {
      client.removeQueries({ queryKey: qk.project(projectId) });
      client.removeQueries({ queryKey: qk.sessions(projectId) });
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