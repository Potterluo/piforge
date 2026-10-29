"use client";

import * as React from "react";

/**
 * Coalesces a burst of calls into one, `delay` ms after the last of them.
 *
 * The workspace panels use this to turn many "something may have changed"
 * signals — a tool call ending, the terminal printing a prompt, the window
 * regaining focus — into one refetch instead of one request per keystroke of
 * the shell. The pending timer is cleared on unmount, so a panel that goes away
 * never fetches on behalf of a page that is no longer showing.
 */
export function useDebouncedCallback(callback: () => void, delay = 400): () => void {
  const latest = React.useRef(callback);
  const timer = React.useRef<number | undefined>(undefined);

  // Keep the callback current without making it a dependency of the returned
  // function: callers pass an inline arrow, and a new identity per render would
  // re-create every effect that schedules a refresh.
  React.useEffect(() => {
    latest.current = callback;
  });

  React.useEffect(() => {
    return () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
    };
  }, []);

  return React.useCallback(() => {
    if (timer.current !== undefined) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      timer.current = undefined;
      latest.current();
    }, delay);
  }, [delay]);
}
