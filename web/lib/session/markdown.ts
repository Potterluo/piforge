/**
 * Fenced-code splitter for the minimal Markdown renderer.
 *
 * Kept in its own dependency-free module so the splitting rules (the part that
 * actually breaks) can be unit-tested without mounting React or the code-block
 * primitives.
 */
export type MarkdownBlock =
  | { kind: "code"; lang: string; code: string; closed: boolean }
  | { kind: "text"; text: string };

const FENCE_OPEN = /^\s*```(.*)$/;
const FENCE_CLOSE = /^\s*```\s*$/;

/**
 * Splits a Markdown string into prose and fenced code segments.
 *
 * An unterminated fence yields `closed: false` and swallows the remainder, so
 * streaming output keeps rendering as code instead of flickering back to prose.
 */
export function splitMarkdownBlocks(source: string): MarkdownBlock[] {
  const blocks: MarkdownBlock[] = [];
  let prose: string[] = [];
  let fenced: string[] = [];
  let inFence = false;
  let lang = "";

  const flushProse = () => {
    const text = prose.join("\n");
    if (text.trim().length > 0) blocks.push({ kind: "text", text });
    prose = [];
  };

  for (const line of source.split("\n")) {
    if (!inFence) {
      const open = FENCE_OPEN.exec(line);
      if (open) {
        flushProse();
        inFence = true;
        lang = (open[1] ?? "").trim().split(/\s+/)[0] ?? "";
        fenced = [];
        continue;
      }
      prose.push(line);
      continue;
    }

    if (FENCE_CLOSE.test(line)) {
      blocks.push({ kind: "code", lang, code: fenced.join("\n"), closed: true });
      inFence = false;
      fenced = [];
      continue;
    }
    fenced.push(line);
  }

  if (inFence) {
    blocks.push({ kind: "code", lang, code: fenced.join("\n"), closed: false });
  } else {
    flushProse();
  }
  return blocks;
}
