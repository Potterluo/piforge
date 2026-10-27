"use client";

import * as React from "react";

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

/**
 * Read-only code view.
 *
 * Highlighting goes through the shared shiki helper in `lib/highlighter.tsx`,
 * which loads grammars on demand. It is async and may not know a language, so
 * the plain-text path is the baseline and highlighting is an upgrade — the
 * panel never blocks on it.
 */
const MAX_RENDERED_LINES = 3000;

export function CodePreview({
  path,
  content,
  language,
}: {
  path: string;
  content: string;
  language: string;
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

  return (
    <div className="flex min-h-0 min-w-0 flex-col overflow-hidden">
      <CodeBlock className="min-h-0 rounded-none border-0">
        <CodeBlockHeader>
          <CodeBlockTitle className="truncate font-mono">{baseName(path)}</CodeBlockTitle>
          <Badge variant="outline" className="font-mono text-[10px]">
            {language}
          </Badge>
          <CodeBlockActions>
            <CodeBlockCopyButton value={content} />
          </CodeBlockActions>
        </CodeBlockHeader>

        {lines ? (
          <CodeBlockContent lineNumbers className="max-h-none">
            {lines.slice(0, MAX_RENDERED_LINES).map((line, index) => (
              <CodeBlockLine key={index}>{line}</CodeBlockLine>
            ))}
          </CodeBlockContent>
        ) : (
          <CodeBlockContent lineNumbers className="max-h-none">
            {clipped ? plain.slice(0, MAX_RENDERED_LINES).join("\n") : content}
          </CodeBlockContent>
        )}

        {clipped ? (
          <CodeBlockFooter className="text-[10px]">
            仅渲染前 {MAX_RENDERED_LINES} 行，可下载完整文件
          </CodeBlockFooter>
        ) : null}
      </CodeBlock>
    </div>
  );
}
