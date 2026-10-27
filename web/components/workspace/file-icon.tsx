"use client";

import * as React from "react";
import {
  BracesIcon,
  FileArchiveIcon,
  FileCodeIcon,
  FileDiffIcon,
  FileIcon,
  FileImageIcon,
  FileJsonIcon,
  FileSpreadsheetIcon,
  FileTerminalIcon,
  FileTextIcon,
  FileTypeIcon,
  FolderIcon,
  FolderOpenIcon,
} from "lucide-react";

import { extensionOf } from "@/lib/fs/file-kind";

/**
 * Extension-derived icon for a tree row / preview header.
 *
 * Icons are monochrome lucide glyphs on purpose: the tree already carries a
 * coloured git chip, and two colour scales in a 28px row read as noise.
 *
 * This returns a *JSX element* rather than a component reference on purpose:
 * picking a component into a local (`const Icon = ...; <Icon />`) is flagged by
 * the React Compiler lint as "creating components during render", while literal
 * tags are not.
 */
export function fileIconElement(
  path: string,
  isDir: boolean,
  isOpen: boolean,
  className?: string,
): React.ReactElement {
  if (isDir) {
    return isOpen ? (
      <FolderOpenIcon className={className} />
    ) : (
      <FolderIcon className={className} />
    );
  }

  switch (extensionOf(path)) {
    case "md":
    case "mdx":
    case "markdown":
    case "txt":
    case "rst":
      return <FileTextIcon className={className} />;
    case "json":
    case "json5":
    case "lock":
      return <FileJsonIcon className={className} />;
    case "png":
    case "jpg":
    case "jpeg":
    case "gif":
    case "svg":
    case "webp":
    case "ico":
      return <FileImageIcon className={className} />;
    case "csv":
    case "tsv":
    case "xls":
    case "xlsx":
      return <FileSpreadsheetIcon className={className} />;
    case "diff":
    case "patch":
      return <FileDiffIcon className={className} />;
    case "zip":
    case "tar":
    case "gz":
    case "rar":
    case "7z":
      return <FileArchiveIcon className={className} />;
    case "sh":
    case "bash":
    case "zsh":
    case "ps1":
      return <FileTerminalIcon className={className} />;
    case "html":
    case "htm":
    case "css":
    case "scss":
    case "less":
      return <FileTypeIcon className={className} />;
    case "toml":
    case "yaml":
    case "yml":
    case "ini":
    case "conf":
      return <BracesIcon className={className} />;
    case "":
      return <FileIcon className={className} />;
    default:
      return <FileCodeIcon className={className} />;
  }
}
