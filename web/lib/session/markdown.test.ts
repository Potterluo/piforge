import { describe, expect, it } from "vitest";

import { splitMarkdownBlocks } from "@/lib/session/markdown";

describe("splitMarkdownBlocks", () => {
  it("returns one prose block when there is no fence", () => {
    expect(splitMarkdownBlocks("第一行\n第二行")).toEqual([
      { kind: "text", text: "第一行\n第二行" },
    ]);
  });

  it("returns nothing for empty input", () => {
    expect(splitMarkdownBlocks("")).toEqual([]);
  });

  it("keeps the language hint of a fence", () => {
    expect(splitMarkdownBlocks("```go\nfunc main() {}\n```")).toEqual([
      { kind: "code", lang: "go", code: "func main() {}", closed: true },
    ]);
  });

  it("treats a fence without a language as plain code", () => {
    expect(splitMarkdownBlocks("```\nplain\n```")).toEqual([
      { kind: "code", lang: "", code: "plain", closed: true },
    ]);
  });

  it("marks an unterminated fence as open and keeps the rest as code", () => {
    expect(splitMarkdownBlocks("说明\n```sh\necho hi\necho again")).toEqual([
      { kind: "text", text: "说明" },
      { kind: "code", lang: "sh", code: "echo hi\necho again", closed: false },
    ]);
  });

  it("splits consecutive fences without an empty prose block between them", () => {
    expect(splitMarkdownBlocks("```js\nconst a = 1;\n```\n```js\nconst b = 2;\n```")).toEqual([
      { kind: "code", lang: "js", code: "const a = 1;", closed: true },
      { kind: "code", lang: "js", code: "const b = 2;", closed: true },
    ]);
  });

  it("keeps prose before and after a fence", () => {
    expect(splitMarkdownBlocks("before\n```ts\nconst x = 1;\n```\nafter")).toEqual([
      { kind: "text", text: "before" },
      { kind: "code", lang: "ts", code: "const x = 1;", closed: true },
      { kind: "text", text: "after" },
    ]);
  });

  it("does not close a fence on a line that has more than backticks", () => {
    expect(splitMarkdownBlocks("```md\n```js\nnot a close\n```")).toEqual([
      { kind: "code", lang: "md", code: "```js\nnot a close", closed: true },
    ]);
  });

  it("closes a fence that only has trailing whitespace", () => {
    expect(splitMarkdownBlocks("```\ncode\n```   ")).toEqual([
      { kind: "code", lang: "", code: "code", closed: true },
    ]);
  });

  it("supports an empty, immediately closed fence", () => {
    expect(splitMarkdownBlocks("```\n```")).toEqual([
      { kind: "code", lang: "", code: "", closed: true },
    ]);
  });

  it("accepts an indented opening fence", () => {
    expect(splitMarkdownBlocks("   ```py\nprint(1)\n   ```")).toEqual([
      { kind: "code", lang: "py", code: "print(1)", closed: true },
    ]);
  });

  it("drops whitespace-only prose between fences", () => {
    const blocks = splitMarkdownBlocks("```\na\n```\n\n\n```\nb\n```");
    expect(blocks).toHaveLength(2);
    expect(blocks.every((block) => block.kind === "code")).toBe(true);
  });

  it("keeps an unclosed fence that starts with no language and never closes", () => {
    expect(splitMarkdownBlocks("```\nstill streaming")).toEqual([
      { kind: "code", lang: "", code: "still streaming", closed: false },
    ]);
  });
});
