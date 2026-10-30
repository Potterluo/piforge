/**
 * Patch lines, for a diff a human can point at.
 *
 * `@git-diff-view` never renders a row in this build (its `DiffUnifiedView`
 * throws and the error boundary falls back to the raw patch), so the diff the
 * user actually sees is the patch text. A comment needs an addressable line, and
 * a `CodeBlockContent` string has none. This module turns the patch into one
 * record per line, with the old and new line numbers the hunk header implies, so
 * the view can offer a comment button per line and the comment can carry
 * `path:line` instead of "somewhere in this file".
 *
 * Nothing here guesses: a line belongs to the old side, the new side, or both,
 * and the numbers come from the `@@` header.
 */

export type PatchLineKind = "meta" | "hunk" | "ctx" | "add" | "del" | "nonewline";

export interface PatchLine {
  /** Position in the patch, 0-based. Stable while the patch is unchanged. */
  index: number;
  kind: PatchLineKind;
  /** The line's content, without the `+`/`-`/` ` marker. */
  text: string;
  /** The raw patch line, marker included. */
  raw: string;
  /** Old-file line number, or null when the line only exists in the new file. */
  oldLine: number | null;
  /** New-file line number, or null when the line was removed. */
  newLine: number | null;
  /** Which `@@` block this line belongs to; -1 before the first hunk. */
  hunkIndex: number;
  /** The `@@ … @@` header of that block, for the comment's context. */
  hunkHeader: string;
}

const HUNK_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

/**
 * parsePatch splits a unified diff into addressable lines.
 *
 * A patch with no `@@` header (binary, mode-only, rename-only) yields only the
 * meta lines, and callers fall back to showing the raw text: there is nothing to
 * anchor a comment to in a file that has no lines in the diff.
 */
export function parsePatch(patch: string): PatchLine[] {
  const out: PatchLine[] = [];
  if (!patch) return out;
  let oldNo = 0;
  let newNo = 0;
  let hunkIndex = -1;
  let hunkHeader = "";
  const lines = patch.replace(/\r\n/g, "\n").split("\n");
  // A patch string almost always ends with a newline; that last empty element is
  // not a line of the file.
  if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();

  lines.forEach((raw, index) => {
    const hunk = HUNK_RE.exec(raw);
    if (hunk) {
      hunkIndex += 1;
      hunkHeader = raw;
      oldNo = Number(hunk[1]);
      newNo = Number(hunk[2]);
      out.push({
        index,
        kind: "hunk",
        text: raw,
        raw,
        oldLine: null,
        newLine: null,
        hunkIndex,
        hunkHeader,
      });
      return;
    }
    if (raw.startsWith("\\")) {
      out.push({
        index,
        kind: "nonewline",
        text: raw,
        raw,
        oldLine: null,
        newLine: null,
        hunkIndex,
        hunkHeader,
      });
      return;
    }
    // Between hunks there is file metadata (diff --git, index, ---, +++). It is
    // not part of a file's content, so it gets no line numbers.
    if (hunkIndex < 0 || raw.startsWith("diff --git") || raw.startsWith("index ") ||
        raw.startsWith("--- ") || raw.startsWith("+++ ")) {
      out.push({
        index,
        kind: "meta",
        text: raw,
        raw,
        oldLine: null,
        newLine: null,
        hunkIndex,
        hunkHeader,
      });
      return;
    }
    const marker = raw[0] ?? " ";
    const text = raw.slice(1);
    if (marker === "+") {
      out.push({
        index,
        kind: "add",
        text,
        raw,
        oldLine: null,
        newLine: newNo,
        hunkIndex,
        hunkHeader,
      });
      newNo += 1;
      return;
    }
    if (marker === "-") {
      out.push({
        index,
        kind: "del",
        text,
        raw,
        oldLine: oldNo,
        newLine: null,
        hunkIndex,
        hunkHeader,
      });
      oldNo += 1;
      return;
    }
    // Context (" ") and, defensively, an empty line inside a hunk (some patches
    // carry a bare newline where a space was stripped) both advance both sides.
    out.push({
      index,
      kind: "ctx",
      text,
      raw,
      oldLine: oldNo,
      newLine: newNo,
      hunkIndex,
      hunkHeader,
    });
    oldNo += 1;
    newNo += 1;
  });

  return out;
}

/** The anchor a comment on this line carries. */
export interface CommentAnchor {
  path: string;
  line: number;
  endLine: number;
  side: "new" | "old";
  hunk: string;
  quote: string[];
}

/**
 * anchorForLine turns one diff line into the path:line anchor a comment is
 * stored under and sent with.
 *
 * A removed line has no line number in the new file, so it is anchored on the
 * old side; the server says so in the message rather than pretending the line
 * still exists.
 */
export function anchorForLine(path: string, line: PatchLine): CommentAnchor | null {
  if (line.kind === "add" || line.kind === "ctx") {
    if (line.newLine === null) return null;
    return {
      path,
      line: line.newLine,
      endLine: line.newLine,
      side: "new",
      hunk: line.hunkHeader,
      quote: [line.text],
    };
  }
  if (line.kind === "del") {
    if (line.oldLine === null) return null;
    return {
      path,
      line: line.oldLine,
      endLine: line.oldLine,
      side: "old",
      hunk: line.hunkHeader,
      quote: [line.text],
    };
  }
  return null;
}

/**
 * pairRows groups a unified patch into side-by-side rows.
 *
 * Deletions and additions are paired in order inside a hunk, which is what a
 * split view shows when the counts differ: the unpaired tail keeps its own row
 * with an empty cell on the other side. There is no attempt to align by content
 * — a guess that lines "look similar" is exactly the kind of inference this
 * file avoids.
 */
export interface SplitRow {
  left: PatchLine | null;
  right: PatchLine | null;
}

export function pairRows(lines: PatchLine[]): SplitRow[] {
  const rows: SplitRow[] = [];
  let pendingDel: PatchLine[] = [];
  let pendingAdd: PatchLine[] = [];

  const flush = () => {
    const count = Math.max(pendingDel.length, pendingAdd.length);
    for (let i = 0; i < count; i += 1) {
      rows.push({ left: pendingDel[i] ?? null, right: pendingAdd[i] ?? null });
    }
    pendingDel = [];
    pendingAdd = [];
  };

  for (const line of lines) {
    if (line.kind === "del") {
      if (pendingAdd.length > 0) flush();
      pendingDel.push(line);
      continue;
    }
    if (line.kind === "add") {
      pendingAdd.push(line);
      continue;
    }
    flush();
    rows.push({ left: line, right: line });
  }
  flush();
  return rows;
}
