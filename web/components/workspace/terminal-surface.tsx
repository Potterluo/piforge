"use client";

import * as React from "react";
import { Terminal, type ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import "@xterm/xterm/css/xterm.css";

import { resolveTokenColor, useIsDark } from "@/components/workspace/use-is-dark";

/**
 * The raw xterm surface.
 *
 * Loaded through `next/dynamic` + `ssr: false` from `terminal-pane.tsx`: xterm
 * touches `document` the moment it is constructed, and it is one of the
 * heaviest modules in the app, so it must not be part of the shell's initial
 * bundle either.
 *
 * The handle is handed to the parent via `onReady` instead of a ref, because a
 * ref does not reliably cross the `next/dynamic` boundary.
 */
export interface TerminalSurfaceHandle {
  write: (chunk: string) => void;
  clear: () => void;
  fit: () => void;
  focus: () => void;
  search: (term: string, forward?: boolean) => boolean;
  reset: () => void;
}

export interface TerminalSurfaceProps {
  readOnly?: boolean;
  fontSize?: number;
  /** User keystrokes, for the interactive pane once a PTY exists. */
  onData?: (chunk: string) => void;
  onReady?: (handle: TerminalSurfaceHandle) => void;
  className?: string;
}

/** Resolves the design tokens once, so the canvas renderer gets real colours. */
function themeFromTokens(): ITheme {
  return {
    background: resolveTokenColor("--background", "#ffffff"),
    foreground: resolveTokenColor("--foreground", "#171717"),
    cursor: resolveTokenColor("--primary", "#171717"),
    cursorAccent: resolveTokenColor("--background", "#ffffff"),
    selectionBackground: resolveTokenColor("--muted", "#f5f5f5"),
  };
}

export function TerminalSurface({
  readOnly = false,
  fontSize = 12,
  onData,
  onReady,
  className,
}: TerminalSurfaceProps) {
  const hostRef = React.useRef<HTMLDivElement | null>(null);
  const termRef = React.useRef<Terminal | null>(null);
  const fitRef = React.useRef<FitAddon | null>(null);
  const searchRef = React.useRef<SearchAddon | null>(null);

  const onDataRef = React.useRef(onData);
  const onReadyRef = React.useRef(onReady);
  React.useEffect(() => {
    onDataRef.current = onData;
    onReadyRef.current = onReady;
  });

  React.useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const terminal = new Terminal({
      convertEol: true,
      disableStdin: readOnly,
      cursorBlink: !readOnly,
      fontSize,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      scrollback: 5000,
      theme: themeFromTokens(),
    });

    const fit = new FitAddon();
    const search = new SearchAddon();
    terminal.loadAddon(fit);
    terminal.loadAddon(search);
    terminal.open(host);

    termRef.current = terminal;
    fitRef.current = fit;
    searchRef.current = search;

    const dataDisposable = terminal.onData((chunk) => onDataRef.current?.(chunk));

    const safeFit = () => {
      // A hidden tab has a zero-sized container; xterm throws on that.
      if (host.clientWidth < 2 || host.clientHeight < 2) return;
      try {
        fit.fit();
      } catch {
        /* the container changed size mid-fit; the next resize will correct it */
      }
    };

    const observer =
      typeof ResizeObserver === "undefined" ? null : new ResizeObserver(() => safeFit());
    observer?.observe(host);
    safeFit();

    onReadyRef.current?.({
      write: (chunk) => terminal.write(chunk),
      clear: () => terminal.clear(),
      fit: safeFit,
      focus: () => terminal.focus(),
      search: (term, forward = true) =>
        forward
          ? search.findNext(term, { incremental: true })
          : search.findPrevious(term, { incremental: true }),
      reset: () => terminal.reset(),
    });

    return () => {
      observer?.disconnect();
      dataDisposable.dispose();
      terminal.dispose();
      termRef.current = null;
      fitRef.current = null;
      searchRef.current = null;
    };
    // Mount-only on purpose: `readOnly` and `fontSize` are applied below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    const terminal = termRef.current;
    if (!terminal) return;
    terminal.options.fontSize = fontSize;
    fitRef.current?.fit();
  }, [fontSize]);

  // Re-resolve the design tokens when the theme class flips: the canvas renderer
  // holds concrete colours, so a class change alone would not repaint it.
  const isDark = useIsDark();
  React.useEffect(() => {
    const terminal = termRef.current;
    if (!terminal) return;
    terminal.options.theme = themeFromTokens();
  }, [isDark]);

  return <div ref={hostRef} className={className} data-slot="xterm-surface" />;
}
