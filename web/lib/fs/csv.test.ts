import { describe, expect, it } from "vitest";

import { delimiterFor, parseDelimited } from "@/lib/fs/csv";

describe("parseDelimited", () => {
  it("splits a plain CSV into a header and rows", () => {
    const table = parseDelimited("a,b\n1,2\n3,4\n");

    expect(table.header).toEqual(["a", "b"]);
    expect(table.rows).toEqual([
      ["1", "2"],
      ["3", "4"],
    ]);
    expect(table.clipped).toBe(false);
    expect(table.totalRows).toBe(3);
  });

  it("keeps delimiters and newlines inside quoted fields", () => {
    const table = parseDelimited('name,note\n"a,b","line1\nline2"\n');

    expect(table.rows).toEqual([["a,b", "line1\nline2"]]);
  });

  it("unescapes doubled quotes", () => {
    const table = parseDelimited('a\n"say ""hi"""\n');

    expect(table.rows[0][0]).toBe('say "hi"');
  });

  it("clips rows and columns to the budget", () => {
    const text = ["h1,h2,h3", "1,1,1", "2,2,2", "3,3,3"].join("\n");
    const table = parseDelimited(text, ",", 2, 2);

    expect(table.header).toEqual(["h1", "h2"]);
    expect(table.rows).toHaveLength(2);
    expect(table.rows[0]).toEqual(["1", "1"]);
    expect(table.clipped).toBe(true);
  });
});

describe("delimiterFor", () => {
  it("uses tabs for .tsv and the tab-separated mime type", () => {
    expect(delimiterFor("data.tsv")).toBe("\t");
    expect(delimiterFor("data.csv", "text/tab-separated-values")).toBe("\t");
    expect(delimiterFor("data.csv")).toBe(",");
  });
});
