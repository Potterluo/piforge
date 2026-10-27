/**
 * Pure helpers for rendering unified patches.
 *
 * `@git-diff-view` does not accept a patch string: it wants `hunks`, one entry
 * per `@@ ... @@` block. These helpers do that split (and a couple of cheap
 * statistics) so the React wrapper stays presentational.
 */

/**
 * Splits a unified patch into one string per hunk.
 *
 * Everything before the first `@@` (the `diff --git` / `---` / `+++` header) is
 * dropped, because the library renders its own file headers. A patch with no
 * hunk at all — a binary change, or a mode-only change — yields an empty array
 * so callers can fall back to a plain text view instead of a blank diff.
 */
export function splitHunks(patch: string): string[] {
  if (!patch) return [];

  const hunks: string[] = [];
  let current: string[] | null = null;

  for (const line of patch.split("\n")) {
    if (line.startsWith("@@")) {
      if (current) hunks.push(current.join("\n"));
      current = [line];
    } else if (current) {
      current.push(line);
    }
  }

  if (current) {
    // A patch that ends with a newline produces one trailing empty line, which
    // would render as a phantom line in the diff.
    if (current[current.length - 1] === "") current.pop();
    hunks.push(current.join("\n"));
  }

  return hunks;
}

/** Counts added and removed lines in a patch, ignoring the `+++`/`---` headers. */
export function countPatchLines(patch: string): { additions: number; deletions: number } {
  let additions = 0;
  let deletions = 0;

  for (const line of patch.split("\n")) {
    if (line.startsWith("+++") || line.startsWith("---")) continue;
    if (line.startsWith("+")) additions++;
    else if (line.startsWith("-")) deletions++;
  }

  return { additions, deletions };
}

/** True when the patch carries no hunk body, so there is nothing to render. */
export function isEmptyPatch(patch: string): boolean {
  return splitHunks(patch).length === 0;
}
