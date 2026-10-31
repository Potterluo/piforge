import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

/**
 * The palette's selection contract (measured from the deployed build):
 *
 *   - typing then Enter runs the highlighted row without an arrow key;
 *   - a row whose *label* grows asynchronously (a session row learns its project
 *     name after the projects query resolves) keeps its selection;
 *   - Enter while the list is still loading is ignored, never queued.
 *
 * The old implementation delegated selection to cmdk, which keys the selected
 * row by its `value` string; the label change above invalidated the key and left
 * every row with `aria-selected` absent, so Enter had no target. These tests pin
 * the behaviour, not the implementation.
 */

const mocks = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  state: {
    sessions: {} as Record<string, unknown>,
    projects: undefined as { projects: { id: string; name: string }[] } | undefined,
  },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mocks.push, replace: mocks.replace }),
}));

vi.mock("next-themes", () => ({
  useTheme: () => ({ resolvedTheme: "dark", setTheme: vi.fn() }),
}));

vi.mock("@/lib/api/client", () => ({
  api: { auth: { logout: vi.fn() } },
}));

vi.mock("@/lib/session/queries", () => ({
  useCurrentUser: () => ({ data: { user: { role: "user" } } }),
  useProjects: () => ({ data: mocks.state.projects }),
  useSessionSearch: () => mocks.state.sessions,
}));

import { CommandPalette } from "@/components/shell/command-palette";

const session = {
  id: "ses_1",
  user_id: "usr_1",
  project_id: "prj_1",
  kind: "main",
  title: "调一下缓存淘汰",
  status: "active",
  created_at: "2026-01-01T00:00:00Z",
  last_active_at: "2026-01-01T00:00:00Z",
};

function renderPalette() {
  const onOpenChange = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={client}>
      <CommandPalette
        open
        onOpenChange={onOpenChange}
        onNewSession={vi.fn()}
        onNewProject={vi.fn()}
      />
    </QueryClientProvider>,
  );
  return { ...view, onOpenChange, client };
}

function input(): HTMLInputElement {
  return screen.getByPlaceholderText("搜索命令、项目、会话…") as HTMLInputElement;
}

function options(): HTMLElement[] {
  return screen.queryAllByRole("option");
}

beforeEach(() => {
  mocks.push.mockReset();
  mocks.replace.mockReset();
  mocks.state.sessions = { data: undefined, isPending: true, isFetching: true };
  mocks.state.projects = undefined;
});

describe("command palette selection", () => {
  it("selects the first row by default when the async list settles, and Enter runs it", () => {
    mocks.state.sessions = { data: undefined, isPending: true, isFetching: true };
    const { rerender, onOpenChange, client } = renderPalette();

    fireEvent.change(input(), { target: { value: "调一下缓存" } });
    expect(options()).toHaveLength(0);
    expect(screen.getByTestId("palette-loading")).toBeVisible();

    // The search resolves after the keystroke. No arrow key, no click.
    mocks.state.sessions = {
      data: { sessions: [session] },
      isPending: false,
      isFetching: false,
    };
    rerender(
      <QueryClientProvider client={client}>
        <CommandPalette
          open
          onOpenChange={onOpenChange}
          onNewSession={vi.fn()}
          onNewProject={vi.fn()}
        />
      </QueryClientProvider>,
    );

    const rows = options();
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveAttribute("aria-selected", "true");
    // The accessibility contract: the active descendant names the selected row.
    expect(input()).toHaveAttribute("aria-activedescendant", rows[0].id);

    fireEvent.keyDown(input(), { key: "Enter" });
    expect(mocks.push).toHaveBeenCalledWith("/p/prj_1/s/ses_1");
  });

  it("keeps the selection when a row's label grows after mount", () => {
    // The session row first renders without its project name: the projects query
    // has not answered. Then it gains the name — the exact change that used to
    // invalidate cmdk's string-keyed selection.
    mocks.state.sessions = {
      data: { sessions: [session] },
      isPending: false,
      isFetching: false,
    };
    const { rerender, onOpenChange, client } = renderPalette();

    fireEvent.change(input(), { target: { value: "调一下缓存" } });
    const before = options()[0];
    expect(before).toHaveAttribute("aria-selected", "true");
    expect(before.textContent).toContain("项目");

    mocks.state.projects = { projects: [{ id: "prj_1", name: "ux60-palette" }] };
    rerender(
      <QueryClientProvider client={client}>
        <CommandPalette
          open
          onOpenChange={onOpenChange}
          onNewSession={vi.fn()}
          onNewProject={vi.fn()}
        />
      </QueryClientProvider>,
    );

    const after = options()[0];
    expect(after.textContent).toContain("ux60-palette");
    expect(after).toHaveAttribute("aria-selected", "true");
    expect(input()).toHaveAttribute("aria-activedescendant", after.id);

    fireEvent.keyDown(input(), { key: "Enter" });
    expect(mocks.push).toHaveBeenCalledWith("/p/prj_1/s/ses_1");
  });

  it("ignores Enter while the list is loading instead of queuing it", () => {
    mocks.state.sessions = { data: undefined, isPending: true, isFetching: true };
    const { onOpenChange } = renderPalette();
    fireEvent.change(input(), { target: { value: "调一下缓存" } });
    fireEvent.keyDown(input(), { key: "Enter" });

    expect(mocks.push).not.toHaveBeenCalled();
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByTestId("palette-loading")).toBeVisible();
  });

  it("finds the settings screens when the user types 打开设置", () => {
    // The palette has always listed the settings commands, but their search
    // text was only the screen name: an acceptance pass typed 采用/合并 and
    // concluded the palette had no commands at all. "打开设置" is the phrase a
    // user reaches for, so every settings row answers to it.
    mocks.state.sessions = { data: { sessions: [] }, isPending: false, isFetching: false };
    renderPalette();
    fireEvent.change(input(), { target: { value: "打开设置" } });

    const labels = options().map((option) => option.textContent ?? "");
    expect(labels.some((label) => label.includes("模型与凭据"))).toBe(true);
    expect(labels.some((label) => label.includes("Skill 中心"))).toBe(true);
    expect(labels.some((label) => label.includes("MCP 中心"))).toBe(true);
  });

  it("keeps ArrowUp/Down navigation working", () => {
    mocks.state.sessions = {
      data: { sessions: [session, { ...session, id: "ses_2", title: "调一下另一个会话" }] },
      isPending: false,
      isFetching: false,
    };
    renderPalette();
    fireEvent.change(input(), { target: { value: "调一下" } });

    const rows = options();
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(input(), { key: "ArrowDown" });
    expect(rows[1]).toHaveAttribute("aria-selected", "true");
    expect(rows[0]).toHaveAttribute("aria-selected", "false");

    fireEvent.keyDown(input(), { key: "ArrowUp" });
    expect(rows[0]).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(input(), { key: "Enter" });
    expect(mocks.push).toHaveBeenCalledWith("/p/prj_1/s/ses_1");
  });
});
