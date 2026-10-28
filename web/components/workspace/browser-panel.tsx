"use client";

import * as React from "react";
import { GlobeIcon, RotateCwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";
import { useFeature } from "@/lib/session/queries";

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
  // "Attached to the browser" and "the page has drawn something" are different
  // facts. Without this the panel says 已连接 over an empty canvas for the ten to
  // twenty seconds a page takes to load, which reads as a broken panel.
  // Starts true: an untouched panel is blank, not loading. It flips false when a
  // navigation is sent and true again when the first frame of that page arrives.
  const [painted, setPainted] = React.useState(true);
  // A disabled feature is not a broken socket: say which one it is instead of
  // looping 连接中 → 已断开 → 重新连接 forever.
  const browserEnabled = useFeature("browser");
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
      // The size was measured before the socket existed, so the request made on
      // mount had nowhere to go. Send it now: otherwise the first frame is the
      // launch size and the page stays letterboxed until something resizes.
      const measured = surfaceSizeRef.current;
      if (measured.width >= 120 && measured.height >= 120) {
        socket.send(JSON.stringify({ type: "resize", ...measured }));
      }
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
            // The control plane sends this text frame right after every binary
            // frame, so it is a cheaper and more reliable "a frame arrived" signal
            // than the bitmap decode path below.
            setPainted(true);
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
      setPainted((was) => was || bitmap.width > 0);
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

  // The sandbox renders at the size the viewer shows, not at a fixed window size:
  // collapsing the sidebar or resizing the window must reflow the page, otherwise
  // a responsive site keeps its 1280x800 layout and gets letterboxed.
  const surfaceRef = React.useRef<HTMLDivElement | null>(null);
  const surfaceSizeRef = React.useRef({ width: 0, height: 0 });
  React.useEffect(() => {
    const surface = surfaceRef.current;
    if (!surface) return;
    let timer: number | undefined;
    let last = { width: 0, height: 0 };
    const push = () => {
      const rect = surface.getBoundingClientRect();
      const width = Math.round(rect.width);
      const height = Math.round(rect.height);
      if (width < 120 || height < 120) return;
      if (Math.abs(width - last.width) < 8 && Math.abs(height - last.height) < 8) return;
      last = { width, height };
      surfaceSizeRef.current = last;
      send({ type: "resize", width, height });
    };
    const observer = new ResizeObserver(() => {
      if (timer) window.clearTimeout(timer);
      timer = window.setTimeout(push, 120);
    });
    observer.observe(surface);
    push();
    return () => {
      observer.disconnect();
      if (timer) window.clearTimeout(timer);
    };
  }, [send, sessionId, generation]);

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
    // The old page stays on the canvas until the new one paints; clearing the
    // flag here is what keeps the loading hint honest about which page it is for.
    setPainted(false);
    send({ type: "navigate", url: target.startsWith("http") ? target : `https://${target}` });
    // A page that refuses to load sends no frame at all, and an overlay that
    // never clears is indistinguishable from a broken panel. Give up explaining
    // after this long and let the browser's own error page show through.
    window.setTimeout(() => setPainted(true), 15_000);
  };

  const live = status === "open";
  const loadingPage = live && !painted;

  if (!browserEnabled) {
    return (
      <div
        data-testid="browser-panel"
        data-browser-status="disabled"
        className={cn("flex h-full min-h-0 min-w-0 flex-col items-center justify-center gap-2 p-6", className)}
      >
        <p className="text-sm">内置浏览器已被管理员关闭</p>
        <p className="max-w-sm text-center text-xs text-muted-foreground">
          这是账号级别的开关。需要它的话请联系部署管理员，在「用户管理 → 功能」里打开。
        </p>
      </div>
    );
  }

  return (
    <div
      data-testid="browser-panel"
      data-browser-status={status}
      data-browser-painted={painted ? "1" : "0"}
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
        ref={surfaceRef}
        className="relative flex min-h-0 min-w-0 flex-1 items-center justify-center overflow-hidden bg-muted/30"
        tabIndex={0}
        onKeyDown={handleKeyDown}
        data-testid="browser-surface"
      >
        {/*
          `block` matters: an inline canvas adds a descender gap, which shows up as
          the panel being a few pixels taller than the frame it displays.
        */}
        <canvas
          ref={canvasRef}
          onClick={handleClick}
          onWheel={handleWheel}
          onContextMenu={(event) => event.preventDefault()}
          className={cn("block h-full w-full object-contain", !live && "opacity-0")}
          data-testid="browser-canvas"
        />

        {loadingPage ? (
          <div
            data-testid="browser-loading"
            className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center gap-2 bg-background/70 text-xs text-muted-foreground backdrop-blur-[1px]"
          >
            <Spinner />
            正在加载页面…
          </div>
        ) : null}

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
