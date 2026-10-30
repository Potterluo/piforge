"use client";

import * as React from "react";

import { CodeBlock, CodeBlockContent } from "@/components/ui/code-block";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { PatchView } from "@/components/workspace/patch-view";
import { parsePatch } from "@/lib/git/patch-lines";

/**
 * Unified diff rendering.
 *
 * The rows come from `PatchView`, which renders the patch line by line with the
 * old/new line numbers the `@@` header implies. That is what makes a line
 * commentable (FR-12.3); it is also why `@git-diff-view/react` is gone from this
 * path: in this build it throws inside `DiffUnifiedView` and the error boundary
 * fell back to one preformatted block of patch text, which has no lines to
 * point at. A patch with no hunk (binary, mode-only) still degrades to that
 * preformatted block, because there is nothing to anchor a comment to.
 */
export interface GitDiffViewProps {
  sessionId: string;
  path: string;
  patch: string;
  split: boolean;
}

export function GitDiffView({ sessionId, path, patch, split }: GitDiffViewProps) {
  const hasHunk = React.useMemo(
    () => parsePatch(patch).some((line) => line.kind !== "meta"),
    [patch],
  );

  if (!patch.trim()) {
    return (
      <Empty className="flex-1">
        <EmptyHeader>
          <EmptyTitle>没有差异</EmptyTitle>
          <EmptyDescription>这个文件在当前范围内没有文本改动。</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  if (!hasHunk) {
    return (
      <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="git-diff-patch">
        <p className="shrink-0 border-b px-2 py-1 text-[10px] text-muted-foreground">
          无 hunk（二进制或仅权限变更），显示原始 patch
        </p>
        <div className="min-h-0 min-w-0 overflow-auto">
          <CodeBlock className="rounded-none border-0">
            <CodeBlockContent lineNumbers>{patch}</CodeBlockContent>
          </CodeBlock>
        </div>
      </div>
    );
  }

  return <PatchView sessionId={sessionId} path={path} patch={patch} split={split} />;
}
