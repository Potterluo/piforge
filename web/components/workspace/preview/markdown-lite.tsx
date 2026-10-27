"use client";

import * as React from "react";

/**
 * A deliberately tiny markdown renderer (~100 lines).
 *
 * It covers what a code-review preview actually needs — headings, paragraphs,
 * fenced code, ordered/unordered lists, blockquotes, rules, and inline
 * bold/italic/code/links — instead of pulling a full CommonMark parser into the
 * bundle. Anything more exotic (tables, footnotes, MDX) is one click away in
 * the 源码 tab.
 */

function inline(text: string, keyPrefix: string): React.ReactNode[] {
  const pattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*]+\*)|(\[[^\]]+\]\([^)]+\))/g;
  const nodes: React.ReactNode[] = [];
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = pattern.exec(text)) !== null) {
    if (match.index > lastIndex) nodes.push(text.slice(lastIndex, match.index));
    const token = match[0];
    const key = `${keyPrefix}-${match.index}`;

    if (token.startsWith("`")) {
      nodes.push(
        <code key={key} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">
          {token.slice(1, -1)}
        </code>,
      );
    } else if (token.startsWith("**")) {
      nodes.push(<strong key={key}>{token.slice(2, -2)}</strong>);
    } else if (token.startsWith("[")) {
      const split = token.indexOf("](");
      const label = token.slice(1, split);
      const href = token.slice(split + 2, -1);
      nodes.push(
        <a
          key={key}
          href={href}
          target="_blank"
          rel="noreferrer"
          className="text-primary underline underline-offset-4"
        >
          {label}
        </a>,
      );
    } else {
      nodes.push(<em key={key}>{token.slice(1, -1)}</em>);
    }

    lastIndex = match.index + token.length;
  }

  if (lastIndex < text.length) nodes.push(text.slice(lastIndex));
  return nodes;
}

export function MarkdownLite({ source }: { source: string }) {
  const blocks = React.useMemo(() => render(source), [source]);
  return <div className="flex min-w-0 flex-col gap-3 text-sm leading-relaxed">{blocks}</div>;
}

function render(source: string): React.ReactNode[] {
  const lines = source.replace(/\r\n/g, "\n").split("\n");
  const blocks: React.ReactNode[] = [];
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];
    const key = `b${index}`;

    if (line.startsWith("```")) {
      const language = line.slice(3).trim();
      const body: string[] = [];
      index++;
      while (index < lines.length && !lines[index].startsWith("```")) {
        body.push(lines[index]);
        index++;
      }
      index++;
      blocks.push(
        <pre
          key={key}
          data-language={language || undefined}
          className="max-w-full overflow-x-auto rounded-md border bg-muted/40 p-2.5 font-mono text-xs"
        >
          <code>{body.join("\n")}</code>
        </pre>,
      );
      continue;
    }

    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const level = heading[1].length;
      const sizes = ["text-lg", "text-base", "text-sm", "text-sm", "text-xs", "text-xs"];
      blocks.push(
        <p
          key={key}
          className={`font-heading font-semibold text-foreground ${sizes[level - 1]}`}
        >
          {inline(heading[2], key)}
        </p>,
      );
      index++;
      continue;
    }

    if (/^\s*([-*]|\d+\.)\s+/.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: string[] = [];
      while (index < lines.length && /^\s*([-*]|\d+\.)\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*([-*]|\d+\.)\s+/, ""));
        index++;
      }
      const ListTag = ordered ? "ol" : "ul";
      blocks.push(
        <ListTag
          key={key}
          className={`ms-4 flex list-outside flex-col gap-1 ${ordered ? "list-decimal" : "list-disc"}`}
        >
          {items.map((item, itemIndex) => (
            <li key={itemIndex}>{inline(item, `${key}-${itemIndex}`)}</li>
          ))}
        </ListTag>,
      );
      continue;
    }

    if (line.startsWith(">")) {
      const quote: string[] = [];
      while (index < lines.length && lines[index].startsWith(">")) {
        quote.push(lines[index].replace(/^>\s?/, ""));
        index++;
      }
      blocks.push(
        <blockquote key={key} className="border-s-2 ps-3 text-muted-foreground">
          {inline(quote.join(" "), key)}
        </blockquote>,
      );
      continue;
    }

    if (/^(-{3,}|\*{3,})$/.test(line.trim())) {
      blocks.push(<hr key={key} className="border-border" />);
      index++;
      continue;
    }

    if (line.trim() === "") {
      index++;
      continue;
    }

    const paragraph: string[] = [];
    while (
      index < lines.length &&
      lines[index].trim() !== "" &&
      !lines[index].startsWith("```") &&
      !lines[index].startsWith("#") &&
      !lines[index].startsWith(">") &&
      !/^\s*([-*]|\d+\.)\s+/.test(lines[index])
    ) {
      paragraph.push(lines[index]);
      index++;
    }
    blocks.push(
      <p key={key} className="break-words text-foreground/90">
        {inline(paragraph.join(" "), key)}
      </p>,
    );
  }

  return blocks;
}
