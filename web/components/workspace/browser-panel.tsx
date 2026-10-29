"use client";

import * as React from "react";
import { Camera as CameraIcon, GlobeIcon, Plus as PlusIcon, RotateCwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { toast } from "sonner";
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
  /** Called with the workspace-relative path after a screenshot is written. */
  onCaptured?: (path: string) => void;
}

function wsBase(): string {
  const configured = process.env.NEXT_PUBLIC_PIFORGE_WS_BASE?.trim();
  if (configured) return configured.replace(/\/$/, "");
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}`;
}

/**
 * The address to send for what was typed.
 *
 * `about:blank` must survive verbatim: prefixing it produced
 * `https://about:blank`, which is a hostname that does not exist, so the blank
 * page the capture refusal is about was unreachable from the UI (QA-16, FIX D).
 * http(s) is already complete; anything else is treated as a hostname.
 */
export function navigationTarget(raw: string): string {
  const target = raw.trim();
  if (!target) return "";
  const lower = target.toLowerCase();
  if (lower === "about:blank") return "about:blank";
  if (lower.startsWith("http://") || lower.startsWith("https://")) return target;
  return `https://${target}`;
}

export function BrowserPanel({ sessionId, className, onCaptured }: BrowserPanelProps) {
  const canvasRef = React.useRef<HTMLCanvasElement | null>(null);
  const socketRef = React.useRef<WebSocket | null>(null);
  const [status, setStatus] = React.useState<Status>("connecting");
  // "Attached to the browser" and "the page has drawn something" are different
  // facts. Without this the panel says 已连接 over an empty canvas for the ten to
  // twenty seconds a page takes to load, which reads as a broken panel.
  // Starts true: an untouched panel is blank, not loading. It flips false when a
  // navigation is sent and true again when the first frame of that page arrives.
  // False until a frame actually arrives. Starting at `true` meant a viewer whose
  // socket opened but whose screencast never produced a frame said 已连接 over an
  // empty canvas — exactly how a broken tab target looked healthy.
  const [painted, setPainted] = React.useState(false);
  // FR-11.2/11.4: observation by default. The server enforces this too — the panel only
  // decides what it offers, not what is allowed.
  const [takeover, setTakeover] = React.useState(false);
  // A disabled feature is not a broken socket: say which one it is instead of
  // looping 连接中 → 已断开 → 重新连接 forever.
  const browserEnabled = useFeature("browser");
  React.useEffect(() => {
    let cancelled = false;
    void api.browser
      .mode(sessionId)
      .then((result) => {
        if (!cancelled) setTakeover(result.mode === "takeover");
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [sessionId]);

  const setMode = React.useCallback(
    async (next: "observe" | "takeover") => {
      try {
        const result = await api.browser.setMode(sessionId, next);
        setTakeover(result.mode === "takeover");
        // Saying it out loud matters: a click that does nothing in observe mode looks
        // like a broken panel, and the reason is not visible anywhere else.
        if (next === "takeover") toast.success("你正在控制浏览器", { description: "agent 的浏览器操作已暂停。" });
        else toast.info("已切回观察模式", { description: "点击与键盘不再发送到页面。" });
      } catch (error) {
        toast.error("切换失败", { description: humanizeError(error) });
      }
    },
    [sessionId],
  );
  const [error, setError] = React.useState<string | null>(null);
  const [meta, setMeta] = React.useState<FrameMeta | null>(null);
  const [url, setUrl] = React.useState("https://example.com");
  const [generation, setGeneration] = React.useState(0);
  // FR-11.7: a viewer is bound to one page. Switching tabs reconnects; opening and
  // closing pages goes through the API so the agent and the viewer see the same set.
  const [tabId, setTabId] = React.useState<string | null>(null);
  const tabs = useQuery({
    queryKey: ["browser-tabs", sessionId],
    queryFn: () => api.sessions.tabs(sessionId),
    enabled: browserEnabled,
    refetchInterval: 5_000,
  });
  React.useEffect(() => {
    if (tabId || !tabs.data) return;
    setTabId(tabs.data.default_tab || tabs.data.tabs[0]?.id || null);
  }, [tabs.data, tabId]);

  React.useEffect(() => {
    // The flag is already known: opening a socket the server will refuse only
    // produces a 403 in the console and a spinner that means nothing.
    if (!browserEnabled) return;
    let disposed = false;
    // One `tab` parameter, and it is the page. It used to be `tab=view&tab=<id>`,
    // which JSON-decoded to "view" on the server and pointed the screencast at a
    // target that does not exist — the canvas stayed blank while the badge said
    // 已连接.
    const query = tabId ? `?tab=${encodeURIComponent(tabId)}` : "";
    const socket = new WebSocket(`${wsBase()}/v1/sessions/${sessionId}/browser${query}`);
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
            setError(humanizeError(message.message, "浏览器不可用"));
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
  }, [sessionId, generation, browserEnabled, tabId]);

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
    const target = navigationTarget(url);
    if (!target) return;
    // Show what was actually sent: a bare host becoming https://… is a fact the
    // address bar should not hide.
    setUrl(target);
    // The old page stays on the canvas until the new one paints; clearing the
    // flag here is what keeps the loading hint honest about which page it is for.
    setPainted(false);
    send({ type: "navigate", url: target });
    // A page that refuses to load sends no frame at all, and an overlay that
    // never clears is indistinguishable from a broken panel. Give up explaining
    // after this long and let the browser's own error page show through.
    window.setTimeout(() => setPainted(true), 15_000);
  };

  // Switching pages must not leave the previous page's pixels on the canvas: a
  // blank tab paints no frame at all, so without this the panel showed the old
  // site while claiming to be on about:blank.
  const clearSurface = React.useCallback(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    canvas.getContext("2d")?.clearRect(0, 0, canvas.width, canvas.height);
  }, []);

  // Capture writes the page into the session's control-plane artifacts, not the
  // checkout: previewing it must not turn the user's repository dirty (FR-11.8).
  const [capturing, setCapturing] = React.useState(false);
  const capture = async () => {
    if (capturing) return;
    setCapturing(true);
    try {
      const result = await api.sessions.screenshot(sessionId, tabId ?? undefined);
      toast.success("已保存到会话附件", { description: result.path });
      onCaptured?.(result.path);
    } catch (error) {
      toast.error("截图失败", {
        description: humanizeError(error),
      });
    } finally {
      setCapturing(false);
    }
  };

  const queryClient = useQueryClient();
  const openTabMutation = useMutation({
    // 新标签页 is a blank page, not a copy of the current one: sending `url` here
    // made the plus button open the same site twice and left the blank-page
    // capture refusal unreachable from the UI.
    mutationFn: () => api.sessions.openTab(sessionId),
    onSuccess: (data) => {
      setTabId(data.tab.id);
      // The blank page is what is in front now; the address bar must say so. A
      // blank page is also already "painted": there is nothing to wait for, and
      // a spinner over it would suggest a load that is never going to happen.
      setUrl(data.tab.url || "about:blank");
      setPainted(true);
      clearSurface();
      void queryClient.invalidateQueries({ queryKey: ["browser-tabs", sessionId] });
    },
    onError: (error) =>
      toast.error("打开标签页失败", {
        description: humanizeError(error),
      }),
  });
  const closeTab = async (id: string) => {
    try {
      await api.sessions.closeTab(sessionId, id);
      if (id === tabId) setTabId(null);
      await queryClient.invalidateQueries({ queryKey: ["browser-tabs", sessionId] });
    } catch (error) {
      toast.error("关闭标签页失败", {
        description: humanizeError(error),
      });
    }
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
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-1 py-1" data-testid="browser-tabs">
        {(tabs.data?.tabs ?? []).map((tab) => (
          <div key={tab.id} className="flex min-w-0 items-center">
            <button
              type="button"
              data-testid="browser-tab"
              data-tab={tab.id}
              data-active={tab.id === tabId ? "1" : "0"}
              title={tab.url}
              onClick={() => {
                setTabId(tab.id);
                // The address bar follows the page you switched to; otherwise it
                // keeps showing the previous tab's URL next to a different page.
                setUrl(tab.url || "about:blank");
                // Drop the previous page's pixels before the new page paints.
                clearSurface();
              }}
              className={cn(
                "max-w-40 truncate rounded-t px-2 py-1 text-[11px]",
                tab.id === tabId
                  ? "bg-muted text-foreground"
                  : "text-muted-foreground hover:bg-muted/60",
              )}
            >
              {tab.title || tab.url || "新标签页"}
            </button>
            {(tabs.data?.tabs ?? []).length > 1 ? (
              <button
                type="button"
                aria-label="关闭标签页"
                data-testid="browser-tab-close"
                data-tab={tab.id}
                className="px-1 text-[11px] text-muted-foreground hover:text-foreground"
                onClick={() => void closeTab(tab.id)}
              >
                ×
              </button>
            ) : null}
          </div>
        ))}
        <Button
          size="icon-xs"
          variant="ghost"
          aria-label="新标签页"
          data-testid="browser-tab-add"
          onClick={() => void openTabMutation.mutate()}
        >
          <PlusIcon />
        </Button>
      </div>

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
        <Button
          size="icon-xs"
          variant="ghost"
          aria-label="截图保存到工作区"
          title="截图保存到工作区"
          data-testid="browser-screenshot"
          disabled={!live || capturing}
          onClick={() => void capture()}
        >
          {capturing ? <Spinner /> : <CameraIcon />}
        </Button>
        <Button
          size="xs"
          variant={takeover ? "default" : "outline"}
          data-testid="browser-mode"
          aria-pressed={takeover}
          title={
            takeover
              ? "你正在控制页面；agent 的浏览器操作会被打断"
              : "只观察：点击与键盘不会发到页面"
          }
          className="text-[10px]"
          onClick={() => {
            if (takeover) {
              void setMode("observe");
              return;
            }
            // Taking over interrupts the agent mid-run, so it asks first and says what it
            // will interrupt. A switch that silently stops someone else's work is a
            // surprise, not a feature.
            toast.warning("接管浏览器？", {
              description: "agent 当前的运行会被中断，直到你切回观察模式。",
              action: { label: "接管", onClick: () => void setMode("takeover") },
            });
          }}
        >
          {takeover ? "控制中" : "观察"}
        </Button>
        <Badge
          variant={live ? "secondary" : "outline"}
          className="text-[10px]"
          title={live && !painted ? "已经连上浏览器，但页面还没有画出第一帧" : undefined}
        >
          {!live
            ? status === "error"
              ? "不可用"
              : "连接中"
            : painted
              ? "已连接"
              : "页面未加载"}
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
