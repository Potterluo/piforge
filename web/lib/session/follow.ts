/**
 * Scrollback for the follow view (FR-9.2).
 *
 * The follow pane is a mirror of the agent's bash output, and a command can print
 * for hours. Keeping every line in the DOM-bound xterm buffer would grow the page
 * without bound, so the source text is capped to the newest lines. It is trimmed
 * in batches rather than on every line: the pane rebuilds its surface when the
 * source shrinks, and doing that per line would cost as much as never capping at
 * all.
 */

/** Lines the follow view keeps. Older output is dropped. */
export const FOLLOW_MAX_LINES = 2_000;

/** How far past the cap the buffer may grow before it is trimmed again. */
export const FOLLOW_TRIM_SLACK = 500;

/**
 * Returns `text` unchanged until it is more than `maxLines + slack` lines long,
 * then returns only its newest `maxLines` lines.
 *
 * A trailing, not-yet-terminated line is kept: the point is to bound memory,
 * not to hide the line that is being written right now.
 */
export function capFollowText(
  text: string,
  maxLines = FOLLOW_MAX_LINES,
  slack = FOLLOW_TRIM_SLACK,
): string {
  if (!text || maxLines <= 0) return text;
  const trimAbove = maxLines + Math.max(0, slack);
  // Not enough complete lines to be over the batch threshold: leave it alone.
  if (startOfLineFromEnd(text, trimAbove + 1) <= 0) return text;
  // Keeping `maxLines` complete lines means starting after the newline that
  // terminates the line before them, i.e. the (maxLines+1)-th newline from the
  // end. A trailing partial line is kept with them.
  const keepFrom = startOfLineFromEnd(text, maxLines + 1);
  return keepFrom > 0 ? text.slice(keepFrom) : text;
}

/**
 * Byte offset of the first character of the `lines`-th newline-terminated line
 * counted from the end, or -1 when the text has fewer lines than that.
 */
function startOfLineFromEnd(text: string, lines: number): number {
  let seen = 0;
  for (let index = text.length - 1; index >= 0; index -= 1) {
    if (text.charCodeAt(index) !== 10) continue;
    seen += 1;
    if (seen === lines) return index + 1;
  }
  return -1;
}
