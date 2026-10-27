import { describe, expect, it } from "vitest";

import { countPatchLines, isEmptyPatch, splitHunks } from "@/lib/git/patch";

const PATCH = [
  "diff --git a/app/main.go b/app/main.go",
  "index 1b2c3d4..5e6f7a8 100644",
  "--- a/app/main.go",
  "+++ b/app/main.go",
  "@@ -1,4 +1,5 @@",
  " package main",
  "-",
  "+import \"fmt\"",
  "+",
  " func main() {",
  "@@ -20,3 +21,3 @@ func helper() {",
  "-	return nil",
  "+	return fmt.Errorf(\"boom\")",
  " }",
  "",
].join("\n");

describe("splitHunks", () => {
  it("returns one entry per hunk and drops the file header", () => {
    const hunks = splitHunks(PATCH);

    expect(hunks).toHaveLength(2);
    expect(hunks[0].split("\n")[0]).toBe("@@ -1,4 +1,5 @@");
    expect(hunks[1].split("\n")[0]).toBe("@@ -20,3 +21,3 @@ func helper() {");
    // The trailing newline of the patch must not become a phantom line.
    expect(hunks[1].endsWith("}")).toBe(true);
  });

  it("returns nothing for a binary or mode-only change", () => {
    const binary = [
      "diff --git a/logo.png b/logo.png",
      "index 1111111..2222222 100644",
      "Binary files a/logo.png and b/logo.png differ",
    ].join("\n");

    expect(splitHunks(binary)).toEqual([]);
    expect(isEmptyPatch(binary)).toBe(true);
    expect(splitHunks("")).toEqual([]);
  });
});

describe("countPatchLines", () => {
  it("counts additions and deletions but not headers", () => {
    expect(countPatchLines(PATCH)).toEqual({ additions: 3, deletions: 2 });
  });

  it("returns zeros for an empty patch", () => {
    expect(countPatchLines("")).toEqual({ additions: 0, deletions: 0 });
  });
});
