"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Activity,
  Archive,
  ChevronRight,
  ChevronsUpDown,
  Download,
  FolderGit2,
  GitBranch,
  LogOut,
  MessageSquareDashed,
  MessagesSquare,
  MoreHorizontal,
  PenLine,
  Plus,
  Settings,
  Shield,
  Trash2,
} from "lucide-react";

import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarTrigger,
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarRail,
  SidebarSeparator,
} from "@/components/ui/sidebar";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api/client";
import {
  formatRelative,
  sandboxLabel,
  sessionKindLabel,
  sessionStatusLabel,
  sessionStatusTone,
} from "@/lib/session/format";
import { qk, useCurrentUser, useProjectSessions, useProjects } from "@/lib/session/queries";import { cn } from "@/lib/utils";
import type { Session, SessionStatus } from "@/lib/types";

interface SessionGroup {
  root: Session;
  children: Session[];
  lastActive: string;
}

/** Groups main sessions with the branches and sidecars forked from them. */
export function groupSessions(sessions: Session[]): SessionGroup[] {
  const groups = new Map<string, SessionGroup>();

  for (const session of sessions) {
    const rootId = session.root_id ?? session.id;
    const existing = groups.get(rootId);
    if (existing) {
      if (session.id !== existing.root.id) existing.children.push(session);
      if (Date.parse(session.last_active_at) > Date.parse(existing.lastActive)) {
        existing.lastActive = session.last_active_at;
      }
      continue;
    }
    groups.set(rootId, { root: session, children: [], lastActive: session.last_active_at });
  }

  return [...groups.values()]
    .map((group) => ({
      ...group,
      children: group.children.sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)),
    }))
    .sort((a, b) => Date.parse(b.lastActive) - Date.parse(a.lastActive));
}

function activeIds(pathname: string): { projectId: string | null; sessionId: string | null } {
  const session = /^\/p\/([^/]+)\/s\/([^/]+)/.exec(pathname);
  if (session) return { projectId: session[1], sessionId: session[2] };
  const project = /^\/p\/([^/]+)/.exec(pathname);
  return { projectId: project?.[1] ?? null, sessionId: null };
}

function StatusDot({ status }: { status: SessionStatus }) {
  const tone = sessionStatusTone(status);
  return (
    <span
      aria-hidden="true"
      title={sessionStatusLabel(status)}
      className={cn(
        "size-1.5 shrink-0 rounded-full",
        tone === "running" && "bg-primary animate-pulse",
        tone === "idle" && "bg-muted-foreground/50",
        tone === "paused" && "border border-muted-foreground bg-transparent",
        tone === "failed" && "bg-destructive",
      )}
    />
  );
}

function KindGlyph({ kind }: { kind: Session["kind"] }) {
  if (kind === "branch") {
    return <GitBranch aria-label={sessionKindLabel(kind)} className="size-3 shrink-0 text-muted-foreground" />;
  }
  if (kind === "sidecar") {
    return (
      <MessageSquareDashed
        aria-label={sessionKindLabel(kind)}
        className="size-3 shrink-0 text-muted-foreground"
      />
    );
  }
  return null;
}

