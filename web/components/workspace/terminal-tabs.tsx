"use client";

import * as React from "react";
import { PlusIcon, XIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { TerminalPane, type TerminalPaneHandle } from "@/components/workspace/terminal-pane";

/**
 * Terminal tabs (PRD FR-10.2).
 *
 * The server has keyed PTYs by `?tab=` for a while; the UI mounted exactly one
 * pane with the fixed tab name `main`, so "several terminals" existed only in
 * the transport. Each tab here is a real, independent shell: its own `tab` id,
 * its own socket, and its own scrollback, kept mounted when it is not selected
 * so switching does not throw the shell away.
 *
 * Renaming changes the label only. The id is the key the PTY lives under — using
 * the label as the key would kill the shell and start a new one on rename, which
 * is the opposite of what renaming a tab means.
 */

interface TermTab {
  id: string;
  label: string;
}

/** Enough to be "≥ 5" with room to spare, low enough not to be a shell farm. */
const MAX_TABS = 8;

export function TerminalTabs({
  sessionId,
  onOutput,
  paneRef,
  className,
}: {
  sessionId: string;
  onOutput?: (chunk: string) => void;
  /**
   * Receives the *active* pane. The workspace's "reveal in terminal" types `cd`
   * through this handle, so it must follow the selected tab rather than the
   * first one that mounted.
   */
  paneRef?: React.RefObject<TerminalPaneHandle | null>;
  className?: string;
}): React.JSX.Element {
  const [tabs, setTabs] = React.useState<TermTab[]>([{ id: "t1", label: "终端 1" }]);
  const [activeId, setActiveId] = React.useState("t1");
  const [renaming, setRenaming] = React.useState<string | null>(null);
  const [draft, setDraft] = React.useState("");
  const handles = React.useRef(new Map<string, TerminalPaneHandle | null>());
  // Ids are never reused: after closing tab 3, the next tab is 6, not 3 again.
  // Reusing an id would reconnect to the PTY the closed tab left behind.
  const nextNumber = React.useRef(2);

  // Keep the shared handle pointed at the tab that is actually on screen.
  React.useEffect(() => {
    if (paneRef) paneRef.current = handles.current.get(activeId) ?? null;
  }, [activeId, tabs, paneRef]);

  const addTab = React.useCallback(() => {
    if (tabs.length >= MAX_TABS) return;
    // Not `nextNumber.current++`: the React Compiler turns that post-increment
    // into a pre-increment on this build, so the first tab would be numbered 3
    // and the visible list would start with a gap.
    const number = nextNumber.current;
    nextNumber.current = number + 1;
    const tab = { id: `t${number}`, label: `终端 ${number}` };
    setTabs((current) => [...current, tab]);
    setActiveId(tab.id);
  }, [tabs.length]);

  const closeTab = React.useCallback(
    (id: string) => {
      // Ask the server to close this PTY before unmounting: dropping the socket
      // is a *reconnect*, and leaving five orphans per session is how a long
      // session hits the terminal cap.
      handles.current.get(id)?.close();
      handles.current.delete(id);

      const index = tabs.findIndex((tab) => tab.id === id);
      if (index === -1) return;
      const remaining = tabs.filter((tab) => tab.id !== id);
      if (remaining.length === 0) {
        // Closing the last tab opens a fresh one instead of an empty panel
        // with no way back.
        // Same explicit increment as above: this build's compiler miscompiles
        // `ref.current++` into a pre-increment.
        const number = nextNumber.current;
        nextNumber.current = number + 1;
        const tab = { id: `t${number}`, label: `终端 ${number}` };
        setTabs([tab]);
        setActiveId(tab.id);
        return;
      }
      setTabs(remaining);
      if (activeId === id) {
        setActiveId(remaining[Math.min(index, remaining.length - 1)].id);
      }
    },
    [tabs, activeId],
  );

  const commitRename = React.useCallback(
    (id: string) => {
      const label = draft.trim();
      if (label) {
        setTabs((current) =>
          current.map((tab) => (tab.id === id ? { ...tab, label } : tab)),
        );
      }
      setRenaming(null);
      setDraft("");
    },
    [draft],
  );

  return (
    <div className={cn("flex h-full min-h-0 min-w-0 flex-col", className)}>
      <div
        role="tablist"
        aria-label="终端标签"
        data-testid="terminal-tabs"
        className="flex h-8 shrink-0 items-center gap-0.5 overflow-x-auto border-b px-1"
      >
        {tabs.map((tab) => {
          const active = tab.id === activeId;
          return (
            <div
              key={tab.id}
              className={cn(
                "flex shrink-0 items-center rounded-sm",
                active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
              )}
            >
              {renaming === tab.id ? (
                <Input
                  autoFocus
                  data-testid="terminal-tab-rename"
                  aria-label={`重命名 ${tab.label}`}
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  onBlur={() => commitRename(tab.id)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      commitRename(tab.id);
                    } else if (event.key === "Escape") {
                      event.preventDefault();
                      setRenaming(null);
                      setDraft("");
                    }
                  }}
                  className="h-6 w-24 px-1 text-[11px]"
                />
              ) : (
                <button
                  type="button"
                  role="tab"
                  aria-selected={active}
                  data-testid="terminal-tab"
                  data-tab-id={tab.id}
                  title="双击重命名"
                  onClick={() => setActiveId(tab.id)}
                  onDoubleClick={() => {
                    setDraft(tab.label);
                    setRenaming(tab.id);
                  }}
                  className="max-w-40 truncate px-1.5 py-0.5 text-[11px]"
                >
                  {tab.label}
                </button>
              )}
              <button
                type="button"
                data-testid="terminal-tab-close"
                data-tab-id={tab.id}
                aria-label={`关闭 ${tab.label}`}
                title={`关闭 ${tab.label}`}
                onClick={() => closeTab(tab.id)}
                className="me-0.5 flex size-4 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-background/60 hover:text-foreground"
              >
                <XIcon className="size-3" />
              </button>
            </div>
          );
        })}
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          data-testid="terminal-tab-add"
          aria-label="新建终端标签"
          title="新建终端标签"
          disabled={tabs.length >= MAX_TABS}
          onClick={addTab}
          className="ms-0.5 shrink-0"
        >
          <PlusIcon />
        </Button>
        <span className="ms-auto shrink-0 pe-1 font-mono text-[10px] text-muted-foreground">
          {tabs.length} 个终端
        </span>
      </div>

      <div className="min-h-0 min-w-0 flex-1">
        {tabs.map((tab) => (
          <div
            key={tab.id}
            data-testid="terminal-tab-panel"
            data-tab-id={tab.id}
            className={cn("h-full min-h-0 min-w-0", tab.id !== activeId && "hidden")}
          >
            <TerminalPane
              ref={(handle) => {
                handles.current.set(tab.id, handle);
                if (paneRef && tab.id === activeId) paneRef.current = handle;
              }}
              sessionId={sessionId}
              tab={tab.id}
              onOutput={onOutput}
              className="min-h-0 flex-1"
            />
          </div>
        ))}
      </div>
    </div>
  );
}
