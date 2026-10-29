"use client";

import { useRouter } from "next/navigation";
import { useTheme } from "next-themes";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import {
  Boxes,
  FolderGit2,
  KeyRound,
  LogOut,
  MessagesSquare,
  Moon,
  Plus,
  Shield,
  SlidersHorizontal,
  Sparkles,
  Sun,
} from "lucide-react";

import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { api } from "@/lib/api/client";
import { sessionStatusLabel } from "@/lib/session/format";
import { useCurrentUser, useProjects, useSessionSearch } from "@/lib/session/queries";

/**
 * ⌘K is the keyboard user's complete path through the app (PRD §10.1-5).
 * Everything the sidebar can do should be reachable here.
 *
 * It also searches sessions. The palette used to contain commands and projects
 * only, so typing a session title answered "没有匹配的命令" even though the
 * session existed: a project is a container, but the thing people look for by
 * name is the conversation.
 */
export function CommandPalette({
  open,
  onOpenChange,
  onNewSession,
  onNewProject,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onNewSession: () => void;
  onNewProject: () => void;
}) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const { resolvedTheme, setTheme } = useTheme();
  const me = useCurrentUser();
  const projects = useProjects();
  const [query, setQuery] = useState("");
  const needle = query.trim();

  // Empty query: the five most recent sessions, so the palette can resume work
  // without a search. Non-empty: server-side title/label search across projects.
  const sessions = useSessionSearch(needle, { enabled: open, limit: needle ? 8 : 5 });
  const projectNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const project of projects.data?.projects ?? []) names.set(project.id, project.name);
    return names;
  }, [projects.data]);

  const close = (next: boolean) => {
    if (!next) setQuery("");
    onOpenChange(next);
  };

  // Running an item is also a close: leaving the old search in the input made
  // the next Ctrl+K open a palette that still looked like the last one.
  const run = (action: () => void) => {
    close(false);
    action();
  };

  const logout = async () => {
    try {
      await api.auth.logout();
    } catch {
      // The local session is dropped either way.
    }
    queryClient.clear();
    router.replace("/login");
  };

  return (
    <CommandDialog
      open={open}
      onOpenChange={close}
      title="命令面板"
      description="快速执行操作"
    >
      {/* CommandDialog in this shadcn build only provides the dialog shell;
          cmdk's context comes from <Command>, and without it every child that
          reads the store (CommandInput, CommandList) throws on mount. */}
      <Command>
        <CommandInput
          value={query}
          onValueChange={setQuery}
          placeholder="搜索命令、项目、会话…"
        />
      <CommandList>
        <CommandEmpty>没有匹配的命令</CommandEmpty>

        <CommandGroup heading="操作">
          <CommandItem onSelect={() => run(onNewSession)}>
            <Plus />
            新会话
          </CommandItem>
          <CommandItem onSelect={() => run(onNewProject)}>
            <FolderGit2 />
            新建项目
          </CommandItem>
        </CommandGroup>

        {(sessions.data?.sessions ?? []).length > 0 ? (
          <CommandGroup heading={needle ? "会话" : "最近会话"}>
            {(sessions.data?.sessions ?? []).map((session) => {
              const projectName = projectNames.get(session.project_id) ?? "项目";
              return (
                <CommandItem
                  key={session.id}
                  data-testid="palette-session"
                  data-session-id={session.id}
                  data-project-id={session.project_id}
                  value={`会话 ${session.title || "新会话"} ${projectName}`}
                  onSelect={() =>
                    run(() => router.push(`/p/${session.project_id}/s/${session.id}`))
                  }
                >
                  <MessagesSquare />
                  <span className="truncate">{session.title || "新会话"}</span>
                  <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">
                    {projectName} · {sessionStatusLabel(session.status)}
                  </span>
                </CommandItem>
              );
            })}
          </CommandGroup>
        ) : null}

        {needle && sessions.isFetched && (sessions.data?.sessions ?? []).length === 0 ? (
          <p
            data-testid="palette-session-empty"
            className="px-2 py-1.5 text-xs text-muted-foreground"
          >
            没有匹配的会话
          </p>
        ) : null}

        {(projects.data?.projects ?? []).length > 0 ? (
          <CommandGroup heading="切换项目">
            {(projects.data?.projects ?? []).map((project) => (
              <CommandItem
                key={project.id}
                value={`项目 ${project.name}`}
                onSelect={() => run(() => router.push(`/p/${project.id}`))}
              >
                <FolderGit2 />
                {project.name}
              </CommandItem>
            ))}
          </CommandGroup>
        ) : null}

        <CommandSeparator />

        <CommandGroup heading="设置">
          <CommandItem onSelect={() => run(() => router.push("/settings/providers"))}>
            <KeyRound />
            模型与凭据
          </CommandItem>
          <CommandItem onSelect={() => run(() => router.push("/settings/api-keys"))}>
            <KeyRound />
            API Key
          </CommandItem>
          <CommandItem onSelect={() => run(() => router.push("/settings/skills"))}>
            <Sparkles />
            Skill 中心
          </CommandItem>
          <CommandItem onSelect={() => run(() => router.push("/settings/mcp"))}>
            <Boxes />
            MCP 中心
          </CommandItem>
          <CommandItem onSelect={() => run(() => router.push("/settings/general"))}>
            <SlidersHorizontal />
            通用设置
          </CommandItem>
          {me.data?.user.role === "admin" ? (
            <CommandItem onSelect={() => run(() => router.push("/admin/users"))}>
              <Shield />
              用户管理
            </CommandItem>
          ) : null}
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading="通用">
          <CommandItem
            onSelect={() =>
              run(() => setTheme(resolvedTheme === "dark" ? "light" : "dark"))
            }
          >
            {resolvedTheme === "dark" ? <Sun /> : <Moon />}
            {resolvedTheme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
          </CommandItem>
          <CommandItem onSelect={() => run(() => void logout())}>
            <LogOut />
            退出登录
          </CommandItem>
        </CommandGroup>
      </CommandList>
      </Command>
    </CommandDialog>
  );
}
