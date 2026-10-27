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
  onData,
  onReady,
}: {
  readOnly?: boolean;
  fontSize: number;
  onData?: (chunk: string) => void;
  onReady: (handle: TerminalSurfaceHandle | null) => void;
}) {
  return (
    <TerminalSurface
      readOnly={readOnly}
      fontSize={fontSize}
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
}

export interface TerminalPaneProps {
  sessionId: string;
  /** Overrides the live socket state; used by tests and the follow-only pane. */
  connected?: boolean;
  /** Tab name inside the session; several terminals may coexist. */
  tab?: string;
  onData?: (chunk: string) => void;
  className?: string;
}

export const TerminalPane = React.forwardRef<TerminalPaneHandle, TerminalPaneProps>(
  function TerminalPane({ sessionId, connected, tab = "main", onData, className }, ref) {
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
      }),
      [],
    );

    // Feed every byte the relay delivers into the terminal.
    React.useEffect(() => {
      return pty.onData((chunk) => surfaceRef.current?.write(chunk));
    }, [pty]);

    const handleSearch = React.useCallback((term: string) => {
      if (term) surfaceRef.current?.search(term);
    }, []);

    return (
      <div
        data-testid="terminal-pane"
        data-pty-status={pty.status}
        className={cn("flex h-full min-h-0 min-w-0 flex-col", className)}
      >
        <Toolbar
          title={`bash · ${sessionId.slice(0, 8)}`}
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

export interface FollowTerminalHandle {
  /** Appends a chunk; safe to call from an event-stream listener. */
  append: (chunk: string) => void;
  reset: () => void;
}

export interface FollowTerminalProps {
  /**
   * Optional pre-buffered chunks. Entries are appended exactly once (tracked by
   * index), so a shell can drive this either with a growing array or with the
   * imperative handle.
   */
  chunks?: string[];
  label?: string;
  className?: string;
}

/**
 * Read-only mirror of the agent's `bash` output.
 *
 * Wiring for `web-shell`: hold a ref, then in the session event stream:
 *
 * ```tsx
 * const followRef = useRef<FollowTerminalHandle>(null);
 * // on pty.stream / tool.progress events:
 * followRef.current?.append(payload.chunk ?? "");
 * // <FollowTerminal ref={followRef} />
 * ```
 *
 * The data source is the event stream (`partialResult` deltas), not a byte
 * stream from a TTY we do not own; the component never invents input.
 */
export const FollowTerminal = React.forwardRef<FollowTerminalHandle, FollowTerminalProps>(
  function FollowTerminal({ chunks, label = "跟随终端", className }, ref) {
    const surfaceRef = React.useRef<TerminalSurfaceHandle | null>(null);
    const consumed = React.useRef(0);
    const [fontSize, setFontSize] = React.useState(12);

    const append = React.useCallback((chunk: string) => {
      surfaceRef.current?.write(chunk);
    }, []);

    React.useImperativeHandle(
      ref,
      () => ({
        append,
        reset: () => {
          surfaceRef.current?.reset();
          consumed.current = 0;
        },
      }),
      [append],
    );

    React.useEffect(() => {
      if (!chunks) return;
      for (let index = consumed.current; index < chunks.length; index++) {
        append(chunks[index]);
      }
      consumed.current = chunks.length;
    }, [append, chunks]);

    return (
      <div className={cn("flex h-full min-h-0 min-w-0 flex-col", className)}>
        <Toolbar
          title={label}
          follow
          fontSize={fontSize}
          onFontSize={setFontSize}
          onSearch={(term) => {
            if (term) surfaceRef.current?.search(term);
          }}
          onClear={() => surfaceRef.current?.clear()}
        />
        <div className="min-h-0 min-w-0 flex-1 bg-background">
          <SurfaceHost
            readOnly
            fontSize={fontSize}
            onReady={(handle) => {
              surfaceRef.current = handle;
            }}
          />
        </div>
      </div>
    );
  },
);
