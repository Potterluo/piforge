"use client";

import * as React from "react";

/**
 * Tracks whether the app is in dark mode.
 *
 * `globals.css` uses the class strategy (`@custom-variant dark (&:is(.dark *))`)
 * so a MutationObserver on `<html>` is the cheapest reliable source: it works
 * with `next-themes` without importing it, and it stays correct if the shell
 * flips the class from anywhere.
 */
export function useIsDark(): boolean {
  const [isDark, setIsDark] = React.useState(false);

  React.useEffect(() => {
    const root = document.documentElement;
    const read = () => setIsDark(root.classList.contains("dark"));
    read();

    const observer = new MutationObserver(read);
    observer.observe(root, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, []);

  return isDark;
}

/**
 * Reads a CSS custom property and resolves it to a concrete `rgb()`/`oklch()`
 * string the canvas-based renderers (xterm) can consume.
 *
 * `getComputedStyle` on the document element returns the raw token value, which
 * may be an `oklch()` expression. Painting a probe element with it makes the
 * browser do the conversion, so the terminal follows the theme without any
 * hard-coded colours.
 */
export function resolveTokenColor(token: string, fallback: string): string {
  if (typeof document === "undefined") return fallback;
  const probe = document.createElement("span");
  probe.style.color = `var(${token})`;
  probe.style.display = "none";
  document.body.appendChild(probe);
  const resolved = getComputedStyle(probe).color;
  probe.remove();
  return resolved || fallback;
}
