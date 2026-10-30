"use client";

import * as React from "react";
import dynamic from "next/dynamic";
import { EraserIcon, MinusIcon, PlusIcon, SearchIcon, TerminalIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import type { TerminalSurfaceHandle } from "@/components/workspace/terminal-surface";
import { usePtySession } from "@/lib/terminal/use-pty";

/**
 * Terminal pane: a real shell inside the session's sandbox.
 *
 * The transport is the control plane's WebSocket relay (`usePtySession`). The
 * overlay shown while connecting is not a placeholder — it is what a user sees
 * when the socket is down, and it carries the endpoint so a bug report says
 * something useful.
 */
const TerminalSurface = dynamic(
  () => import("@/components/workspace/terminal-surface").then((mod) => mod.TerminalSurface),
  { ssr: false, loading: () => <div className="min-h-0 flex-1" /> },
);

function SurfaceHost({
  readOnly,
  fontSize,
  scrollback,
  onData,
  onReady,
}: {
  readOnly?: boolean;
  fontSize: number;
  scrollback?: number;
  onData?: (chunk: string) => void;
  onReady: (handle: TerminalSurfaceHandle | null) => void;
}) {
  return (
    <TerminalSurface
      readOnly={readOnly}
      fontSize={fontSize}
      scrollback={scrollback}
      onData={onData}
      onReady={onReady}
      className="h-full min-h-0 min-w-0 px-1 py-1"
    />
  );
}

function Toolbar({
  title,
  connected,
  follow,
  fontSize,
  onFontSize,
  onSearch,
  onClear,
}: {
  title: string;
  /** `undefined` hides the badge — the follow pane has no connection to show. */
  connected?: boolean;
  follow?: boolean;
  fontSize: number;
  onFontSize: (next: number) => void;
  onSearch: (term: string) => void;
  onClear: () => void;
}) {
  const [term, setTerm] = React.useState("");

  return (
    <header className="flex shrink-0 flex-wrap items-center gap-1 border-b px-2 py-1">
      <TerminalIcon className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="min-w-0 truncate text-[11px]">{title}</span>
      {connected === undefined ? null : (
        <Badge variant={connected ? "secondary" : "outline"} className="shrink-0 text-[10px]">
          {connected ? "已连接" : "未连接"}
        </Badge>
      )}
      {follow ? (
        <Badge variant="outline" className="shrink-0 text-[10px]">
          agent 只读
        </Badge>
      ) : null}

      <div className="ms-auto flex items-center gap-0.5">
        <div className="relative">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-1.5 size-3 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={term}
            onChange={(event) => {
              setTerm(event.target.value);
              onSearch(event.target.value);
            }}
            placeholder="搜索输出"
            aria-label="搜索终端输出"
            className="h-6 w-28 pl-6 text-[11px]"
          />
        </div>
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="缩小字号"
          onClick={() => onFontSize(Math.max(9, fontSize - 1))}
        >
          <MinusIcon />
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="放大字号"
          onClick={() => onFontSize(Math.min(18, fontSize + 1))}
        >
          <PlusIcon />
        </Button>
        <Button variant="ghost" size="icon-xs" aria-label="清屏" onClick={onClear}>
          <EraserIcon />
        </Button>
      </div>
    </header>
  );
}

/**
 * Shown while the socket is not open. The endpoint is included on purpose: a
 * user reporting "the terminal is dead" then hands over the exact route.
 */
export const PTY_NOTICE = "正在连接 /v1/sessions/{id}/pty …";

export interface TerminalPaneHandle {
  write: (chunk: string) => void;
  /** Types into the shell itself, as opposed to `write`, which only displays. */
  send: (data: string) => void;
  clear: () => void;
  fit: () => void;
  focus: () => void;
  /** Closes the PTY behind this pane (a tab was closed, not disconnected). */
  close: () => void;
}

export interface TerminalPaneProps {
  sessionId: string;
  /** Overrides the live socket state; used by tests and the follow-only pane. */
  connected?: boolean;
  /** Tab name inside the session; several terminals may coexist. */
  tab?: string;
  onData?: (chunk: string) => void;
  /**
   * Fired for every chunk the shell prints.
   *
   * This is the workspace's only signal that a *terminal* command ran: the
   * session event stream carries the agent's tool calls, not this PTY, so a
   * file created with `echo > file` would otherwise stay invisible to the file
   * tree until someone clicked refresh.
   */
  onOutput?: (chunk: string) => void;
  className?: string;
}

export const TerminalPane = React.forwardRef<TerminalPaneHandle, TerminalPaneProps>(
  function TerminalPane({ sessionId, connected, tab = "main", onData, onOutput, className }, ref) {
    const surfaceRef = React.useRef<TerminalSurfaceHandle | null>(null);
    const [fontSize, setFontSize] = React.useState(12);
    const pty = usePtySession({ sessionId, tab, enabled: connected !== false });

    const live = connected ?? pty.status === "open";

    React.useImperativeHandle(
      ref,
      () => ({
        write: (chunk) => surfaceRef.current?.write(chunk),
        send: (data) => pty.send(data),
        clear: () => surfaceRef.current?.clear(),
        fit: () => surfaceRef.current?.fit(),
        focus: () => surfaceRef.current?.focus(),
        close: () => pty.terminate(),
      }),
      [pty],
    );

    // Read through a ref: the caller passes an inline callback, and resubscribing
    // to the socket on every render would drop bytes that arrived in between.
    const outputRef = React.useRef(onOutput);
    React.useEffect(() => {
      outputRef.current = onOutput;
    });

    // Feed every byte the relay delivers into the terminal.
    React.useEffect(() => {
      return pty.onData((chunk) => {
        surfaceRef.current?.write(chunk);
        outputRef.current?.(chunk);
      });
    }, [pty]);

    const handleSearch = React.useCallback((term: string) => {
      if (term) surfaceRef.current?.search(term);
    }, []);

    return (
      <div
        data-testid="terminal-pane"
        data-pty-tab={tab}
        data-pty-status={pty.status}
        className={cn("flex h-full min-h-0 min-w-0 flex-col", className)}
      >
        <Toolbar
          title={tab === "main" ? `bash · ${sessionId.slice(0, 8)}` : `${tab} · bash`}
          connected={live}
          fontSize={fontSize}
          onFontSize={setFontSize}
          onSearch={handleSearch}
          onClear={() => surfaceRef.current?.clear()}
        />

        <div className="relative flex min-h-0 min-w-0 flex-1 flex-col bg-background">
          <div className={cn("min-h-0 flex-1", !live && "pointer-events-none opacity-30")}>
            <SurfaceHost
              fontSize={fontSize}
              onData={(chunk) => {
                pty.send(chunk);
                onData?.(chunk);
              }}
              onReady={(handle) => {
                surfaceRef.current = handle;
              }}
            />
          </div>

          {!live ? (
            <div
              data-testid="terminal-disconnected"
              className="absolute inset-0 flex items-center justify-center bg-background/70 p-3"
            >
              <Empty>
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <TerminalIcon />
                  </EmptyMedia>
                  <EmptyTitle>
                    {pty.status === "reconnecting"
                      ? "正在重连终端"
                      : pty.status === "error"
                        ? "终端连接失败"
                        : "正在连接终端"}
                  </EmptyTitle>
                  <EmptyDescription>{pty.error ?? PTY_NOTICE}</EmptyDescription>
                </EmptyHeader>
                <EmptyContent>
                  <code className="block w-full truncate rounded border bg-muted px-2 py-1 text-left font-mono text-[10px]">
                    WS /v1/sessions/{sessionId}/pty
                  </code>
                  {pty.status === "error" ? (
                    <Button size="sm" variant="outline" onClick={pty.reconnect}>
                      重新连接
                    </Button>
                  ) : null}
                </EmptyContent>
              </Empty>
            </div>
          ) : null}
        </div>
      </div>
    );
  },
);

export interface FollowTerminalProps {
  /**
   * The agent's bash output so far, already bounded (see `capFollowText`).
   *
   * This is a growing string rather than a list of chunks because the source is
   * a cumulative stream: Pi's `partialResult` repeats the output produced so far,
   * so the newest snapshot *contains* the previous one. Comparing lengths is the
   * only way to append exactly the bytes that are new.
   */
  text?: string;
  label?: string;
  className?: string;
  /** Lines of scrollback the pane keeps; matches the source cap. */
  scrollback?: number;
}

/**
 * Read-only, live mirror of the agent's `bash` output.
 *
 * The data source is the session event stream (`tool.progress`), not a byte
 * stream from a TTY we do not own: the component never invents input. It appends
 * only the part of `text` it has not written yet, so output shows up while the
 * command is still running instead of after it settles.
 *
 * Two cases used to lose output, and both are handled here:
 *   - the tab is opened *after* the command started, so `text` is non-empty at
 *     mount while xterm is still loading (bytes are buffered until it exists);
 *   - the source shrinks because `capFollowText` dropped the oldest lines (the
 *     surface is reset and re-rendered from the new tail).
 */
export function FollowTerminal({
  text = "",
  label = "跟随终端",
  className,
  scrollback,
}: FollowTerminalProps) {
  const surfaceRef = React.useRef<TerminalSurfaceHandle | null>(null);
  // Characters of `text` already handed to xterm (or buffered for it).
  const writtenRef = React.useRef(0);
  // Bytes that arrived before xterm finished mounting. Dropping them would make
  // a tab opened mid-command start at whatever line came next.
  const pendingRef = React.useRef("");
  const [fontSize, setFontSize] = React.useState(12);

  const write = React.useCallback((chunk: string) => {
    if (!chunk) return;
    const surface = surfaceRef.current;
    if (!surface) {
      pendingRef.current += chunk;
      return;
    }
    // Follow only if the user was already at the bottom: scrolling up to read
    // older output must not be undone by the next line the agent prints.
    const stick = surface.isAtBottom();
    surface.write(chunk);
    if (stick) surface.scrollToBottom();
  }, []);

  React.useEffect(() => {
    // The source shrank, which only happens when the cap dropped old lines (or a
    // resync replaced the transcript). Appending to the surface would then show
    // the old and the new tail stitched together, so rebuild it.
    if (text.length < writtenRef.current) {
      writtenRef.current = 0;
      pendingRef.current = "";
      surfaceRef.current?.reset();
    }
    if (text.length === writtenRef.current) return;
    const delta = text.slice(writtenRef.current);
    writtenRef.current = text.length;
    write(delta);
  }, [text, write]);

  const handleReady = React.useCallback((handle: TerminalSurfaceHandle | null) => {
    surfaceRef.current = handle;
    if (!handle || !pendingRef.current) return;
    handle.write(pendingRef.current);
    pendingRef.current = "";
    handle.scrollToBottom();
  }, []);

  return (
    <div
      data-testid="follow-terminal"
      className={cn("flex h-full min-h-0 min-w-0 flex-col", className)}
    >
      <Toolbar
        title={label}
        follow
        fontSize={fontSize}
        onFontSize={setFontSize}
        onSearch={(term) => {
          if (term) surfaceRef.current?.search(term);
        }}
        onClear={() => {
          surfaceRef.current?.clear();
          // Clearing is a view action, not a data reset: keep `writtenRef` so the
          // next chunk appends instead of replaying the whole transcript.
        }}
      />
      <div className="min-h-0 min-w-0 flex-1 bg-background">
        <SurfaceHost
          readOnly
          scrollback={scrollback}
          fontSize={fontSize}
          onReady={handleReady}
        />
      </div>
    </div>
  );
}
