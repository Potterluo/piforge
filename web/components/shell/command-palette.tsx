"use client";

import { useRouter } from "next/navigation";
import { useTheme } from "next-themes";
import { useQueryClient } from "@tanstack/react-query";
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import {
  Boxes,
  FolderGit2,
  KeyRound,
  LogOut,
  MessagesSquare,
  Moon,
  Plus,
  Search,
  Shield,
  SlidersHorizontal,
  Sparkles,
  Sun,
} from "lucide-react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api/client";
import { sessionStatusLabel } from "@/lib/session/format";
import { useCurrentUser, useProjects, useSessionSearch } from "@/lib/session/queries";

/**
 * One row the palette can run. `id` is the selection key and never changes while
 * the row is on screen: the *label* is allowed to grow asynchronously (a session
 * row learns its project name after the projects query resolves), and selecting
 * by label was the defect this replaced.
 */
interface PaletteEntry {
  id: string;
  /** Text the query is matched against; never rendered. */
  search: string;
  node: ReactNode;
  run: () => void;
  testId?: string;
  data?: Record<string, string>;
}

interface PaletteGroup {
  id: string;
  heading: string;
  entries: PaletteEntry[];
}

/**
 * ⌘K is the keyboard user's complete path through the app (PRD §10.1-5).
 * Everything the sidebar can do should be reachable here.
 *
 * It also searches sessions. The palette used to contain commands and projects
 * only, so typing a session title answered "没有匹配的命令" even though the
 * session existed: a project is a container, but the thing people look for by
 * name is the conversation.
 *
 * Selection is owned here rather than delegated to cmdk (1.1.1). cmdk keys the
 * selected row by its `value` string; a row whose value changes after mount — a
 * session row's label gains its project name when the projects query settles —
 * stops matching the stored value, every row loses `aria-selected`, and Enter
 * becomes a no-op because cmdk only dispatches to the selected row (measured on
 * the previous build: `[cmdk-item][aria-selected="true"]` = 0 while the session
 * was on screen). Deriving the active row from the visible list on every render
 * means the first enabled row is selected as soon as it exists, no matter which
 * query resolves last, and `aria-activedescendant` stays in sync with
 * `aria-selected` because both come from the same value.
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
  // Selection is an id, not an index or a label: ids survive filtering, label
  // changes and list reordering, so a row can never become "selected but gone".
  const [activeId, setActiveId] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const listId = useId();
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
    if (!next) {
      setQuery("");
      setActiveId(null);
    }
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

  const projectRows = projects.data?.projects ?? [];
  const sessionRows = sessions.data?.sessions ?? [];

  const groups: PaletteGroup[] = [
    {
      id: "actions",
      heading: "操作",
      entries: [
        {
          id: "command:new-session",
          search: "新会话 新建会话",
          node: (
            <>
              <Plus />
              新会话
            </>
          ),
          run: onNewSession,
        },
        {
          id: "command:new-project",
          search: "新建项目 项目",
          node: (
            <>
              <FolderGit2 />
              新建项目
            </>
          ),
          run: onNewProject,
        },
      ],
    },
  ];

  if (sessionRows.length > 0) {
    groups.push({
      id: "sessions",
      heading: needle ? "会话" : "最近会话",
      entries: sessionRows.map((session) => {
        const projectName = projectNames.get(session.project_id) ?? "项目";
        const title = session.title || "新会话";
        return {
          id: `session:${session.id}`,
          search: `会话 ${title} ${projectName} ${sessionStatusLabel(session.status)}`,
          testId: "palette-session",
          data: { "data-session-id": session.id, "data-project-id": session.project_id },
          node: (
            <>
              <MessagesSquare />
              <span className="truncate">{title}</span>
              <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">
                {projectName} · {sessionStatusLabel(session.status)}
              </span>
            </>
          ),
          run: () => router.push(`/p/${session.project_id}/s/${session.id}`),
        };
      }),
    });
  }

  if (projectRows.length > 0) {
    groups.push({
      id: "projects",
      heading: "切换项目",
      entries: projectRows.map((project) => ({
        id: `project:${project.id}`,
        search: `项目 ${project.name}`,
        node: (
          <>
            <FolderGit2 />
            {project.name}
          </>
        ),
        run: () => router.push(`/p/${project.id}`),
      })),
    });
  }

  const settingsEntries: PaletteEntry[] = [
    {
      id: "settings:providers",
      search: "模型与凭据 设置",
      node: (
        <>
          <KeyRound />
          模型与凭据
        </>
      ),
      run: () => router.push("/settings/providers"),
    },
    {
      id: "settings:api-keys",
      search: "API Key 设置",
      node: (
        <>
          <KeyRound />
          API Key
        </>
      ),
      run: () => router.push("/settings/api-keys"),
    },
    {
      id: "settings:skills",
      search: "Skill 中心 设置",
      node: (
        <>
          <Sparkles />
          Skill 中心
        </>
      ),
      run: () => router.push("/settings/skills"),
    },
    {
      id: "settings:mcp",
      search: "MCP 中心 设置",
      node: (
        <>
          <Boxes />
          MCP 中心
        </>
      ),
      run: () => router.push("/settings/mcp"),
    },
    {
      id: "settings:general",
      search: "通用设置",
      node: (
        <>
          <SlidersHorizontal />
          通用设置
        </>
      ),
      run: () => router.push("/settings/general"),
    },
  ];
  if (me.data?.user.role === "admin") {
    settingsEntries.push({
      id: "settings:admin-users",
      search: "用户管理 设置",
      node: (
        <>
          <Shield />
          用户管理
        </>
      ),
      run: () => router.push("/admin/users"),
    });
  }
  groups.push({ id: "settings", heading: "设置", entries: settingsEntries });

  groups.push({
    id: "general",
    heading: "通用",
    entries: [
      {
        id: "command:toggle-theme",
        search: resolvedTheme === "dark" ? "切换到浅色主题 主题" : "切换到深色主题 主题",
        node:
          resolvedTheme === "dark" ? (
            <>
              <Sun />
              切换到浅色主题
            </>
          ) : (
            <>
              <Moon />
              切换到深色主题
            </>
          ),
        run: () => setTheme(resolvedTheme === "dark" ? "light" : "dark"),
      },
      {
        id: "command:logout",
        search: "退出登录 登出",
        node: (
          <>
            <LogOut />
            退出登录
          </>
        ),
        run: () => void logout(),
      },
    ],
  });

  // Every whitespace-separated term has to appear, so "api key" finds the API
  // Key command. Case-insensitive substring is enough for a command list and —
  // unlike a fuzzy score — has no surprising matches to explain.
  const terms = needle.toLowerCase().split(/\s+/).filter(Boolean);
  const matches = (entry: PaletteEntry) => {
    if (terms.length === 0) return true;
    const haystack = entry.search.toLowerCase();
    return terms.every((term) => haystack.includes(term));
  };

  const visibleGroups = groups
    .map((group) => ({ ...group, entries: group.entries.filter(matches) }))
    .filter((group) => group.entries.length > 0);
  const visible = visibleGroups.flatMap((group) => group.entries);
  const visibleIds = visible.map((entry) => entry.id);

  // Derived, not stored: if the row the cursor points at is filtered out — or
  // has not arrived yet — the first enabled row is the selection. That is what
  // makes "type, then Enter" work without an arrow key, including when the
  // result list settles after the keystroke.
  const activeIndex = activeId === null ? -1 : visibleIds.indexOf(activeId);
  const effectiveIndex = visible.length === 0 ? -1 : activeIndex >= 0 ? activeIndex : 0;
  const active = effectiveIndex >= 0 ? visible[effectiveIndex] : null;
  const optionId = (index: number) => `${listId}-option-${index}`;

  // Keep the highlighted row in view without moving focus: the input stays the
  // active element, which is the listbox pattern this palette uses.
  useEffect(() => {
    if (!active) return;
    document.getElementById(optionId(effectiveIndex))?.scrollIntoView({ block: "nearest" });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- optionId is derived from listId
  }, [active?.id, effectiveIndex, listId]);

  const move = (delta: number) => {
    if (visible.length === 0) return;
    const next = Math.min(Math.max(effectiveIndex + delta, 0), visible.length - 1);
    setActiveId(visible[next].id);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        move(1);
        return;
      case "ArrowUp":
        event.preventDefault();
        move(-1);
        return;
      case "Home":
        event.preventDefault();
        if (visible.length > 0) setActiveId(visible[0].id);
        return;
      case "End":
        event.preventDefault();
        if (visible.length > 0) setActiveId(visible[visible.length - 1].id);
        return;
      case "Enter":
        if (!active) {
          // No target: either the list is still loading or nothing matches.
          // Enter is ignored rather than queued — the result list can change
          // between the keypress and the response, and a queued Enter would run
          // a row the user never saw. The palette stays open so the next Enter
          // (after the row appears) does what the user meant.
          return;
        }
        event.preventDefault();
        run(active.run);
        return;
      default:
        return;
    }
  };

  const loading = sessions.isFetching || sessions.isPending;

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogHeader className="sr-only">
        <DialogTitle>命令面板</DialogTitle>
        <DialogDescription>快速执行操作</DialogDescription>
      </DialogHeader>
      <DialogContent
        className="top-1/3 translate-y-0 overflow-hidden rounded-xl p-0"
        showCloseButton={false}
      >
        <div className="flex items-center gap-2 border-b border-border px-3">
          <Search className="size-4 shrink-0 opacity-50" aria-hidden="true" />
          <input
            // The dialog owns focus; this keeps keystrokes on the filter even
            // when the row list re-renders underneath.
            autoFocus
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              // A new filter starts at the top: the cursor must not stay on a
              // row that no longer matches.
              setActiveId(null);
            }}
            onKeyDown={onKeyDown}
            placeholder="搜索命令、项目、会话…"
            role="combobox"
            aria-label="搜索命令"
            aria-expanded={open}
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={active ? optionId(effectiveIndex) : undefined}
            className="h-10 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground/60"
          />
        </div>

        <div
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label="命令"
          aria-activedescendant={active ? optionId(effectiveIndex) : undefined}
          className="max-h-72 overflow-x-hidden overflow-y-auto p-1 outline-none"
        >
          {visible.length === 0 ? (
            <p
              data-testid={loading && needle ? "palette-loading" : "palette-empty"}
              className="px-2 py-6 text-center text-sm text-muted-foreground"
            >
              {loading && needle ? "正在搜索…" : "没有匹配的命令"}
            </p>
          ) : (
            visibleGroups.map((group) => (
              <div key={group.id} role="group" aria-labelledby={`${listId}-${group.id}`}>
                <div
                  id={`${listId}-${group.id}`}
                  className="px-2 py-1.5 text-xs font-medium text-muted-foreground"
                >
                  {group.heading}
                </div>
                {group.entries.map((entry) => {
                  const index = visible.indexOf(entry);
                  const selected = index === effectiveIndex;
                  return (
                    <button
                      key={entry.id}
                      id={optionId(index)}
                      type="button"
                      role="option"
                      aria-selected={selected}
                      data-selected={selected ? "true" : undefined}
                      data-testid={entry.testId}
                      {...entry.data}
                      onMouseMove={() => setActiveId(entry.id)}
                      onMouseDown={(event) => {
                        // Keep focus on the input: a click must not blur it
                        // before the action runs.
                        event.preventDefault();
                      }}
                      onClick={() => run(entry.run)}
                      className={
                        selected
                          ? "flex w-full items-center gap-2 rounded-sm bg-muted px-2 py-1.5 text-left text-sm text-foreground"
                          : "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-muted/60"
                      }
                    >
                      {entry.node}
                    </button>
                  );
                })}
              </div>
            ))
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
