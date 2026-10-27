"use client";

import { DiffModeEnum, DiffView } from "@git-diff-view/react";
// The library ships its own scoped stylesheet (every rule is inside
// `.diff-tailwindcss-wrapper`), so importing it globally cannot leak out.
import "@git-diff-view/react/styles/diff-view.css";

/**
 * The one module that imports `@git-diff-view/react`.
 *
 * It is loaded dynamically with `ssr: false` from `git-diff.tsx`: the package
 * carries a `"use client"` banner and instantiates `DiffFile` objects on mount,
 * and it is heavy (lowlight + highlight.js), so it should never sit in the
 * initial bundle for someone who only opens the file tree.
 */
export interface GitDiffImplProps {
  hunks: string[];
  fileName: string;
  language: string;
  dark: boolean;
  split: boolean;
  fontSize?: number;
}

export function GitDiffImpl({
  hunks,
  fileName,
  language,
  dark,
  split,
  fontSize = 12,
}: GitDiffImplProps) {
  return (
    <DiffView
      data={{
        oldFile: { fileName, fileLang: language },
        newFile: { fileName, fileLang: language },
        hunks,
      }}
      diffViewMode={split ? DiffModeEnum.Split : DiffModeEnum.Unified}
      diffViewTheme={dark ? "dark" : "light"}
      diffViewFontSize={fontSize}
      diffViewHighlight
      diffViewWrap={false}
    />
  );
}
