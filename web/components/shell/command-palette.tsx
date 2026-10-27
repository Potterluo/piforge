"use client";

import { useRouter } from "next/navigation";
import { useTheme } from "next-themes";
import { useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  FolderGit2,
  KeyRound,
  LogOut,
  Moon,
  Plus,
  Shield,
  Sparkles,
  Sun,
} from "lucide-react";

import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { api } from "@/lib/api/client";
import { useCurrentUser, useProjects } from "@/lib/session/queries";

/**
 * ⌘K is the keyboard user's complete path through the app (PRD §10.1-5).
 * Everything the sidebar can do should be reachable here.
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

  const run = (action: () => void) => {
    onOpenChange(false);
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
      onOpenChange={onOpenChange}
      title="命令面板"
      description="快速执行操作"
    >
      <CommandInput placeholder="搜索命令、项目…" />
      <CommandList>
        <CommandEmpty>没有匹配的命令</CommandEmpty>

        <CommandGroup heading="会话">
          <CommandItem onSelect={() => run(onNewSession)}>
            <Plus />
            新会话
          </CommandItem>
          <CommandItem onSelect={() => run(onNewProject)}>
            <FolderGit2 />
            新建项目
          </CommandItem>
        </CommandGroup>

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
          <CommandItem onSelect={() => run(() => router.push("/settings/skills"))}>
            <Sparkles />
            Skill 中心
          </CommandItem>
          <CommandItem onSelect={() => run(() => router.push("/settings/mcp"))}>
            <Boxes />
            MCP 中心
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
    </CommandDialog>
  );
}
