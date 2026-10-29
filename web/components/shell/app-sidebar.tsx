"use client";

import * as React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Activity,
  Archive,
  Check,
  Copy,
  Eye,
  ChevronRight,
  ChevronsUpDown,
  Download,
  FolderGit2,
  GitBranch,
  GitCompare,
  GitMerge,
  LogOut,
  MessageSquareDashed,
  MessagesSquare,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  PenLine,
  Plus,
  Search,
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
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarRail,
  SidebarSeparator,
  useSidebar,
} from "@/components/ui/sidebar";
import { Skeleton } from "@/components/ui/skeleton";
import { Kbd } from "@/components/ui/kbd";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import {
  formatRelative,
  sandboxLabel,
  sessionKindLabel,
  sessionStatusLabel,
  sessionStatusTone,
} from "@/lib/session/format";
import { qk, useCurrentUser, useFeature, useProjectSessions, useProjects } from "@/lib/session/queries";
import { SupportNotices } from "@/components/shell/support-notices";import { cn } from "@/lib/utils";
import type { Session, SessionStatus } from "@/lib/types";

interface SessionGroup {
  root: Session;
  children: Session[];
  lastActive: string;
}

/**
 * Groups main sessions with the branches and sidecars forked from them.
 *
 * The root is the session whose own id is the group key, not whichever row showed
 * up first: the API returns sessions sorted by activity, so a freshly created
 * sidecar often arrives before its parent. Taking "first" made the sidecar the
 * parent of the session it was attached to — and, worse, mounted the row actions
 * on the wrong session entirely.
 */
