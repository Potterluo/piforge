"use client";

import * as React from "react";

/**
 * The smallest async-data hook the workspace panels need.
 *
 * The shell owns a react-query provider; the panels must not assume it, so this
 * keeps a plain effect. Cancellation is a plain `alive` flag rather than an
 * AbortSignal, because `lib/api/client.ts` does not thread a signal through
 * every workspace call — honest and small beats a second data library.
 *
 * `loading` is *derived* (state signature vs. current signature) instead of
 * being set at the top of the effect: writing state synchronously inside an
 * effect causes the cascading-render lint error and an extra frame of work.
 */
export interface AsyncResult<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  reload: () => void;
}

/** Human message for anything thrown by the API client. */
export function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  return "请求失败，请重试";
}

interface AsyncState<T> {
  signature: string;
  data: T | null;
  error: string | null;
}

/**
 * Runs `load` whenever `key` changes (or `reload()` is called).
 *
 * `load` is read through a ref so callers can pass an inline arrow function
 * without re-triggering the fetch on every render.
 */
export function useAsync<T>(
  key: string,
  load: () => Promise<T>,
  options: { enabled?: boolean } = {},
): AsyncResult<T> {
  const { enabled = true } = options;
  const loadRef = React.useRef(load);
  const [nonce, setNonce] = React.useState(0);
  const [state, setState] = React.useState<AsyncState<T>>({
    signature: "",
    data: null,
    error: null,
  });

  const signature = `${key}|${nonce}|${enabled ? "on" : "off"}`;
  const loading = enabled && state.signature !== signature;

  // Keep the latest loader without making it an effect dependency. Declared
  // before the fetch effect, so it is always current when that effect runs.
  React.useEffect(() => {
    loadRef.current = load;
  });

  React.useEffect(() => {
    if (!enabled) return;

    let alive = true;

    loadRef
      .current()
      .then((data) => {
        if (alive) setState({ signature, data, error: null });
      })
      .catch((error: unknown) => {
        if (alive) setState({ signature, data: null, error: errorMessage(error) });
      });

    return () => {
      alive = false;
    };
  }, [signature, enabled]);

  const reload = React.useCallback(() => setNonce((value) => value + 1), []);

  return { data: state.data, error: state.error, loading, reload };
}
