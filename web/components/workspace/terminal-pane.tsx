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

/**
 * Terminal pane.
 *
 * There is no PTY transport in this slice: the backend route
 * `/v1/sessions/{id}/pty` is defined, the WebSocket relay is the next
 * milestone. So this component renders a real xterm surface, accepts streamed
 * chunks through its imperative handle, and tells the truth about the missing
 * channel instead of faking a prompt.
 *
 * It is marked with `TODO(pty)` at the two places a relay has to plug in:
 * the socket that feeds `surface.write()` and the `onData` callback that must
 * send keystrokes back (plus a resize message from `TerminalSurface`'s fit).
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
 * The message that must stay accurate until the relay exists. Kept as a
 * constant so the endpoint text is not paraphrased in two places.
 */
export const PTY_NOTICE =
  "终端通道尚未接入（后端 /v1/sessions/{id}/pty 已定义，WebSocket 中继为下一里程碑）";

export interface TerminalPaneHandle {
  write: (chunk: string) => void;
  clear: () => void;
  fit: () => void;
  focus: () => void;
}

export interface TerminalPaneProps {
  sessionId: string;
  /** Flip to true once the WebSocket relay lands and a PTY is attached. */
  connected?: boolean;
  onData?: (chunk: string) => void;
  className?: string;
}

export const TerminalPane = React.forwardRef<TerminalPaneHandle, TerminalPaneProps>(
  function TerminalPane({ sessionId, connected = false, onData, className }, ref) {
    const surfaceRef = React.useRef<TerminalSurfaceHandle | null>(null);
    const [fontSize, setFontSize] = React.useState(12);

    React.useImperativeHandle(
      ref,
      () => ({
        write: (chunk) => surfaceRef.current?.write(chunk),
        clear: () => surfaceRef.current?.clear(),
        fit: () => surfaceRef.current?.fit(),
        focus: () => surfaceRef.current?.focus(),
      }),
      [],
    );

    const handleSearch = React.useCallback((term: string) => {
      if (term) surfaceRef.current?.search(term);
    }, []);

    return (
      <div
        data-testid="terminal-pane"
        className={cn("flex h-full min-h-0 min-w-0 flex-col", className)}
      >
        <Toolbar
          title={`bash · ${sessionId.slice(0, 8)}`}
          connected={connected}
          fontSize={fontSize}
          onFontSize={setFontSize}
          onSearch={handleSearch}
          onClear={() => surfaceRef.current?.clear()}
        />

        <div className="relative flex min-h-0 min-w-0 flex-1 flex-col bg-background">
          <div className={cn("min-h-0 flex-1", !connected && "pointer-events-none opacity-30")}>
            <SurfaceHost
              fontSize={fontSize}
              onData={onData}
              onReady={(handle) => {
                surfaceRef.current = handle;
              }}
            />
          </div>

          {!connected ? (
            <div
              data-testid="terminal-disconnected"
              className="absolute inset-0 flex items-center justify-center bg-background/70 p-3"
            >
              <Empty>
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <TerminalIcon />
                  </EmptyMedia>
                  <EmptyTitle>终端未连接</EmptyTitle>
                  <EmptyDescription>{PTY_NOTICE}</EmptyDescription>
                </EmptyHeader>
                <EmptyContent>
                  <code className="block w-full truncate rounded border bg-muted px-2 py-1 text-left font-mono text-[10px]">
                    GET /v1/sessions/{sessionId}/pty
                  </code>
                  <p className="text-[10px] text-muted-foreground">
                    等待 WebSocket 中继：字节偏移重连 + ring buffer 回放
                  </p>
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
