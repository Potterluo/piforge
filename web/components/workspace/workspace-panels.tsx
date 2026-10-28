"use client";

import * as React from "react";

import { Marker, MarkerContent, MarkerIcon } from "@/components/ui/marker";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Columns2, GlobeIcon, TerminalIcon, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { GitProvider } from "@/components/workspace/git-data";
import { QuickOpen } from "@/components/workspace/quick-open";
import { FilePreview } from "@/components/workspace/file-preview";
import { FileTree } from "@/components/workspace/file-tree";
import { GitHistoryPanel } from "@/components/workspace/git-history";
import { GitChangesPanel } from "@/components/workspace/git-panel";
import { BrowserPanel } from "@/components/workspace/browser-panel";
import { asPTYInput, buildCdCommand } from "@/lib/terminal/cd";
import { FollowTerminal, TerminalPane, type TerminalPaneHandle } from "@/components/workspace/terminal-pane";

/**
 * The right-hand inspector of the workbench.
 *
 * This is the only entry point `web-shell` mounts:
 *
 * ```tsx
 * import { WorkspacePanels } from "@/components/workspace/workspace-panels";
 * <WorkspacePanels sessionId={session.id} projectId={session.project_id} />
 * ```
 *
 * Layout contract: it fills whatever box it is given (`h-full w-full`), needs
 * `min-w-0` from that box, and survives 280px — every tab is a vertical
 * `Resizable` group so the user can rebalance tree/preview or changes/diff.
 *
 * The four tabs stay mounted: switching away and back must not refetch the
 * git log or tear down the xterm instance.
 */
