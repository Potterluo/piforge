"use client";

import * as React from "react";

/**
 * Reports that `value` is not the value this component last rendered with, and
 * records the new one.
 *
 * This exists so a component can reset its own state when an input changes
 * without an effect whose body calls `setState`: the `react-hooks/recommended`
 * rules refuse that ("Calling setState synchronously within an effect can
 * trigger cascading renders"), and they are right — the reset would land one
 * frame after the render that already drew the old input's leftovers.
 *
 * The update happens during the render that sees the new value, which is the
 * pattern React documents as "adjusting state when a prop changes" (You Might
 * Not Need an Effect). The new value is recorded before this function returns,
 * so the immediate re-render it causes compares equal and does nothing — the
 * hook reports a change exactly once per change.
 *
 *     const pathChanged = useOnChange(path);
 *     if (pathChanged) {
 *       setEditing(false);      // the draft belonged to the previous file
 *       setDraft("");
 *     }
 */
export function useOnChange<T>(value: T): boolean {
  const [seen, setSeen] = React.useState(value);
  if (Object.is(seen, value)) return false;
  setSeen(value);
  return true;
}
