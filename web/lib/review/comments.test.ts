import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  DRAFT_STORAGE_KEY,
  clearSessionDrafts,
  draftsAt,
  draftsForPath,
  forgetDraftsCache,
  listDrafts,
  newDraftId,
  removeDraft,
  saveDraft,
  subscribeDrafts,
  type ReviewDraft,
} from "@/lib/review/comments";

function draft(overrides: Partial<ReviewDraft> = {}): ReviewDraft {
  return {
    id: newDraftId(),
    path: "src/cache.ts",
    line: 12,
    endLine: 12,
    side: "new",
    hunk: "@@ -10,3 +10,4 @@",
    quote: ["  return cache.get(key);"],
    body: "这里没有处理未命中。",
    origin: "diff",
    createdAt: "2025-01-01T00:00:00.000Z",
    ...overrides,
  };
}

beforeEach(() => {
  window.localStorage.clear();
  forgetDraftsCache();
});

describe("review comment drafts", () => {
  it("persists a draft and reads it back after the cache is dropped (a reload)", () => {
    const saved = draft();
    saveDraft("sess-1", saved);

    // A reload: new module state, same localStorage.
    forgetDraftsCache();
    const rows = listDrafts("sess-1");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      path: "src/cache.ts",
      line: 12,
      body: "这里没有处理未命中。",
      origin: "diff",
    });
    // And it really is in storage, not just in memory.
    expect(window.localStorage.getItem(DRAFT_STORAGE_KEY)).toContain("src/cache.ts");
  });

  it("keeps sessions apart", () => {
    saveDraft("sess-1", draft({ line: 1 }));
    saveDraft("sess-2", draft({ line: 2, path: "src/other.ts" }));
    expect(listDrafts("sess-1").map((row) => row.path)).toEqual(["src/cache.ts"]);
    expect(listDrafts("sess-2").map((row) => row.path)).toEqual(["src/other.ts"]);
  });

  it("updates a draft in place instead of appending a second copy", () => {
    const first = draft();
    saveDraft("sess-1", first);
    saveDraft("sess-1", { ...first, body: "改主意了。" });
    const rows = listDrafts("sess-1");
    expect(rows).toHaveLength(1);
    expect(rows[0].body).toBe("改主意了。");
  });

  it("deletes a draft when its text is cleared, and on removeDraft", () => {
    const first = draft();
    saveDraft("sess-1", first);
    saveDraft("sess-1", { ...first, body: "   " });
    expect(listDrafts("sess-1")).toHaveLength(0);

    const second = draft({ line: 20 });
    saveDraft("sess-1", second);
    removeDraft("sess-1", second.id);
    expect(listDrafts("sess-1")).toHaveLength(0);
  });

  it("clears every draft of a session", () => {
    saveDraft("sess-1", draft({ line: 1 }));
    saveDraft("sess-1", draft({ line: 2 }));
    saveDraft("sess-2", draft({ line: 3 }));
    clearSessionDrafts("sess-1");
    expect(listDrafts("sess-1")).toHaveLength(0);
    expect(listDrafts("sess-2")).toHaveLength(1);
  });

  it("filters by path and by line so a marker only shows where it belongs", () => {
    const a = draft({ line: 12, path: "a.ts" });
    const b = draft({ line: 30, path: "a.ts" });
    saveDraft("sess-1", a);
    saveDraft("sess-1", b);
    const rows = listDrafts("sess-1");
    expect(draftsForPath(rows, "a.ts")).toHaveLength(2);
    expect(draftsForPath(rows, "b.ts")).toHaveLength(0);
    expect(draftsAt(rows, "a.ts", 12).map((row) => row.id)).toEqual([a.id]);
    expect(draftsAt(rows, "a.ts", 31)).toHaveLength(0);
  });

  it("survives a corrupt storage value instead of throwing on mount", () => {
    window.localStorage.setItem(DRAFT_STORAGE_KEY, "{not json");
    forgetDraftsCache();
    expect(listDrafts("sess-1")).toEqual([]);
    // And it can still save afterwards.
    saveDraft("sess-1", draft());
    expect(listDrafts("sess-1")).toHaveLength(1);
  });

  it("drops rows that are not drafts while keeping the ones that are", () => {
    window.localStorage.setItem(
      DRAFT_STORAGE_KEY,
      JSON.stringify({
        "sess-1": [
          { path: "ok.ts", line: 3, body: "fine" },
          { path: "", line: 3, body: "no path" },
          { path: "bad.ts", line: 0, body: "no line" },
          { path: "bad2.ts", line: 4 },
          "nonsense",
        ],
        "sess-2": "not an array",
      }),
    );
    forgetDraftsCache();
    expect(listDrafts("sess-1").map((row) => row.path)).toEqual(["ok.ts"]);
    expect(listDrafts("sess-2")).toEqual([]);
  });

  it("notifies subscribers on every change", () => {
    const listener = vi.fn();
    const off = subscribeDrafts(listener);
    saveDraft("sess-1", draft());
    removeDraft("sess-1", listDrafts("sess-1")[0].id);
    clearSessionDrafts("sess-1");
    off();
    saveDraft("sess-1", draft());
    expect(listener).toHaveBeenCalledTimes(3);
  });
});