export function AppSidebar({
  onNewProject,
  onNewSession,
}: {
  onNewProject: () => void;
  onNewSession: () => void;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const queryClient = useQueryClient();
  const me = useCurrentUser();
  const projects = useProjects();

  const { projectId, sessionId } = useMemo(() => activeIds(pathname), [pathname]);
  const sessions = useProjectSessions(projectId ?? undefined);

  const project = projects.data?.projects.find((item) => item.id === projectId) ?? null;
  // Archived sessions stay out of the way until asked for: archiving one is how a
  // user says "I am done with this" without losing it.
  const [showArchived, setShowArchived] = useState(false);
  const archivedCount = (sessions.data?.sessions ?? []).filter(
    (item) => item.status === "archived",
  ).length;
  const groups = useMemo(() => {
    const visible = (sessions.data?.sessions ?? []).filter(
      (item) => showArchived || item.status !== "archived",
    );
    return groupSessions(visible);
  }, [sessions.data, showArchived]);

  const user = me.data?.user;
  const isAdmin = user?.role === "admin";

  const logout = async () => {
    try {
      await api.auth.logout();
    } catch {
      // Logging out locally is still the right outcome if the call fails.
    }
    queryClient.clear();
    router.replace("/login");
  };

  return (
    <Sidebar collapsible="offcanvas" data-testid="app-sidebar">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem className="flex items-center gap-1">
            <SidebarTrigger data-testid="sidebar-collapse" className="ml-auto" aria-label="收起侧边栏" />
          </SidebarMenuItem>
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<SidebarMenuButton size="lg" className="data-[popup-open]:bg-sidebar-accent" />}
              >
                <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                  <FolderGit2 className="size-4" />
                </div>
                <div className="grid min-w-0 flex-1 text-left leading-tight">
                  <span className="truncate text-sm font-medium">
                    {project?.name ?? "PiForge"}
                  </span>
                  <span className="truncate text-xs text-muted-foreground">
                    {project ? `沙箱 · ${sandboxLabel(project.sandbox_state)}` : "选择一个项目"}
                  </span>
                </div>
                <ChevronsUpDown className="ml-auto size-4" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-56">
                <DropdownMenuLabel>项目</DropdownMenuLabel>
                {(projects.data?.projects ?? []).map((item) => (
                  <DropdownMenuItem key={item.id} onClick={() => router.push(`/p/${item.id}`)}>
                    <FolderGit2 />
                    <span className="truncate">{item.name}</span>
                  </DropdownMenuItem>
                ))}
                {projects.data && projects.data.projects.length === 0 ? (
                  <DropdownMenuItem disabled>还没有项目</DropdownMenuItem>
                ) : null}
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={onNewProject}>
                  <Plus />
                  新建项目…
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>项目</SidebarGroupLabel>
          <SidebarGroupAction onClick={onNewProject} title="新建项目">
            <Plus />
            <span className="sr-only">新建项目</span>
          </SidebarGroupAction>
          <SidebarGroupContent>
            {projects.isPending ? (
              <SidebarMenu>
                {[0, 1, 2].map((index) => (
                  <SidebarMenuItem key={index}>
                    <Skeleton className="h-8 w-full" />
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            ) : projects.isError ? (
              <div className="flex flex-col items-start gap-2 px-2 py-1 text-xs text-muted-foreground">
                <span>项目加载失败</span>
                <Button size="xs" variant="outline" onClick={() => void projects.refetch()}>
                  重试
                </Button>
              </div>
            ) : (
              <SidebarMenu>
                {(projects.data?.projects ?? []).map((item) => (
                  <SidebarMenuItem key={item.id}>
                    <SidebarMenuButton
                      isActive={item.id === projectId}
                      tooltip={item.name}
                      render={<Link href={`/p/${item.id}`} />}
                    >
                      <FolderGit2 />
                      <span>{item.name}</span>
                      {item.session_count > 0 ? (
                        <SidebarMenuBadge>{item.session_count}</SidebarMenuBadge>
                      ) : null}
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            )}
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarSeparator />

        <SidebarGroup>
          <SidebarGroupLabel className="gap-2">
            会话
            {archivedCount > 0 ? (
              <button
                type="button"
                data-testid="toggle-archived"
                className="ml-auto rounded px-1 text-[10px] font-normal text-muted-foreground hover:text-foreground"
                onClick={() => setShowArchived((value) => !value)}
              >
                {showArchived ? "隐藏已归档" : `显示已归档 (${archivedCount})`}
              </button>
            ) : null}
          </SidebarGroupLabel>
          <SidebarGroupAction onClick={onNewSession} title="新会话">
            <Plus />
            <span className="sr-only">新会话</span>
          </SidebarGroupAction>
          <SidebarGroupContent>
            {!projectId ? (
              <p className="px-2 py-1 text-xs text-muted-foreground">先选择一个项目</p>
            ) : sessions.isPending ? (
              <SidebarMenu>
                {[0, 1, 2, 3].map((index) => (
                  <SidebarMenuItem key={index}>
                    <Skeleton className="h-7 w-full" />
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            ) : sessions.isError ? (
              <div className="flex flex-col items-start gap-2 px-2 py-1 text-xs text-muted-foreground">
                <span>会话加载失败</span>
                <Button size="xs" variant="outline" onClick={() => void sessions.refetch()}>
                  重试
                </Button>
              </div>
            ) : groups.length === 0 ? (
              <div className="flex flex-col items-start gap-2 px-2 py-1 text-xs text-muted-foreground">
                <span>还没有会话</span>
                <Button size="xs" variant="outline" onClick={onNewSession}>
                  <Plus />
                  新会话
                </Button>
              </div>
            ) : (
              groups.map((group) => (
                <Collapsible key={group.root.id} defaultOpen>
                  <SidebarMenu>
                    <SidebarMenuItem>
                      <SidebarMenuButton
                        isActive={group.root.id === sessionId}
                        tooltip={group.root.title || "未命名会话"}
                        data-testid="session-link"
                        render={<Link href={`/p/${projectId}/s/${group.root.id}`} />}
                      >
                        <MessagesSquare />
                        <span>{group.root.title || "未命名会话"}</span>
                        <StatusDot status={group.root.status} />
                      </SidebarMenuButton>
                      <SessionActions session={group.root} projectId={projectId!} />
                      {group.children.length > 0 ? (
                        <CollapsibleTrigger
                          render={
                            <SidebarMenuAction
                              showOnHover
                              className="group"
                              aria-label="展开分支"
                              title={`展开 ${group.children.length} 个分支`}
                            />
                          }
                        >
                          <ChevronRight className="transition-transform group-data-[panel-open]:rotate-90" />
                        </CollapsibleTrigger>
                      ) : null}
                    </SidebarMenuItem>
                  </SidebarMenu>
                  {group.children.length > 0 ? (
                    <CollapsibleContent>
                      <SidebarMenuSub>
                        {group.children.map((child) => (
                          <SidebarMenuSubItem key={child.id}>
                            <SidebarMenuSubButton
                              isActive={child.id === sessionId}
                              data-testid="session-link"
                              render={<Link href={`/p/${projectId}/s/${child.id}`} />}
                            >
                              <KindGlyph kind={child.kind} />
                              <span>{child.label || child.title || "未命名"}</span>
                              <StatusDot status={child.status} />
                            </SidebarMenuSubButton>
                          </SidebarMenuSubItem>
                        ))}
                      </SidebarMenuSub>
                    </CollapsibleContent>
                  ) : null}
                </Collapsible>
              ))
            )}
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton tooltip="设置" render={<Link href="/settings/providers" />}>
              <Settings />
              <span>设置</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
          {isAdmin ? (
            <>
              <SidebarMenuItem>
                <SidebarMenuButton tooltip="运行状态" render={<Link href="/admin/status" />}>
                  <Activity />
                  <span>运行状态</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
              <SidebarMenuItem>
                <SidebarMenuButton tooltip="用户管理" render={<Link href="/admin/users" />}>
                  <Shield />
                  <span>用户管理</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </>
          ) : null}
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger render={<SidebarMenuButton size="lg" />}>
                <Avatar className="size-8 rounded-lg">
                  <AvatarFallback>
                    {(user?.display_name || user?.email || "?").slice(0, 1).toUpperCase()}
                  </AvatarFallback>
                </Avatar>
                <div className="grid min-w-0 flex-1 text-left leading-tight">
                  <span className="truncate text-sm font-medium">
                    {user?.display_name || "未登录"}
                  </span>
                  <span
                    data-testid="user-email"
                    className="truncate text-xs text-muted-foreground"
                  >
                    {user?.email ?? ""}
                  </span>
                </div>
                {isAdmin ? (
                  <Badge variant="outline" className="group-data-[collapsible=icon]:hidden">
                    管理员
                  </Badge>
                ) : null}
              </DropdownMenuTrigger>
              <DropdownMenuContent side="top" align="start" className="w-56">
                <DropdownMenuLabel className="truncate">{user?.email}</DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={() => void logout()}>
                  <LogOut />
                  退出登录
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
        <div className="px-2 py-1 text-[11px] text-muted-foreground group-data-[collapsible=icon]:hidden">
          {project ? `最近活动 ${formatRelative(project.last_active_at)}` : "PiForge"}
        </div>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  );
}
/**
 * Per-session actions: rename, archive, export, delete.
 *
 * These are the four things a session list is expected to do and none of them
 * existed, so a finished session could only be scrolled past forever.
 */
function SessionActions({ session, projectId }: { session: Session; projectId: string }) {
  const queryClient = useQueryClient();
  const router = useRouter();
  const [renaming, setRenaming] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: qk.sessions(projectId) });
    void queryClient.invalidateQueries({ queryKey: qk.session(session.id) });
    void queryClient.invalidateQueries({ queryKey: qk.projects });
  };

  const rename = useMutation({
    mutationFn: (title: string) => api.sessions.update(session.id, { title }),
    onSuccess: () => {
      toast.success("已重命名");
      setRenaming(null);
      refresh();
    },
    onError: (error) =>
      toast.error("重命名失败", {
        description: error instanceof Error ? error.message : "未知错误",
      }),
  });

  const archived = session.status === "archived";
  const archive = useMutation({
    mutationFn: () => api.sessions.archive(session.id, !archived),
    onSuccess: () => {
      toast.success(archived ? "已移出归档" : "已归档", {
        description: archived ? undefined : "归档的会话默认不出现在列表里，可随时恢复。",
      });
      refresh();
    },
    onError: (error) =>
      toast.error("操作失败", {
        description: error instanceof Error ? error.message : "未知错误",
      }),
  });

  const remove = useMutation({
    mutationFn: () => api.sessions.remove(session.id),
    onSuccess: () => {
      toast.success("已删除会话");
      setDeleting(false);
      if (session.id) refresh();
      router.replace(`/p/${projectId}`);
    },
    onError: (error) =>
      toast.error("删除失败", {
        description: error instanceof Error ? error.message : "未知错误",
      }),
  });

  return (
    <>
      <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
        <DropdownMenuTrigger
          render={
            <SidebarMenuAction
              showOnHover
              aria-label="会话操作"
              data-testid="session-actions"
              className={menuOpen ? "opacity-100" : undefined}
            />
          }
        >
          <MoreHorizontal />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-40">
          <DropdownMenuItem
            data-testid="session-rename"
            onClick={() => {
              setRenaming(session.title ?? "");
              setMenuOpen(false);
            }}
          >
            <PenLine />
            重命名
          </DropdownMenuItem>
          <DropdownMenuItem
            data-testid="session-archive"
            onClick={() => {
              archive.mutate();
              setMenuOpen(false);
            }}
          >
            <Archive />
            {archived ? "移出归档" : "归档"}
          </DropdownMenuItem>
          <DropdownMenuItem
            data-testid="session-export"
            render={<a href={api.sessions.exportUrl(session.id, "md")} download />}
          >
            <Download />
            导出 Markdown
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            data-testid="session-delete"
            onClick={() => {
              setDeleting(true);
              setMenuOpen(false);
            }}
          >
            <Trash2 />
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={renaming !== null} onOpenChange={(open) => !open && setRenaming(null)}>
        <DialogContent className="sm:max-w-md">
          <form
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              rename.mutate((renaming ?? "").trim());
            }}
          >
            <DialogHeader>
              <DialogTitle>重命名会话</DialogTitle>
              <DialogDescription>名字只影响列表和标题，不改变对话内容。</DialogDescription>
            </DialogHeader>
            <Field>
              <FieldLabel htmlFor="session-title-input">名称</FieldLabel>
              <Input
                id="session-title-input"
                data-testid="session-title-input"
                value={renaming ?? ""}
                onChange={(event) => setRenaming(event.target.value)}
                autoFocus
              />
            </Field>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setRenaming(null)}>
                取消
              </Button>
              <Button type="submit" data-testid="session-rename-submit" disabled={rename.isPending}>
                保存
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <AlertDialog open={deleting} onOpenChange={setDeleting}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除「{session.title || "未命名会话"}」？</AlertDialogTitle>
            <AlertDialogDescription>
              对话记录会一并删除，无法恢复。想要保留但不想看到它，请用「归档」。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              data-testid="session-delete-confirm"
              onClick={() => remove.mutate()}
            >
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}