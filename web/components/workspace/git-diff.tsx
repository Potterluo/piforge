"use client";

import * as React from "react";
import dynamic from "next/dynamic";

import { CodeBlock, CodeBlockContent } from "@/components/ui/code-block";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { languageOf } from "@/lib/fs/file-kind";
import { splitHunks } from "@/lib/git/patch";
import { PanelSkeleton } from "@/components/workspace/panel-state";
import { useIsDark } from "@/components/workspace/use-is-dark";

/**
 * Unified diff rendering.
 *
 * `@git-diff-view` does not accept a patch string: it wants `hunks`, one string
 * per `@@` block, plus the old/new filenames. `splitHunks()` in `lib/git/patch`
 * does that conversion, and when a patch has no hunk (binary, mode-only) or the
 * library throws, this component degrades to the raw patch in a `CodeBlock`
 * instead of rendering an empty box.
 */
const GitDiffImpl = dynamic(
  () => import("@/components/workspace/git-diff-impl").then((mod) => mod.GitDiffImpl),
  { ssr: false, loading: () => <PanelSkeleton rows={10} /> },
);

interface BoundaryProps {
  fallback: React.ReactNode;
  children: React.ReactNode;
}

class DiffBoundary extends React.Component<BoundaryProps, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true };
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

function RawPatch({ patch }: { patch: string }) {
  return (
    <div className="min-h-0 min-w-0 overflow-auto">
      <CodeBlock className="rounded-none border-0">
        <CodeBlockContent lineNumbers>{patch}</CodeBlockContent>
      </CodeBlock>
    </div>
  );
}

export interface GitDiffViewProps {
  path: string;
  patch: string;
  split: boolean;
}

export function GitDiffView({ path, patch, split }: GitDiffViewProps) {
  const dark = useIsDark();
  const hunks = React.useMemo(() => splitHunks(patch), [patch]);

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

  if (hunks.length === 0) {
    return (
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <p className="shrink-0 border-b px-2 py-1 text-[10px] text-muted-foreground">
          无 hunk（二进制或仅权限变更），显示原始 patch
        </p>
        <RawPatch patch={patch} />
      </div>
    );
  }

  return (
    <div className="diff-tailwindcss-wrapper min-h-0 min-w-0 flex-1 overflow-auto">
      <DiffBoundary fallback={<RawPatch patch={patch} />}>
        <GitDiffImpl
          hunks={hunks}
          fileName={path}
          language={languageOf(path)}
          dark={dark}
          split={split}
        />
      </DiffBoundary>
    </div>
  );
}
