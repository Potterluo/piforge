import { describe, expect, it } from "vitest";
import { asPTYInput, buildCdCommand } from "./cd";

describe("buildCdCommand", () => {
  it("changes into the file's directory", () => {
    expect(buildCdCommand("internal/cache/lru.go")).toBe('cd "internal/cache"');
  });

  it("uses the repository root for a top-level file", () => {
    expect(buildCdCommand("README.md")).toBe("cd .");
  });

  it("allows the characters a real repository path contains", () => {
    expect(buildCdCommand("src/v1.2/@scope/pkg-name_test.go")).toBe('cd "src/v1.2/@scope"');
  });

  it.each([
    ["traversal", "../../etc/passwd"],
    ["absolute", "/etc/passwd"],
    ["windows absolute", "C:/Windows"],
    ["shell separator", "src/a; rm -rf ~"],
    ["command substitution", "src/$HOME"],
    ["backtick", "src/`id`"],
    ["newline", "src\nrm -rf ~"],
    ["quote", 'src/a"b'],
    ["empty", "   "],
  ])("refuses %s", (_name, path) => {
    // The command is typed into a real shell, so refusing is the safe answer;
    // the caller falls back to showing the path instead of running anything.
    expect(buildCdCommand(path)).toBeNull();
  });

  it("ends the line so the shell actually runs it", () => {
    expect(asPTYInput('cd "src"')).toBe('cd "src"\r');
  });
});
