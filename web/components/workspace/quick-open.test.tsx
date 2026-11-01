import * as React from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Quick open (FR-6.4 / FR-6.6): a search answers the *current* query.
 *
 * The debounce used to own the cancellation flag: `let cancelled = false` was
 * declared *inside* the `setTimeout` callback, so the effect's cleanup — which
 * runs when the query changes — had nothing to flip. A request that resolved
 * after the user kept typing (or after the query was cleared) still called
 * `setResults`, and the palette listed hits for a query nobody was looking at.
 *
 * These tests hold the debounce (they drive fake timers) and answer the stale
 * request on purpose, so the race is deterministic instead of occasional.
 */

const mocks = vi.hoisted(() => ({ search: vi.fn() }));

vi.mock("@/lib/api/client", () => {
  const api = { files: { search: mocks.search } };
  return { api, default: api };
});

import { QuickOpen } from "@/components/workspace/quick-open";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (cause: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((settle, fail) => {
    resolve = settle;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function openPalette(): HTMLElement {
  render(<QuickOpen sessionId="ses_1" onOpenFile={vi.fn()} />);
  // ⌘P / Ctrl+P is the input the palette really listens to.
  act(() => {
    fireEvent.keyDown(window, { key: "p", ctrlKey: true });
  });
  return screen.getByTestId("quick-open-input");
}

beforeEach(() => {
  vi.useFakeTimers();
  mocks.search.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("quick open search", () => {
  it("does not write the answer of a query the user has already moved past", async () => {
    const stale = deferred<{ results: { path: string }[] }>();
    const fresh = deferred<{ results: { path: string }[] }>();
    mocks.search.mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);

    const input = openPalette();

    // Typing is debounced: nothing is asked for until 180 ms pass.
    act(() => {
      fireEvent.change(input, { target: { value: "alp" } });
    });
    expect(mocks.search).not.toHaveBeenCalled();
    act(() => {
      vi.advanceTimersByTime(180);
    });
    expect(mocks.search).toHaveBeenCalledTimes(1);
    expect(mocks.search.mock.calls[0].slice(0, 2)).toEqual(["ses_1", "alp"]);

    // The user keeps typing while "alp" is still in flight.
    act(() => {
      fireEvent.change(input, { target: { value: "alpha" } });
    });

    // "alp" answers now, after its query is gone.
    await act(async () => {
      stale.resolve({ results: [{ path: "stale-alp.ts" }] });
      await Promise.resolve();
    });

    // The one thing this test is about: no write for the abandoned query.
    expect(screen.queryAllByTestId("quick-open-result")).toHaveLength(0);
    expect(screen.queryByText("stale-alp.ts")).toBeNull();

    // And the component still works: the answer for the live query draws.
    act(() => {
      vi.advanceTimersByTime(180);
    });
    expect(mocks.search).toHaveBeenCalledTimes(2);
    await act(async () => {
      fresh.resolve({ results: [{ path: "alpha.ts" }] });
      await Promise.resolve();
    });

    const rows = screen.getAllByTestId("quick-open-result");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveAttribute("data-path", "alpha.ts");
  });

  it("does not report a failure for a search the user has already cleared", async () => {
    // The error branch is drawn before the empty-query branch, so a late
    // rejection was *visible*: clearing the box left the failure on screen with
    // nothing to explain it.
    const stale = deferred<{ results: { path: string }[] }>();
    mocks.search.mockReturnValueOnce(stale.promise);

    const input = openPalette();
    act(() => {
      fireEvent.change(input, { target: { value: "notes" } });
    });
    act(() => {
      vi.advanceTimersByTime(180);
    });
    expect(mocks.search).toHaveBeenCalledTimes(1);

    // The box is emptied while the search is in flight.
    act(() => {
      fireEvent.change(input, { target: { value: "" } });
    });

    await act(async () => {
      stale.reject(new Error("rg exited 2"));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(screen.queryByText(/搜索失败/)).toBeNull();
    expect(screen.queryAllByTestId("quick-open-result")).toHaveLength(0);
  });
});