export function WorkspacePanels({
  sessionId,
  projectId,
  followChunks,
}: {
  sessionId: string;
  projectId: string;
  /** The agent's own bash output, mirrored by the 跟随 panel (FR-9.2). */
  followChunks?: string[];
}): React.JSX.Element {
  const [tab, setTab] = React.useState("files");
  // A second pane (PRD FR-3.4): watching the diff while the terminal runs is the
  // common case, and it should not need a tab switch that tears the terminal down.
  const [split, setSplit] = React.useState<string | null>(null);
  // Bumped whenever something writes into the checkout from the UI side (a browser
  // screenshot today): the file tree refetches and the preview can open the result.
  const [revision, setRevision] = React.useState(0);
  const [openPath, setOpenPath] = React.useState<string | null>(null);
  const [revealTarget, setRevealTarget] = React.useState<string | null>(null);

  const handleOpenFile = React.useCallback((path: string) => {
    setOpenPath(path);
    setTab("files");
  }, []);

  const handleCaptured = React.useCallback((path: string) => {
    setRevision((value) => value + 1);
    setOpenPath(path);
    setTab("files");
  }, []);

  const coverageRef = React.useRef<TerminalPaneHandle | null>(null);

  // Switching the main pane onto the panel the second pane owns is a swap, not a
  // second mount: a panel here can hold a PTY or a screencast socket, and two of
  // either is a bug, not a feature.
  const selectPrimary = React.useCallback(
    (next: string) => {
      if (split && next === split) {
        setSplit(tab);
        setTab(next);
        return;
      }
      setTab(next);
    },
    [split, tab],
  );

  const handleRevealInTerminal = React.useCallback((path: string) => {
    setRevealTarget(path);
    setTab("terminal");
    // Type the cd into the shell itself. Paths that are not provably inert are
    // refused by buildCdCommand and only displayed: a repository can contain a
    // file named `a; rm -rf ~`, and this string goes to a real shell.
    const command = buildCdCommand(path);
    if (command) coverageRef.current?.send(asPTYInput(command));
  }, []);

  return (
    <div
      data-slot="workspace-panels"
      data-testid="workspace-panels"
      data-project={projectId}
      className="flex h-full min-h-0 w-full min-w-0 flex-col overflow-hidden border-s bg-background"
    >
      <QuickOpen sessionId={sessionId} onOpenFile={handleOpenFile} />
      <GitProvider sessionId={sessionId}>
        <div className="flex h-full min-h-0 min-w-0">
        <Tabs
          value={tab}
          onValueChange={(value) => selectPrimary(String(value))}
          className={cn(
            // `flex-1` always: the pane must fill the row even when there is no
            // second one, or the browser surface stops tracking the panel width.
            "flex h-full min-h-0 min-w-0 flex-1 flex-col gap-0",
            split ? "border-e" : "",
          )}
        >
          <TabsList
            variant="line"
            className="h-9 w-full shrink-0 justify-start gap-0 rounded-none border-b bg-transparent px-1 py-0"
          >
            <TabsTrigger value="files" data-testid="panel-tab-files" className="text-xs">
              文件
            </TabsTrigger>
            <TabsTrigger value="changes" data-testid="panel-tab-changes" className="text-xs">
              变更
            </TabsTrigger>
            <TabsTrigger value="git" data-testid="panel-tab-git" className="text-xs">
              Git
            </TabsTrigger>
            <TabsTrigger value="browser" data-testid="panel-tab-browser" className="text-xs">
          <GlobeIcon className="size-3.5" />
          浏览器
        </TabsTrigger>
        <TabsTrigger value="terminal" data-testid="panel-tab-terminal" className="text-xs">
              终端
            </TabsTrigger>
            <TabsTrigger value="follow" data-testid="panel-tab-follow" className="text-xs">
              跟随
            </TabsTrigger>
            <div className="ms-auto flex items-center pe-1">
              <Button
                size="icon-xs"
                variant={split ? "secondary" : "ghost"}
                aria-label={split ? "关闭第二窗格" : "打开第二窗格"}
                aria-pressed={Boolean(split)}
                data-testid="panel-split"
                title={split ? "关闭第二窗格" : "并排显示两个面板"}
                onClick={() => setSplit((current) => (current ? null : otherPanel(tab)))}
              >
                <Columns2 />
              </Button>
            </div>
          </TabsList>

          <TabsContent value="files" keepMounted={split !== "files"} className="min-h-0 min-w-0 flex-1">
            <ResizablePanelGroup orientation="vertical" className="min-h-0 min-w-0">
              <ResizablePanel defaultSize="46%" minSize={120} className="min-h-0">
                <FileTree
            revision={revision}
                  key={sessionId}
                  sessionId={sessionId}
                  activePath={openPath}
                  onOpenFile={handleOpenFile}
                  onRevealInTerminal={handleRevealInTerminal}
                />
              </ResizablePanel>
              <ResizableHandle withHandle />
              <ResizablePanel defaultSize="54%" minSize={120} className="min-h-0">
                <FilePreview
                  key={sessionId}
                  sessionId={sessionId}
                  path={openPath}
                  onClose={() => setOpenPath(null)}
                />
              </ResizablePanel>
            </ResizablePanelGroup>
          </TabsContent>

          <TabsContent value="changes" keepMounted={split !== "changes"} className="min-h-0 min-w-0 flex-1">
            <GitChangesPanel />
          </TabsContent>

          <TabsContent value="git" keepMounted={split !== "git"} className="min-h-0 min-w-0 flex-1">
            <GitHistoryPanel />
          </TabsContent>

          <TabsContent value="browser" keepMounted={split !== "browser"} className="min-h-0 min-w-0 flex-1">
        <BrowserPanel sessionId={sessionId} className="min-h-0 flex-1" onCaptured={handleCaptured} />
      </TabsContent>

      <TabsContent value="terminal" keepMounted={split !== "terminal"} className="min-h-0 min-w-0 flex-1">
            <div className="flex h-full min-h-0 min-w-0 flex-col">
              {revealTarget ? (
                <Marker variant="border" className="shrink-0 px-2 py-1 text-[10px]">
                  <MarkerIcon>
                    <TerminalIcon />
                  </MarkerIcon>
                  <MarkerContent className="truncate">
                    待打开：
                    <code className="font-mono">{revealTarget}</code>（已在该目录执行 cd）
                  </MarkerContent>
                </Marker>
              ) : null}
              <TerminalPane ref={coverageRef} sessionId={sessionId} className="min-h-0 flex-1" />
            </div>
          </TabsContent>

          <TabsContent value="follow" keepMounted className="min-h-0 min-w-0 flex-1">
            <FollowTerminal chunks={followChunks ?? []} className="min-h-0 flex-1" />
          </TabsContent>
        </Tabs>
        </div>
        {split ? (
          <div
            className="flex h-full min-h-0 w-[45%] min-w-0 flex-col"
            data-testid="workspace-split"
            data-panel={split}
          >
            <div className="flex h-9 shrink-0 items-center gap-1 border-b px-2">
              {PANELS.map((panel) => (
                <Button
                  key={panel.id}
                  size="sm"
                  variant={panel.id === split ? "secondary" : "ghost"}
                  className="h-6 px-2 text-xs"
                  data-testid={`split-tab-${panel.id}`}
                  onClick={() => setSplit(panel.id === tab ? split : panel.id)}
                >
                  {panel.label}
                </Button>
              ))}
              <div className="ms-auto">
                <Button
                  size="icon-xs"
                  variant="ghost"
                  aria-label="关闭第二窗格"
                  onClick={() => setSplit(null)}
                >
                  <X />
                </Button>
              </div>
            </div>
            <div className="min-h-0 min-w-0 flex-1">
              <SharedPanel
                id={split}
                sessionId={sessionId}
                openPath={openPath}
                onOpenFile={handleOpenFile}
                onRevealInTerminal={handleRevealInTerminal}
                onCaptured={handleCaptured}
                coverageRef={coverageRef}
                revealTarget={revealTarget}
                sessionKey={`split-${sessionId}`}
              />
            </div>
          </div>
        ) : null}
      </GitProvider>
    </div>
  );
}

