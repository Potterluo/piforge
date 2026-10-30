"use client";

import * as React from "react";
import { MessageSquarePlusIcon } from "lucide-react";

import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockFooter,
  CodeBlockHeader,
  CodeBlockLine,
  CodeBlockTitle,
} from "@/components/ui/code-block";
import { Badge } from "@/components/ui/badge";
import { baseName } from "@/lib/fs/file-kind";
import { highlightCode } from "@/lib/highlighter";
import { cn } from "@/lib/utils";

/**
 * Read-only code view.
 *
 * Highlighting goes through the shared shiki helper in `lib/highlighter.tsx`,
 * which loads grammars on demand. It is async and may not know a language, so
 * the plain-text path is the baseline and highlighting is an upgrade — the
 * panel never blocks on it.
 *
 * `line` is the link a test failure carries: the file opens at that line, with
 * the line highlighted and scrolled into view. Rendering every line through
 * `CodeBlockLine` (rather than letting `CodeBlockContent` split a string) is
 * what makes one line addressable.
 */
const MAX_RENDERED_LINES = 3000;

export function CodePreview({
  path,
  content,
  language,
  line,
  onCommentLine,
}: {
  path: string;
  content: string;
  language: string;
  line?: number | null;
  /**
   * FR-12.3: called with the line number and its text when the user asks to
   * comment on that line. The preview does not own the comment editor; it only
   * reports which line was picked, so the same draft store is used here and in
   * the diff.
   */
  onCommentLine?: (line: number, text: string) => void;
}) {
  const [highlighted, setHighlighted] = React.useState<{
    content: string;
    language: string;
    lines: React.ReactNode[][] | null;
  } | null>(null);

  React.useEffect(() => {
    let alive = true;
    highlightCode(content, language)
      .then((result) => {
        if (alive) setHighlighted({ content, language, lines: result });
      })
      .catch(() => {
        /* Unknown grammars fall back to the plain path below. */
      });
    return () => {
      alive = false;
    };
  }, [content, language]);

  // Derived, not state: a stale highlight from the previous file must not be
  // shown under this path while the new one is being computed.
  const lines =
    highlighted && highlighted.content === content && highlighted.language === language
      ? highlighted.lines
      : null;

  const plain = React.useMemo(() => content.replace(/\n$/, "").split("\n"), [content]);
  const clipped = plain.length > MAX_RENDERED_LINES;
  const active = typeof line === "number" && line > 0 ? line : null;
  const hostRef = React.useRef<HTMLDivElement | null>(null);

  React.useEffect(() => {
    if (!active) return;
    const host = hostRef.current;
    if (!host) return;
    const target = host.querySelector(`[data-line="${active}"]`);
    // jsdom has no layout, so only call when the method exists.
    target?.scrollIntoView?.({ block: "center" });
  }, [active, content, lines]);

  const rendered = (lines ?? plain).slice(0, MAX_RENDERED_LINES).map((entry, index) => {
    const number = index + 1;
    return (
      <CodeBlockLine
        key={index}
        data-line={number}
        variant={active === number ? "highlighted" : "default"}
        className={cn("group relative", active === number && "ring-1 ring-amber-500/50 ring-inset")}
      >
        {entry}
        {onCommentLine ? (
          <button
            type="button"
            data-testid="file-line-comment"
            data-line={number}
            aria-label={`评论 ${path}:${number}`}
            title={`评论 ${path}:${number}`}
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              onCommentLine(number, plain[index] ?? "");
            }}
            className="invisible absolute end-1 top-0 rounded p-0.5 text-muted-foreground opacity-0 transition group-hover:visible group-hover:opacity-100 hover:bg-accent hover:text-foreground focus:visible focus:opacity-100"
          >
            <MessageSquarePlusIcon className="size-3" />
          </button>
        ) : null}
      </CodeBlockLine>
    );
  });

  return (
    <div
      ref={hostRef}
      data-testid="code-preview"
      data-active-line={active ?? undefined}
      className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden"
    >
      <CodeBlock className="min-h-0 rounded-none border-0">
        <CodeBlockHeader>
          <CodeBlockTitle className="truncate font-mono">{baseName(path)}</CodeBlockTitle>
          {active ? (
            <Badge variant="outline" className="font-mono text-[10px]">
              第 {active} 行
            </Badge>
          ) : null}
          <Badge variant="outline" className="font-mono text-[10px]">
            {language}
          </Badge>
          <CodeBlockActions>
            <CodeBlockCopyButton value={content} />
          </CodeBlockActions>
        </CodeBlockHeader>

        <CodeBlockContent lineNumbers className="max-h-none">
          {rendered}
        </CodeBlockContent>

        {clipped ? (
          <CodeBlockFooter className="text-[10px]">
            仅渲染前 {MAX_RENDERED_LINES} 行，可下载完整文件
          </CodeBlockFooter>
        ) : null}
      </CodeBlock>
    </div>
  );
}
