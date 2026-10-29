/**
 * Where the left sidebar's open/closed choice is remembered.
 *
 * localStorage is the source of truth (it is what the user's browser keeps), the
 * shadcn provider mirrors the same choice into its `sidebar_state` cookie so a
 * server-rendered shell could restore it too. Both are written on every toggle.
 *
 * Reading it must never throw: Safari's private mode and a locked-down enterprise
 * profile both make `localStorage` access throw, and a sidebar that cannot be
 * loaded is worse than one that forgets it was collapsed.
 */
export const SIDEBAR_PREFERENCE_KEY = "piforge.sidebar.open";

/** `true` = expanded. Anything unreadable falls back to the default (expanded). */
export function readSidebarOpen(fallback = true): boolean {
  if (typeof window === "undefined") return fallback;
  try {
    const stored = window.localStorage.getItem(SIDEBAR_PREFERENCE_KEY);
    if (stored === "true") return true;
    if (stored === "false") return false;
  } catch {
    // Storage is unavailable; the sidebar still works, it just will not remember.
  }
  return fallback;
}

export function writeSidebarOpen(open: boolean): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(SIDEBAR_PREFERENCE_KEY, open ? "true" : "false");
  } catch {
    // See readSidebarOpen: remembering the choice is a convenience, not a feature
    // the layout depends on.
  }
}