/** The panels the second pane can host. Files is excluded: it is itself a split. */
const PANELS = [
  { id: "changes", label: "变更" },
  { id: "git", label: "Git" },
  { id: "browser", label: "浏览器" },
  { id: "terminal", label: "终端" },
] as const;

/** A sensible partner for the panel you are looking at. */
function otherPanel(primary: string): string {
  return primary === "terminal" ? "changes" : "terminal";
}

/**
 * One panel, rendered for the second pane.
 *
 * It is a second *mount*, not a second copy of the state: the primary pane does not
 * mount the panel this pane owns (see `keepMounted` above), so a PTY or a screencast
 * socket is never opened twice.
 */
function SharedPanel({
  id,
  sessionId,
  openPath,
  onOpenFile,
  onRevealInTerminal,
  onCaptured,
  coverageRef,
  revealTarget,
  sessionKey,
}: {
  id: string;
  sessionId: string;
  openPath: string | null;
  onOpenFile: (path: string) => void;
  onRevealInTerminal: (path: string) => void;
  onCaptured: (path: string) => void;
  coverageRef: React.RefObject<TerminalPaneHandle | null>;
  revealTarget: string | null;
  sessionKey: string;
}) {
  switch (id) {
    case "changes":
      return <GitChangesPanel />;
    case "git":
      return <GitHistoryPanel />;
    case "browser":
      return (
        <BrowserPanel
          key={sessionKey}
          sessionId={sessionId}
          className="min-h-0 flex-1"
          onCaptured={onCaptured}
        />
      );
    case "terminal":
      return (
        <div className="flex h-full min-h-0 min-w-0 flex-col">
          {revealTarget ? (
            <Marker variant="border" className="shrink-0 px-2 py-1 text-[10px]">
              <MarkerIcon>
                <TerminalIcon />
              </MarkerIcon>
              <MarkerContent className="truncate">
                待打开：
                <code className="font-mono">{revealTarget}</code>（已在该目录执行 cd）
              </MarkerContent>
            </Marker>
          ) : null}
          <TerminalPane
            key={sessionKey}
            ref={coverageRef}
            sessionId={sessionId}
            className="min-h-0 flex-1"
          />
        </div>
      );
    default:
      return (
        <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
          这个窗格不支持该面板
        </div>
      );
  }
}