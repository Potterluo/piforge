"use client";

import * as React from "react";
import { MessageSquarePlusIcon } from "lucide-react";

import { ReviewCommentEditor, ReviewCommentRow, useReviewDrafts } from "@/components/workspace/review-comment";
import { anchorForLine, pairRows, parsePatch, type PatchLine } from "@/lib/git/patch-lines";
import type { ReviewDraft } from "@/lib/review/comments";
import { cn } from "@/lib/utils";

/**
 * A patch rendered as addressable lines, with a comment affordance per line.
 *
 * This replaced the `@git-diff-view/react` renderer. That component never
 * produced a row in this build (its unified view throws, and the file's own
 * fallback then showed the raw patch as one preformatted block), which made
 * "comment on this line" impossible: a string has no lines to point at. The rows
 * below keep the same test ids (`git-diff-view` / `git-diff-patch`) and the same
 * `+`/`-` markers other tests assert on, and every line carries the file line
 * number the hunk header implies.
 */
export function PatchView({
  sessionId,
  path,
  patch,
  split,
}: {
  sessionId: string;
  path: string;
  patch: string;
  split: boolean;
}) {
  const lines = React.useMemo(() => parsePatch(patch), [patch]);
  const { drafts } = useReviewDrafts(sessionId);
  const [editing, setEditing] = React.useState<PatchLine | null>(null);

  const forPath = React.useMemo(
    () => drafts.filter((draft) => draft.path === path),
    [drafts, path],
  );
  const onLine = React.useCallback(
    (line: PatchLine): ReviewDraft[] => {
      const number = line.newLine ?? line.oldLine ?? 0;
      return forPath.filter((draft) => draft.line === number && draft.side === (line.newLine !== null ? "new" : "old"));
    },
    [forPath],
  );

  const rows = React.useMemo(() => (split ? pairRows(lines) : null), [lines, split]);

  const renderCell = (line: PatchLine | null, side: "old" | "new") => {
    if (!line) {
      return <div className="min-w-0 flex-1 bg-muted/30" />;
    }
    const number = side === "old" ? line.oldLine : line.newLine;
    const commentable = line.kind === "add" || line.kind === "ctx" || line.kind === "del";
    const marker = line.kind === "add" ? "+" : line.kind === "del" ? "-" : " ";
    return (
      <div className="flex min-w-0 flex-1 items-start">
        <span className="w-9 shrink-0 select-none text-right font-mono text-[10px] leading-5 text-muted-foreground/70">
          {number ?? ""}
        </span>
        <span
          className={cn(
            "w-3 shrink-0 select-none text-center font-mono text-[11px] leading-5",
            line.kind === "add" && "text-emerald-600 dark:text-emerald-400",
            line.kind === "del" && "text-red-600 dark:text-red-400",
          )}
        >
          {marker}
        </span>
        <span
          className={cn(
            "min-w-0 flex-1 whitespace-pre-wrap break-words font-mono text-[11px] leading-5",
            line.kind === "add" && "bg-emerald-500/10",
            line.kind === "del" && "bg-red-500/10",
          )}
        >
          {line.text || " "}
        </span>
        {commentable ? (
          <button
            type="button"
            data-testid="diff-line-comment"
            data-side={side}
            aria-label={`评论 ${path}:${number}`}
            title={`评论 ${path}:${number}`}
            onClick={() => setEditing(line)}
            className="invisible shrink-0 rounded p-0.5 text-muted-foreground opacity-0 transition group-hover:visible group-hover:opacity-100 hover:bg-accent hover:text-foreground focus:visible focus:opacity-100"
          >
            <MessageSquarePlusIcon className="size-3" />
          </button>
        ) : (
          <span className="w-[18px] shrink-0" />
        )}
      </div>
    );
  };

  const renderComments = (line: PatchLine) => {
    const rowsWithDrafts = onLine(line);
    const number = line.newLine ?? line.oldLine ?? 0;
    return (
      <>
        {editing && editing.index === line.index ? (
          <ReviewCommentEditor
            sessionId={sessionId}
            anchor={anchorForLine(path, line)!}
            existing={rowsWithDrafts[0]}
            onClose={() => setEditing(null)}
          />
        ) : null}
        {rowsWithDrafts.map((draft) => (
          <ReviewCommentRow
            key={draft.id}
            sessionId={sessionId}
            draft={draft}
            onEdit={() => setEditing(lines[line.index] ?? line)}
          />
        ))}
        {rowsWithDrafts.length > 0 ? (
          <span className="sr-only" data-testid="diff-line-marker" data-line={number} data-count={rowsWithDrafts.length}>
            {number} 行有 {rowsWithDrafts.length} 条评论
          </span>
        ) : null}
      </>
    );
  };

  return (
    <div
      data-testid="git-diff-view"
      data-lines={lines.length}
      data-comments={forPath.length}
      className="flex min-h-0 min-w-0 flex-1 flex-col overflow-auto [&_*]:font-[inherit]"
    >
      {forPath.length > 0 ? (
        <div
          data-testid="diff-comment-count"
          className="sticky top-0 z-10 shrink-0 border-b bg-background/95 px-2 py-0.5 text-[10px] text-muted-foreground"
        >
          这个文件有 {forPath.length} 条行内评论
        </div>
      ) : null}
      {split && rows
        ? rows.map((row, index) => {
            // Headers and metadata are full-width in a split view too: repeating a
            // `diff --git` line in both columns reads like two files.
            const single = row.left ?? row.right;
            if (single && (single.kind === "hunk" || single.kind === "meta" || single.kind === "nonewline")) {
              return (
                <div
                  key={index}
                  data-testid="diff-line-row"
                  data-kind={single.kind}
                  className={cn("group flex min-w-0", single.kind === "hunk" && "bg-muted/50")}
                >
                  <span className="min-w-0 flex-1 whitespace-pre-wrap break-words px-2 font-mono text-[10px] text-muted-foreground">
                    {single.kind === "hunk" ? single.raw : single.text}
                  </span>
                </div>
              );
            }
            return (
              <React.Fragment key={index}>
                <div className="group flex min-w-0 gap-1 border-b border-border/40 last:border-0">
                  {renderCell(row.left, "old")}
                  {renderCell(row.right, "new")}
                </div>
                {row.right ? renderComments(row.right) : null}
                {!row.right && row.left ? renderComments(row.left) : null}
              </React.Fragment>
            );
          })
        : lines.map((line) => {
            if (line.kind === "meta" || line.kind === "nonewline") {
              return (
                <div
                  key={line.index}
                  data-testid="diff-line-row"
                  data-kind={line.kind}
                  className="group flex min-w-0"
                >
                  <span className="min-w-0 flex-1 whitespace-pre-wrap break-words px-2 font-mono text-[10px] text-muted-foreground/60">
                    {line.text}
                  </span>
                </div>
              );
            }
            if (line.kind === "hunk") {
              return (
                <div
                  key={line.index}
                  data-testid="diff-line-row"
                  data-kind="hunk"
                  className="group flex min-w-0 bg-muted/50"
                >
                  <span className="min-w-0 flex-1 whitespace-pre-wrap break-words px-2 font-mono text-[10px] text-muted-foreground">
                    {line.raw}
                  </span>
                </div>
              );
            }
            return (
              <React.Fragment key={line.index}>
                <div
                  data-testid="diff-line-row"
                  data-kind={line.kind}
                  data-new-line={line.newLine ?? undefined}
                  data-old-line={line.oldLine ?? undefined}
                  className="group flex min-w-0 items-start"
                >
                  <span className="w-9 shrink-0 select-none text-right font-mono text-[10px] leading-5 text-muted-foreground/70">
                    {line.oldLine ?? ""}
                  </span>
                  <span className="w-9 shrink-0 select-none text-right font-mono text-[10px] leading-5 text-muted-foreground/70">
                    {line.newLine ?? ""}
                  </span>
                  <span
                    className={cn(
                      "w-3 shrink-0 select-none text-center font-mono text-[11px] leading-5",
                      line.kind === "add" && "text-emerald-600 dark:text-emerald-400",
                      line.kind === "del" && "text-red-600 dark:text-red-400",
                    )}
                  >
                    {line.kind === "add" ? "+" : line.kind === "del" ? "-" : " "}
                  </span>
                  <span
                    className={cn(
                      "min-w-0 flex-1 whitespace-pre-wrap break-words font-mono text-[11px] leading-5",
                      line.kind === "add" && "bg-emerald-500/10",
                      line.kind === "del" && "bg-red-500/10",
                    )}
                  >
                    {line.text || " "}
                  </span>
                  <button
                    type="button"
                    data-testid="diff-line-comment"
                    data-side={line.newLine !== null ? "new" : "old"}
                    aria-label={`评论 ${path}:${line.newLine ?? line.oldLine}`}
                    title={`评论 ${path}:${line.newLine ?? line.oldLine}`}
                    onClick={() => setEditing(line)}
                    className="invisible shrink-0 rounded p-0.5 text-muted-foreground opacity-0 transition group-hover:visible group-hover:opacity-100 hover:bg-accent hover:text-foreground focus:visible focus:opacity-100"
                  >
                    <MessageSquarePlusIcon className="size-3" />
                  </button>
                </div>
                {renderComments(line)}
              </React.Fragment>
            );
          })}
    </div>
  );
}
