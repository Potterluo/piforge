/**
 * A deliberately small Markdown renderer for assistant prose.
 *
 * The product renders model output, and model output is untrusted: no HTML is
 * ever injected, links are restricted to http(s), and everything else becomes
 * text nodes. Fenced blocks become the shadcn CodeBlock (with copy disabled
 * while a fence is still open, because half a snippet is not copyable), inline
 * code gets a styled <code>, and the rest is paragraphs plus lists.
 */
import type { ReactNode } from "react";

import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/components/ui/code-block";
import { splitMarkdownBlocks } from "@/lib/session/markdown";
import { cn } from "@/lib/utils";

const INLINE = /(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\n]+\*|\[[^\]\n]+\]\((?:https?:\/\/)[^)\s]+\))/g;

/** Inline markup: code, bold, italic, and http(s) links. Nothing else. */
function inline(text: string, keyPrefix: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let cursor = 0;
  let match: RegExpExecArray | null;
  INLINE.lastIndex = 0;

  while ((match = INLINE.exec(text)) !== null) {
    if (match.index > cursor) nodes.push(text.slice(cursor, match.index));
    const token = match[0];
    const key = `${keyPrefix}-${match.index}`;

    if (token.startsWith("`")) {
      nodes.push(
        <code key={key} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.9em]">
          {token.slice(1, -1)}
        </code>,
      );
    } else if (token.startsWith("**")) {
      nodes.push(<strong key={key}>{token.slice(2, -2)}</strong>);
    } else if (token.startsWith("*")) {
      nodes.push(<em key={key}>{token.slice(1, -1)}</em>);
    } else {
      const split = token.lastIndexOf("](");
      const label = token.slice(1, split);
      const href = token.slice(split + 2, -1);
      nodes.push(
        <a key={key} href={href} target="_blank" rel="noreferrer noopener" className="underline">
          {label}
        </a>,
      );
    }
    cursor = match.index + token.length;
  }

  if (cursor < text.length) nodes.push(text.slice(cursor));
  return nodes;
}

/** Paragraphs and lists only; headings collapse into a bold paragraph. */
function Prose({ text, idPrefix }: { text: string; idPrefix: string }) {
  const nodes: ReactNode[] = [];
  let paragraph: string[] = [];
  let list: string[] = [];
  let ordered = false;
  let counter = 0;

  const flushParagraph = () => {
    if (paragraph.length === 0) return;
    const joined = paragraph.join(" ");
    counter += 1;
    nodes.push(<p key={`${idPrefix}-p-${counter}`}>{inline(joined, `${idPrefix}-p-${counter}`)}</p>);
    paragraph = [];
  };

  const flushList = () => {
    if (list.length === 0) return;
    counter += 1;
    const items = list.map((entry, index) => (
      <li key={`${idPrefix}-li-${index}`}>{inline(entry, `${idPrefix}-li-${index}`)}</li>
    ));
    nodes.push(
      ordered ? (
        <ol key={`${idPrefix}-list-${counter}`} className="list-decimal pl-5">
          {items}
        </ol>
      ) : (
        <ul key={`${idPrefix}-list-${counter}`} className="list-disc pl-5">
          {items}
        </ul>
      ),
    );
    list = [];
  };

  for (const line of text.split("\n")) {
    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }
    const bullet = /^\s*[-*+]\s+(.*)$/.exec(line);
    const numbered = /^\s*\d+[.)]\s+(.*)$/.exec(line);
    if (bullet || numbered) {
      flushParagraph();
      const isOrdered = Boolean(numbered);
      if (list.length > 0 && ordered !== isOrdered) flushList();
      ordered = isOrdered;
      list.push((bullet?.[1] ?? numbered?.[1]) ?? "");
      continue;
    }
    if (list.length > 0) flushList();
    paragraph.push(line.trim());
  }
  flushParagraph();
  flushList();

  return <div className="flex flex-col gap-2.5">{nodes}</div>;
}

export function Markdown({ text, className }: { text: string; className?: string }) {
  const blocks = splitMarkdownBlocks(text);

  return (
    <div className={cn("flex min-w-0 flex-col gap-3 text-sm leading-relaxed", className)}>
      {blocks.map((block, index) =>
        block.kind === "text" ? (
          <Prose key={`prose-${index}`} text={block.text} idPrefix={`md-${index}`} />
        ) : (
          <CodeBlock key={`code-${index}`} size="sm">
            <CodeBlockHeader>
              <CodeBlockTitle>{block.lang || "text"}</CodeBlockTitle>
              <CodeBlockActions>
                <CodeBlockCopyButton value={block.code} disabled={!block.closed} />
              </CodeBlockActions>
            </CodeBlockHeader>
            <CodeBlockContent>{block.code}</CodeBlockContent>
          </CodeBlock>
        ),
      )}
    </div>
  );
}
