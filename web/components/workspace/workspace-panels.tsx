"use client";

import * as React from "react";

import { Marker, MarkerContent, MarkerIcon } from "@/components/ui/marker";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { GlobeIcon, TerminalIcon } from "lucide-react";
import { GitProvider } from "@/components/workspace/git-data";
import { QuickOpen } from "@/components/workspace/quick-open";
import { FilePreview } from "@/components/workspace/file-preview";
import { FileTree } from "@/components/workspace/file-tree";
import { GitHistoryPanel } from "@/components/workspace/git-history";
import { GitChangesPanel } from "@/components/workspace/git-panel";
import { BrowserPanel } from "@/components/workspace/browser-panel";
import { asPTYInput, buildCdCommand } from "@/lib/terminal/cd";
import { TerminalPane, type TerminalPaneHandle } from "@/components/workspace/terminal-pane";

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
}: {
  sessionId: string;
  projectId: string;
}): React.JSX.Element {
  const [tab, setTab] = React.useState("files");
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
        <Tabs
          value={tab}
          onValueChange={(value) => setTab(String(value))}
          className="flex h-full min-h-0 min-w-0 flex-col gap-0"
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
          </TabsList>

          <TabsContent value="files" keepMounted className="min-h-0 min-w-0 flex-1">
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

          <TabsContent value="changes" keepMounted className="min-h-0 min-w-0 flex-1">
            <GitChangesPanel />
          </TabsContent>

          <TabsContent value="git" keepMounted className="min-h-0 min-w-0 flex-1">
            <GitHistoryPanel />
          </TabsContent>

          <TabsContent value="browser" keepMounted className="min-h-0 min-w-0 flex-1">
        <BrowserPanel sessionId={sessionId} className="min-h-0 flex-1" onCaptured={handleCaptured} />
      </TabsContent>

      <TabsContent value="terminal" keepMounted className="min-h-0 min-w-0 flex-1">
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
        </Tabs>
      </GitProvider>
    </div>
  );
}
