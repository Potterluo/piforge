import { describe, expect, it } from "vitest";

import { deriveProjectName, freeProjectName, repoBasename } from "./name";

describe("repoBasename", () => {
  it("takes the last path segment of every supported address", () => {
    expect(repoBasename("https://github.com/octocat/Spoon-Knife.git")).toBe("Spoon-Knife");
    expect(repoBasename("https://github.com/octocat/Spoon-Knife")).toBe("Spoon-Knife");
    expect(repoBasename("git@github.com:x/y.git")).toBe("y");
    expect(repoBasename("git@github.com:single.git")).toBe("single");
    expect(repoBasename("C:\\repos\\cache-service")).toBe("cache-service");
    expect(repoBasename("/repos/cache-service.git")).toBe("cache-service");
  });

  it("ignores trailing slashes and query strings", () => {
    expect(repoBasename("https://github.com/octocat/Spoon-Knife.git/")).toBe("Spoon-Knife");
    expect(repoBasename("https://github.com/octocat/Spoon-Knife.git?ref=main")).toBe(
      "Spoon-Knife",
    );
  });

  it("has no name for an empty address", () => {
    expect(repoBasename("")).toBe("");
    expect(repoBasename("   ")).toBe("");
  });
});

describe("deriveProjectName", () => {
  it("derives the repository name", () => {
    expect(deriveProjectName("https://github.com/octocat/Spoon-Knife.git")).toBe("Spoon-Knife");
    expect(deriveProjectName("git@github.com:x/y.git")).toBe("y");
  });

  it("dates a blank workspace so two empty projects are distinguishable", () => {
    expect(deriveProjectName("", new Date(2026, 9, 29))).toBe("空白工作区 · 10-29");
    expect(deriveProjectName("", new Date(2026, 0, 3))).toBe("空白工作区 · 01-03");
  });
});

describe("freeProjectName", () => {
  it("keeps a free name and suffixes a taken one", () => {
    expect(freeProjectName("frontend", [])).toBe("frontend");
    expect(freeProjectName("frontend", ["frontend"])).toBe("frontend-2");
    expect(freeProjectName("frontend", ["frontend", "frontend-2"])).toBe("frontend-3");
    // A gap is not reused: the first free suffix wins, in order.
    expect(freeProjectName("frontend", ["frontend", "frontend-3"])).toBe("frontend-2");
  });

  it("compares case-insensitively, like the API", () => {
    expect(freeProjectName("Frontend", ["frontend"])).toBe("Frontend-2");
    expect(freeProjectName("frontend", ["FRONTEND", "Frontend-2"])).toBe("frontend-3");
  });

  it("has nothing to offer for an empty base", () => {
    expect(freeProjectName("   ", ["x"])).toBe("");
  });
});