export function groupSessions(sessions: Session[]): SessionGroup[] {
  const groups = new Map<string, { root?: Session; children: Session[]; lastActive: string }>();

  for (const session of sessions) {
    const rootId = session.root_id ?? session.id;
    const group = groups.get(rootId) ?? { children: [], lastActive: session.last_active_at };
    if (session.id === rootId) {
      group.root = session;
    } else {
      group.children.push(session);
    }
    if (Date.parse(session.last_active_at) > Date.parse(group.lastActive)) {
      group.lastActive = session.last_active_at;
    }
    groups.set(rootId, group);
  }

  return [...groups.values()]
    .filter((group): group is { root: Session; children: Session[]; lastActive: string } =>
      Boolean(group.root),
    )
    .map((group) => ({
      root: group.root,
      children: group.children.sort(
        (a, b) => Date.parse(a.created_at) - Date.parse(b.created_at),
      ),
      lastActive: group.lastActive,
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
  onOpenPalette,
}: {
  onNewProject: () => void;
  onNewSession: () => void;
  onOpenPalette: () => void;
}) {
  const [accountOpen, setAccountOpen] = React.useState(false);
  const pathname = usePathname();
  const router = useRouter();
  const queryClient = useQueryClient();
  const me = useCurrentUser();
  const projects = useProjects();
  // "collapsed" here means the icon rail: the same sidebar, 48px wide, with the
  // project/session/settings/account affordances still reachable as icons.
  const { state: sidebarState, toggleSidebar } = useSidebar();
  const collapsed = sidebarState === "collapsed";

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

  /**
   * Forty projects is a normal account, and a flat list of forty rows pushes the
   * session list — the thing the sidebar is actually for — below the fold. The
   * default view is therefore "最近": the few projects a person works in, newest
   * first, plus the one on screen so the list never hides where you are. Typing
   * in the filter searches *all* of them; "查看全部" shows the whole list. No
   * project is ever removed, only folded away.
   */
  const [projectQuery, setProjectQuery] = useState("");
  const [showAllProjects, setShowAllProjects] = useState(false);
  const normalizedQuery = projectQuery.trim().toLowerCase();
  const projectList = useMemo(() => {
    const byRecency = [...(projects.data?.projects ?? [])].sort(
      (a, b) =>
        (Date.parse(b.last_active_at ?? "") || 0) - (Date.parse(a.last_active_at ?? "") || 0),
    );
    return byRecency;
  }, [projects.data]);
  const recentProjects = useMemo(() => {
    const recent = projectList.slice(0, 6);
    if (projectId && !recent.some((item) => item.id === projectId)) {
      const current = projectList.find((item) => item.id === projectId);
      if (current) recent.unshift(current);
    }
    return recent;
  }, [projectList, projectId]);
  const matchingProjects = useMemo(() => {
    if (!normalizedQuery) return [];
    return projectList.filter((item) => item.name.toLowerCase().includes(normalizedQuery));
  }, [projectList, normalizedQuery]);
  const visibleProjects = normalizedQuery
    ? matchingProjects
    : showAllProjects
      ? projectList
      : recentProjects;

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
    <Sidebar id="app-sidebar" collapsible="icon" data-testid="app-sidebar">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            {/*
              The only affordance before this was a 28px ghost icon in the corner,
              which read as decoration: the user's complaint was "what sidebar
              collapse?". The control is now a labelled row with the shortcut
              spelled out, and in the icon rail it keeps the same icon plus a
              tooltip, so there is always a visible way back.
            */}
            <SidebarMenuButton
              variant="outline"
              tooltip={collapsed ? "展开侧边栏（Ctrl+B）" : "收起侧边栏（Ctrl+B）"}
              aria-label={collapsed ? "展开侧边栏（Ctrl+B）" : "收起侧边栏（Ctrl+B）"}
              aria-expanded={!collapsed}
              aria-controls="app-sidebar"
              data-testid="sidebar-collapse-toggle"
              onClick={() => toggleSidebar()}
            >
              {collapsed ? <PanelLeftOpen /> : <PanelLeftClose />}
              <span>收起侧边栏</span>
              <Kbd className="ml-auto group-data-[collapsible=icon]:hidden">Ctrl B</Kbd>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            {/*
              A visible door to the palette. Ctrl+K was the only way in, which
              made every command it contains invisible to anyone who does not
              know the shortcut.
            */}
            <SidebarMenuButton
              tooltip="命令面板（Ctrl+K）"
              aria-label="打开命令面板"
              data-testid="command-palette-trigger"
              onClick={onOpenPalette}
            >
              <Search />
              <span>搜索命令</span>
              <Kbd className="ml-auto group-data-[collapsible=icon]:hidden">Ctrl K</Kbd>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            {/*
              The header used to be a dropdown that opened nothing and swallowed the click.
              A list of projects inside a menu with one item per project is also a second
              copy of the list right below it, so this is a plain link to the project list:
              one target, always the same behaviour.
            */}
            <SidebarMenuButton
              size="lg"
              tooltip="所有项目"
              data-testid="project-switcher"
              render={<Link href="/" />}
            >
              <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                <FolderGit2 className="size-4" />
              </div>
              <div className="grid min-w-0 flex-1 text-left leading-tight">
                <span className="truncate text-sm font-medium">{project?.name ?? "PiForge"}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {project ? `沙箱 · ${sandboxLabel(project.sandbox_state)}` : "所有项目"}
                </span>
              </div>
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent data-testid={collapsed ? "sidebar-rail" : undefined}>
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
              <>
                {/* The rail cannot type, so the filter and the scope switcher only
                    exist in the expanded sidebar; the icon list falls back to 最近. */}
                {projectList.length > 6 ? (
                  <div className="px-2 pb-2 group-data-[collapsible=icon]:hidden">
                    <Input
                      data-testid="project-filter"
                      aria-label="筛选项目"
                      placeholder="筛选项目…"
                      value={projectQuery}
                      onChange={(event) => setProjectQuery(event.target.value)}
                      className="h-7 text-xs"
                    />
                  </div>
                ) : null}

                <div
                  data-testid="project-list-scope"
                  className="flex items-center justify-between gap-2 px-2 pb-1 text-[10px] text-muted-foreground group-data-[collapsible=icon]:hidden"
                >
                  <span>
                    {normalizedQuery
                      ? `匹配 ${matchingProjects.length} 个`
                      : showAllProjects
                        ? `全部 ${projectList.length} 个`
                        : "最近"}
                  </span>
                  {projects.data ? <span>{`共 ${projectList.length} 个`}</span> : null}
                </div>

                <SidebarMenu>
                  {/* Icon mode keeps the two "new" doors: a 48px rail with no way to
                      start a session would be a dead end. */}
                  <SidebarMenuItem className="hidden group-data-[collapsible=icon]:block">
                    <SidebarMenuButton
                      tooltip="新建项目"
                      aria-label="新建项目"
                      onClick={onNewProject}
                    >
                      <Plus />
                      <span>新建项目</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                  {visibleProjects.map((item) => (
                    <SidebarMenuItem key={item.id} data-testid="project-row">
                      <SidebarMenuButton
                        isActive={item.id === projectId}
                        tooltip={item.name}
                        render={<Link href={`/p/${item.id}`} />}
                      >
                        <FolderGit2 />
                        <span className="truncate">{item.name}</span>
                        {item.session_count > 0 ? (
                          <SidebarMenuBadge>{item.session_count}</SidebarMenuBadge>
                        ) : null}
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  ))}
                </SidebarMenu>

                {normalizedQuery ? (
                  matchingProjects.length === 0 ? (
                    <p
                      data-testid="project-empty"
                      className="px-2 py-1 text-xs text-muted-foreground"
                    >
                      没有匹配的项目
                    </p>
                  ) : (
                    <button
                      type="button"
                      data-testid="project-clear-filter"
                      className="mt-1 w-full rounded-md px-2 py-1 text-left text-xs text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-foreground group-data-[collapsible=icon]:hidden"
                      onClick={() => setProjectQuery("")}
                    >
                      清除筛选
                    </button>
                  )
                ) : projectList.length > recentProjects.length ? (
                  <button
                    type="button"
                    data-testid="project-show-all"
                    aria-expanded={showAllProjects}
                    className="mt-1 w-full rounded-md px-2 py-1 text-left text-xs text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-foreground group-data-[collapsible=icon]:hidden"
                    onClick={() => setShowAllProjects((value) => !value)}
                  >
                    {showAllProjects
                      ? "只看最近"
                      : `查看全部 ${projectList.length} 个项目`}
                  </button>
                ) : null}
              </>
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
                aria-pressed={showArchived}
                className="ml-auto rounded px-1 text-[10px] font-normal text-muted-foreground hover:text-foreground data-[on=true]:text-foreground"
                data-on={showArchived}
                onClick={() => setShowArchived((value) => !value)}
              >
                {showArchived ? `隐藏已归档 (${archivedCount})` : `显示已归档 (${archivedCount})`}
              </button>
            ) : null}
          </SidebarGroupLabel>
          <SidebarGroupAction onClick={onNewSession} title="新会话">
            <Plus />
            <span className="sr-only">新会话</span>
          </SidebarGroupAction>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem className="hidden group-data-[collapsible=icon]:block">
                <SidebarMenuButton tooltip="新会话" aria-label="新会话" onClick={onNewSession}>
                  <Plus />
                  <span>新会话</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
            {!projectId ? (
              <p className="px-2 py-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
                先选择一个项目
              </p>
            ) : sessions.isPending ? (
              <SidebarMenu>
                {[0, 1, 2, 3].map((index) => (
                  <SidebarMenuItem key={index}>
                    <Skeleton className="h-7 w-full" />
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            ) : sessions.isError ? (
              <div className="flex flex-col items-start gap-2 px-2 py-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
                <span>会话加载失败</span>
                <Button size="xs" variant="outline" onClick={() => void sessions.refetch()}>
                  重试
                </Button>
              </div>
            ) : groups.length === 0 ? (
              <div className="flex flex-col items-start gap-2 px-2 py-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
                <span>{archivedCount > 0 && !showArchived ? "没有进行中的会话" : "还没有会话"}</span>
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
                        <span
                          className={cn(
                            // `truncate` belongs on the label itself: the menu
                            // button's `[&>span:last-child]` rule hits the status
                            // dot, not the title, so long titles wrapped to two
                            // lines and turned the list into a wall of text.
                            "truncate",
                            group.root.status === "archived" && "text-muted-foreground",
                          )}
                        >
                          {group.root.title || "未命名会话"}
                        </span>
                        {group.root.status === "archived" ? (
                          <Badge variant="outline" className="text-[9px] leading-none">
                            已归档
                          </Badge>
                        ) : null}
                        <StatusDot status={group.root.status} />
                      </SidebarMenuButton>
                      <SessionActions session={group.root} projectId={projectId!} />
                      {group.children.length > 0 ? (
                        <CollapsibleTrigger
                          render={
                            <SidebarMenuAction
                              showOnHover
                              // Two hover actions share the right edge: the chevron
                              // sits left of the kebab, or one swallows the other's
                              // clicks (which is how the session menu died).
                              className="group right-7"
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
                              <span className="truncate">{child.label || child.title || "未命名"}</span>
                              <StatusDot status={child.status} />
                            </SidebarMenuSubButton>
                            {/* A branch is a session: it can be renamed, archived,
                                exported and deleted like its parent. */}
                            <SessionActions session={child} projectId={projectId!} />
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
          <SidebarMenuItem>
            <SupportNotices />
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
              <SidebarMenuItem>
                <SidebarMenuButton tooltip="只读排障" render={<Link href="/admin/support" />}>
                  <Eye />
                  <span>只读排障</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </>
          ) : null}
          <SidebarMenuItem className="relative">
            {/*
              The account control is a plain button with a panel beside it, not a popup
              menu: the menu version opened nothing and left the trigger detached from the
              DOM, which is what "clicking my avatar does nothing" looked like from outside.
            */}
            <SidebarMenuButton
              size="lg"
              tooltip={user?.display_name || user?.email || "账户"}
              aria-expanded={accountOpen}
              data-testid="account-menu"
              onClick={() => setAccountOpen((open) => !open)}
            >
              <Avatar className="size-8 rounded-lg">
                <AvatarFallback>
                  {(user?.display_name || user?.email || "?").slice(0, 1).toUpperCase()}
                </AvatarFallback>
              </Avatar>
              <div className="grid min-w-0 flex-1 text-left leading-tight">
                <span className="truncate text-sm font-medium">
                  {user?.display_name || "未登录"}
                </span>
                <span data-testid="user-email" className="truncate text-xs text-muted-foreground">
                  {user?.email ?? ""}
                </span>
              </div>
              {isAdmin ? (
                <Badge variant="outline" className="group-data-[collapsible=icon]:hidden">
                  管理员
                </Badge>
              ) : null}
            </SidebarMenuButton>
            {accountOpen ? (
              <div
                data-testid="account-panel"
                className="absolute bottom-full start-0 z-50 mb-1 w-56 rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
              >
                <p className="truncate px-2 py-1.5 text-xs text-muted-foreground">{user?.email}</p>
                <Link
                  href="/settings/providers"
                  className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-accent"
                  onClick={() => setAccountOpen(false)}
                >
                  <Settings />
                  设置
                </Link>
                <button
                  type="button"
                  data-testid="logout"
                  className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm text-destructive hover:bg-accent"
                  onClick={() => void logout()}
                >
                  <LogOut />
                  退出登录
                </button>
              </div>
            ) : null}
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
  // The server refuses export when the flag is off; hiding the item keeps the
  // refusal from looking like a broken menu.
  const exportEnabled = useFeature("export");
  const router = useRouter();
  const [renaming, setRenaming] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  // The compare chooser: a session can only be compared with *other* sessions in
  // its project. Picking `session.id` as the fallback (the old behaviour when a
  // session had no parent) put the same id in the URL twice and the compare page
  // dutifully rendered the session against itself.
  const [compareOpen, setCompareOpen] = useState(false);
  const [compareWith, setCompareWith] = useState<string[]>([]);
  const projectSessions = useProjectSessions(projectId);
  const compareCandidates = (projectSessions.data?.sessions ?? []).filter(
    (candidate) => candidate.id !== session.id,
  );

  const toggleCompareCandidate = (id: string) => {
    setCompareWith((current) => {
      if (current.includes(id)) return current.filter((value) => value !== id);
      if (current.length >= 2) return current;
      return [...current, id];
    });
  };

  const startCompare = () => {
    if (compareWith.length === 0) return;
    const ids = [session.id, ...compareWith];
    setCompareOpen(false);
    setCompareWith([]);
    setMenuOpen(false);
    router.push(`/p/${projectId}/compare?ids=${ids.join(",")}`);
  };

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
        description: humanizeError(error),
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
        description: humanizeError(error),
      }),
  });

  const remove = useMutation({
    mutationFn: () => api.sessions.remove(session.id),
    onSuccess: () => {
      toast.success("已删除会话");
      setDeleting(false);
      refresh();
      // Only leave the workbench if the session that just vanished is the one on
      // screen; deleting a sibling from the list must not close what you read.
      const path = window.location.pathname;
      if (path.includes(`/s/${session.id}`)) router.replace(`/p/${projectId}`);
    },
    onError: (error) =>
      toast.error("删除失败", {
        description: humanizeError(error),
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
              data-session-actions={session.id}
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
          {exportEnabled ? (
            <DropdownMenuItem
              data-testid="session-export"
              render={<a href={api.sessions.exportUrl(session.id, "md")} download />}
            >
              <Download />
              导出 Markdown
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem
            data-testid="session-compare"
            onClick={() => {
              setMenuOpen(false);
              setCompareWith([]);
              setCompareOpen(true);
            }}
          >
            <GitCompare />
            对比
          </DropdownMenuItem>
          {session.kind === "branch" ? (
            <DropdownMenuItem
              data-testid="session-adopt"
              onClick={() => {
                setMenuOpen(false);
                void api.sessions
                  .adopt(session.id)
                  .then((data) =>
                    toast.success("已采纳回主干", {
                      description: `${data.adopted.commit} · ${data.adopted.files.length} 个文件`,
                    }),
                  )
                  .catch((error: unknown) =>
                    toast.error("采纳失败", {
                      description: humanizeError(error),
                    }),
                  );
              }}
            >
              <GitMerge />
              采纳回主干
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem
            data-testid="session-clone"
            onClick={() => {
              setMenuOpen(false);
              void api.sessions
                .clone(session.id)
                .then((data) => router.push(`/p/${projectId}/s/${data.session.id}`))
                .catch((error: unknown) =>
                  toast.error("克隆失败", {
                    description: humanizeError(error),
                  }),
                );
            }}
          >
            <Copy />
            克隆（再试一次）
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

      <Dialog
        open={compareOpen}
        onOpenChange={(open) => {
          setCompareOpen(open);
          if (!open) setCompareWith([]);
        }}
      >
        <DialogContent className="sm:max-w-md" data-testid="compare-chooser">
          <DialogHeader>
            <DialogTitle>对比其他会话</DialogTitle>
            <DialogDescription>
              选一个或两个「{session.title || "未命名会话"}」之外的会话，并排看每个尝试的改动和开销。
            </DialogDescription>
          </DialogHeader>

          {compareCandidates.length === 0 ? (
            <p
              className="rounded-md border border-dashed border-border p-3 text-xs text-muted-foreground"
              data-testid="compare-no-others"
            >
              这个项目里还没有其他会话。先克隆当前会话，或者新建一个会话，再回来对比。
            </p>
          ) : (
            <div className="flex max-h-72 flex-col gap-1 overflow-y-auto">
              {compareCandidates.map((candidate) => {
                const selected = compareWith.includes(candidate.id);
                const full = compareWith.length >= 2 && !selected;
                return (
                  <button
                    key={candidate.id}
                    type="button"
                    data-testid="compare-candidate"
                    data-session-id={candidate.id}
                    aria-pressed={selected}
                    aria-disabled={full || undefined}
                    disabled={full}
                    onClick={() => toggleCompareCandidate(candidate.id)}
                    className={`flex items-center gap-2 rounded-md border px-2 py-1.5 text-left transition-colors ${
                      selected ? "border-primary/50 bg-muted" : "border-transparent hover:bg-muted"
                    } disabled:cursor-not-allowed disabled:opacity-50`}
                  >
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate text-sm">{candidate.title || "未命名会话"}</span>
                      <span className="truncate text-xs text-muted-foreground">
                        {sessionKindLabel(candidate.kind)} · {sessionStatusLabel(candidate.status)} ·{" "}
                        {formatRelative(candidate.last_active_at)}
                      </span>
                    </span>
                    {selected ? <Check className="size-4 shrink-0 text-primary" /> : null}
                  </button>
                );
              })}
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setCompareOpen(false)}>
              取消
            </Button>
            <Button
              type="button"
              data-testid="compare-start"
              disabled={compareWith.length === 0}
              onClick={startCompare}
            >
              对比
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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
              <Button
                type="submit"
                data-testid="session-rename-submit"
                disabled={rename.isPending || (renaming ?? "").trim().length === 0}
              >
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