import { describe, expect, it } from "vitest";

import { capFollowText, FOLLOW_MAX_LINES, FOLLOW_TRIM_SLACK } from "@/lib/session/follow";

/** `n` newline-terminated lines, each named so the tail is checkable. */
function lines(n: number): string {
  let text = "";
  for (let index = 1; index <= n; index += 1) text += `line ${index}\n`;
  return text;
}

describe("the follow view's scrollback cap", () => {
  it("leaves a short transcript untouched, identity included", () => {
    const text = lines(5);
    expect(capFollowText(text, 100, 0)).toBe(text);
  });

  it("does not trim just past the cap: trimming is batched", () => {
    // Within maxLines + slack the surface is not rebuilt, so the buffer is left
    // alone. Rebuilding on every line would cost more than the bound it buys.
    const text = lines(FOLLOW_MAX_LINES + FOLLOW_TRIM_SLACK - 1);
    expect(capFollowText(text)).toBe(text);
  });

  it("keeps only the newest lines once the batch threshold is passed", () => {
    const text = lines(FOLLOW_MAX_LINES + FOLLOW_TRIM_SLACK + 10);
    const capped = capFollowText(text);
    expect(capped).not.toBe(text);
    expect(capped.endsWith("line 2510\n")).toBe(true);
    expect(capped).not.toContain("line 1\n");
    // Exactly the cap worth of complete lines survives.
    expect(capped.match(/^line /gm)).toHaveLength(FOLLOW_MAX_LINES);
  });

  it("keeps a trailing line that has not been terminated yet", () => {
    // The line being written is the most interesting one on screen.
    const text = `a\n${lines(10)}partial tail`;
    const capped = capFollowText(text, 2, 0);
    expect(capped).toBe("line 9\nline 10\npartial tail");
  });

  it("returns the text unchanged for a non-positive cap", () => {
    const text = lines(10);
    expect(capFollowText(text, 0, 0)).toBe(text);
  });
});
