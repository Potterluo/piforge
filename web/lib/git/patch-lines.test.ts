import { describe, expect, it } from "vitest";

import { anchorForLine, pairRows, parsePatch } from "@/lib/git/patch-lines";

const patch = [
  "diff --git a/src/cache.ts b/src/cache.ts",
  "index 1111111..2222222 100644",
  "--- a/src/cache.ts",
  "+++ b/src/cache.ts",
  "@@ -10,5 +10,6 @@ export function get(key: string) {",
  "   const hit = cache[key];",
  "   if (!hit) {",
  "-    return undefined;",
  "+    return null;",
  "+  }",
  "   return hit;",
  "",
].join("\n");

describe("parsePatch", () => {
  it("gives every hunk line the old and new number the header implies", () => {
    const lines = parsePatch(patch);
    const body = lines.filter((line) => line.kind !== "meta" && line.kind !== "hunk");
    expect(body.map((line) => [line.kind, line.oldLine, line.newLine])).toEqual([
      ["ctx", 10, 10],
      ["ctx", 11, 11],
      ["del", 12, null],
      ["add", null, 12],
      ["add", null, 13],
      ["ctx", 13, 14],
    ]);
    // The metadata above the first hunk has no line numbers: it is not content.
    const meta = lines.filter((line) => line.kind === "meta");
    expect(meta.every((line) => line.oldLine === null && line.newLine === null)).toBe(true);
  });

  it("records which hunk each line came from, so a comment can quote it", () => {
    const lines = parsePatch(`${patch}\n@@ -40,2 +41,2 @@\n-a\n+b\n`);
    const added = lines.find((line) => line.kind === "add" && line.text === "b");
    expect(added?.hunkIndex).toBe(1);
    expect(added?.hunkHeader).toBe("@@ -40,2 +41,2 @@");
    expect(added?.newLine).toBe(41);
  });

  it("returns nothing for a patch with no hunk (binary, mode-only)", () => {
    const binary = [
      "diff --git a/logo.png b/logo.png",
      "index 1111111..2222222 100644",
      "Binary files a/logo.png and b/logo.png differ",
    ].join("\n");
    const lines = parsePatch(binary);
    expect(lines.every((line) => line.kind === "meta")).toBe(true);
  });

  it("keeps an empty line inside a hunk addressable", () => {
    const lines = parsePatch("@@ -1,2 +1,2 @@\n \n-x\n+y\n");
    const blank = lines.find((line) => line.kind === "ctx");
    expect(blank?.newLine).toBe(1);
  });
});

describe("anchorForLine", () => {
  it("anchors an added line on the new side with path:line and the code", () => {
    const lines = parsePatch(patch);
    const added = lines.find((line) => line.kind === "add" && line.text === "    return null;")!;
    expect(anchorForLine("src/cache.ts", added)).toMatchObject({
      path: "src/cache.ts",
      line: 12,
      endLine: 12,
      side: "new",
      quote: ["    return null;"],
      hunk: "@@ -10,5 +10,6 @@ export function get(key: string) {",
    });
  });

  it("anchors a removed line on the old side", () => {
    const lines = parsePatch(patch);
    const removed = lines.find((line) => line.kind === "del")!;
    expect(anchorForLine("src/cache.ts", removed)).toMatchObject({
      line: 12,
      side: "old",
      quote: ["    return undefined;"],
    });
  });

  it("has no anchor for metadata or a hunk header", () => {
    const lines = parsePatch(patch);
    for (const line of lines) {
      if (line.kind === "meta" || line.kind === "hunk" || line.kind === "nonewline") {
        expect(anchorForLine("src/cache.ts", line)).toBeNull();
      }
    }
  });
});

describe("pairRows", () => {
  it("pairs each removed line with an added one and keeps context on both sides", () => {
    const rows = pairRows(parsePatch(patch));
    // headers/meta are carried as full-width rows
    const pairs = rows.filter((row) => row.left?.kind === "del" || row.right?.kind === "add");
    expect(pairs).toHaveLength(2);
    expect(pairs[0].left?.text).toBe("    return undefined;");
    expect(pairs[0].right?.text).toBe("    return null;");
    // The second addition has no deletion to pair with: the left cell is empty.
    expect(pairs[1].left).toBeNull();
    expect(pairs[1].right?.text).toBe("  }");
    const context = rows.filter((row) => row.left?.kind === "ctx");
    expect(context.every((row) => row.left === row.right)).toBe(true);
  });
});
