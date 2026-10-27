"use client";

import * as React from "react";
import { Maximize2Icon, ZoomInIcon, ZoomOutIcon } from "lucide-react";

import { Button } from "@/components/ui/button";

const MIN_SCALE = 0.1;
const MAX_SCALE = 8;

/**
 * Image viewer: wheel to zoom, drag to pan, fit-to-width by default.
 *
 * The wheel handler is attached natively with `{ passive: false }` — React's
 * root-level wheel listener is passive, so `preventDefault()` from `onWheel`
 * would be ignored and the surrounding panel would scroll while zooming.
 */
export function ImagePreview({ src, name }: { src: string; name: string }) {
  const containerRef = React.useRef<HTMLDivElement | null>(null);
  const dragRef = React.useRef<{ x: number; y: number; ox: number; oy: number } | null>(null);
  const [scale, setScale] = React.useState(1);
  const [offset, setOffset] = React.useState({ x: 0, y: 0 });
  const [dragging, setDragging] = React.useState(false);

  const zoomBy = React.useCallback((factor: number) => {
    setScale((current) => Math.min(MAX_SCALE, Math.max(MIN_SCALE, current * factor)));
  }, []);

  const reset = React.useCallback(() => {
    setScale(1);
    setOffset({ x: 0, y: 0 });
  }, []);

  React.useEffect(() => {
    const element = containerRef.current;
    if (!element) return;

    const onWheel = (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey && Math.abs(event.deltaY) < 1) return;
      event.preventDefault();
      zoomBy(event.deltaY < 0 ? 1.12 : 1 / 1.12);
    };

    element.addEventListener("wheel", onWheel, { passive: false });
    return () => element.removeEventListener("wheel", onWheel);
  }, [zoomBy]);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b px-2 py-1">
        <Button variant="ghost" size="icon-xs" aria-label="缩小" onClick={() => zoomBy(1 / 1.2)}>
          <ZoomOutIcon />
        </Button>
        <span className="w-10 text-center font-mono text-[10px] text-muted-foreground">
          {Math.round(scale * 100)}%
        </span>
        <Button variant="ghost" size="icon-xs" aria-label="放大" onClick={() => zoomBy(1.2)}>
          <ZoomInIcon />
        </Button>
        <Button variant="ghost" size="icon-xs" aria-label="适应宽度" onClick={reset}>
          <Maximize2Icon />
        </Button>
        <span className="ms-auto truncate text-[10px] text-muted-foreground">{name}</span>
      </div>

      <div
        ref={containerRef}
        className="relative min-h-0 min-w-0 flex-1 cursor-grab overflow-hidden bg-muted/20 active:cursor-grabbing"
        onMouseDown={(event) => {
          dragRef.current = {
            x: event.clientX,
            y: event.clientY,
            ox: offset.x,
            oy: offset.y,
          };
          setDragging(true);
        }}
        onMouseMove={(event) => {
          const drag = dragRef.current;
          if (!drag) return;
          setOffset({
            x: drag.ox + (event.clientX - drag.x),
            y: drag.oy + (event.clientY - drag.y),
          });
        }}
        onMouseUp={() => {
          dragRef.current = null;
          setDragging(false);
        }}
        onMouseLeave={() => {
          dragRef.current = null;
          setDragging(false);
        }}
      >
        {/* eslint-disable-next-line @next/next/no-img-element -- images come from the file API as data URLs */}
        <img
          src={src}
          alt={name}
          draggable={false}
          className="absolute top-0 left-0 max-w-full origin-top-left select-none"
          style={{
            transform: `translate(${offset.x}px, ${offset.y}px) scale(${scale})`,
            transition: dragging ? "none" : "transform 80ms ease-out",
          }}
        />
      </div>
    </div>
  );
}
