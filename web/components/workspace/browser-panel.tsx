"use client";

import * as React from "react";
import { GlobeIcon, RotateCwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";

/**
 * The browser panel.
 *
 * One Chrome per sandbox, so every viewer of a session sees the same page the
 * agent is driving (PRD §7.7). Frames arrive as binary JPEG over the control
 * plane's WebSocket and are painted to a canvas; clicks, keys and scrolls go
 * back as JSON commands.
 *
 * The panel is deliberately a viewer with input, not a second browser: running
 * a separate headless browser in the tab would see different cookies and a
 * different page than the agent, which makes the panel a lie.
 */

type Status = "connecting" | "open" | "error" | "closed";

interface FrameMeta {
  width: number;
  height: number;
}

export interface BrowserPanelProps {
  sessionId: string;
  className?: string;
}

function wsBase(): string {
  const configured = process.env.NEXT_PUBLIC_PIFORGE_WS_BASE?.trim();
  if (configured) return configured.replace(/\/$/, "");
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}`;
}

export function BrowserPanel({ sessionId, className }: BrowserPanelProps) {
  const canvasRef = React.useRef<HTMLCanvasElement | null>(null);
  const socketRef = React.useRef<WebSocket | null>(null);
  const [status, setStatus] = React.useState<Status>("connecting");
  const [error, setError] = React.useState<string | null>(null);
  const [meta, setMeta] = React.useState<FrameMeta | null>(null);
  const [url, setUrl] = React.useState("https://example.com");
  const [generation, setGeneration] = React.useState(0);

  React.useEffect(() => {
    let disposed = false;
    const socket = new WebSocket(`${wsBase()}/v1/sessions/${sessionId}/browser?tab=view`);
    socket.binaryType = "blob";
    socketRef.current = socket;

    socket.onopen = () => {
      if (disposed) return;
      setStatus("open");
      setError(null);
    };

    socket.onmessage = async (event) => {
      if (disposed) return;
      if (typeof event.data === "string") {
        try {
          const message = JSON.parse(event.data) as {
            type?: string;
            message?: string;
            width?: number;
            height?: number;
          };
          if (message.type === "frame" && message.width && message.height) {
            setMeta({ width: message.width, height: message.height });
          } else if (message.type === "error") {
            setError(message.message ?? "浏览器不可用");
            setStatus("error");
          }
        } catch {
          // A non-JSON text frame carries nothing we can use.
        }
        return;
      }

      // Paint the frame. createImageBitmap keeps decoding off the paint path,
      // which matters when frames arrive faster than the display refreshes.
      const bitmap = await createImageBitmap(event.data as Blob).catch(() => null);
      if (!bitmap || disposed) return;
      const canvas = canvasRef.current;
      if (canvas) {
        if (canvas.width !== bitmap.width || canvas.height !== bitmap.height) {
          canvas.width = bitmap.width;
          canvas.height = bitmap.height;
        }
        canvas.getContext("2d")?.drawImage(bitmap, 0, 0);
      }
      bitmap.close();
    };

    socket.onclose = () => {
      if (!disposed) setStatus("closed");
    };
    socket.onerror = () => {
      if (!disposed) setStatus("error");
    };

    return () => {
      disposed = true;
      socket.close(1000, "unmount");
      socketRef.current = null;
    };
  }, [sessionId, generation]);

  const send = React.useCallback((payload: unknown) => {
    const socket = socketRef.current;
    if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(payload));
  }, []);

  // Page coordinates: the canvas is scaled to fit, so a click has to be mapped
  // back through the element's client size.
  const toPagePoint = React.useCallback((event: React.MouseEvent<HTMLCanvasElement>) => {
    const canvas = event.currentTarget;
    const rect = canvas.getBoundingClientRect();
    const width = meta?.width || canvas.width || rect.width;
    const height = meta?.height || canvas.height || rect.height;
    return {
      x: ((event.clientX - rect.left) / rect.width) * width,
      y: ((event.clientY - rect.top) / rect.height) * height,
    };
  }, [meta]);

  const handleClick = (event: React.MouseEvent<HTMLCanvasElement>) => {
    const { x, y } = toPagePoint(event);
    send({ type: "input", input: { type: "click", x, y, button: event.button === 2 ? "right" : "left" } });
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    // Let the browser keep its own shortcuts; the page gets everything else.
    if (event.key === "Tab") return;
    event.preventDefault();
    send({
      type: "input",
      input: {
        type: "key",
        key: event.key,
        code: event.code,
        text: event.key.length === 1 ? event.key : "",
      },
    });
  };

  const handleWheel = (event: React.WheelEvent<HTMLCanvasElement>) => {
    const { x, y } = toPagePoint(event);
    send({ type: "input", input: { type: "scroll", x, y, delta_y: event.deltaY } });
  };

  const navigate = () => {
    const target = url.trim();
    if (!target) return;
    send({ type: "navigate", url: target.startsWith("http") ? target : `https://${target}` });
  };

  const live = status === "open";

  return (
    <div
      data-testid="browser-panel"
      data-browser-status={status}
      className={cn("flex h-full min-h-0 min-w-0 flex-col", className)}
    >
      <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-2 py-1.5">
        <GlobeIcon className="size-3.5 shrink-0 text-muted-foreground" />
        <Input
          data-testid="browser-url"
          value={url}
          onChange={(event) => setUrl(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") navigate();
          }}
          className="h-6 min-w-0 flex-1 font-mono text-[11px]"
          aria-label="地址"
        />
        <Button size="icon-xs" variant="ghost" aria-label="打开" onClick={navigate} disabled={!live}>
          <RotateCwIcon />
        </Button>
        <Badge variant={live ? "secondary" : "outline"} className="text-[10px]">
          {live ? "已连接" : status === "error" ? "不可用" : "连接中"}
        </Badge>
      </div>

      <div
        className="relative flex min-h-0 min-w-0 flex-1 items-center justify-center bg-muted/30 p-1"
        tabIndex={0}
        onKeyDown={handleKeyDown}
        data-testid="browser-surface"
      >
        <canvas
          ref={canvasRef}
          onClick={handleClick}
          onWheel={handleWheel}
          onContextMenu={(event) => event.preventDefault()}
          className={cn("max-h-full max-w-full object-contain", !live && "opacity-0")}
          data-testid="browser-canvas"
        />

        {!live ? (
          <div
            data-testid="browser-idle"
            className="absolute inset-0 flex items-center justify-center p-3"
          >
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  {status === "connecting" ? <Spinner /> : <GlobeIcon />}
                </EmptyMedia>
                <EmptyTitle>
                  {status === "error" ? "浏览器不可用" : status === "connecting" ? "正在连接浏览器" : "浏览器已断开"}
                </EmptyTitle>
                <EmptyDescription>
                  {error ?? "每个沙箱一个 Chrome，画面就是 agent 正在操作的那个页面。"}
                </EmptyDescription>
              </EmptyHeader>
              {status !== "connecting" ? (
                <Button size="sm" variant="outline" onClick={() => setGeneration((g) => g + 1)}>
                  重新连接
                </Button>
              ) : null}
            </Empty>
          </div>
        ) : null}
      </div>
    </div>
  );
}
