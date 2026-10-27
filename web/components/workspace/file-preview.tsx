"use client";

import * as React from "react";
import { DownloadIcon, FileTextIcon, XIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import api from "@/lib/api/client";
import { baseName, formatBytes, imageMimeOf, kindOf, languageOf } from "@/lib/fs/file-kind";
import type { FileContent } from "@/lib/types";
import { PanelError, PanelSkeleton } from "@/components/workspace/panel-state";
import { useAsync } from "@/components/workspace/use-async";
import { BinaryCard } from "@/components/workspace/preview/binary-card";
import { CodePreview } from "@/components/workspace/preview/code-preview";
import { HtmlPreview } from "@/components/workspace/preview/html-preview";
import { ImagePreview } from "@/components/workspace/preview/image-preview";
import { MarkdownPreview } from "@/components/workspace/preview/markdown-preview";
import { TablePreview } from "@/components/workspace/preview/table-preview";

export interface FilePreviewProps {
  sessionId: string;
  path: string | null;
  onClose?: () => void;
}

/** Builds a `data:` URL for an inline image, or null when the body is not usable. */
function toImageSrc(content: FileContent): string | null {
  const body = content.body?.trim();
  if (!body) return null;

  // SVG arrives as text far more often than as base64.
  if (body.startsWith("<svg") || body.startsWith("<?xml")) {
    return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(content.body)}`;
  }

  const looksBase64 = /^[A-Za-z0-9+/=\s]+$/.test(body.slice(0, 256));
  if (!looksBase64) return null;
  return `data:${imageMimeOf(content.path, content.mime_type)};base64,${body.replace(/\s/g, "")}`;
}

/**
 * Renders one file from the file API.
 *
 * The dispatcher stays thin: every media type has its own module under
 * `preview/`, which is what keeps this file readable at ~200 lines and makes
 * adding pdf/pdfjs later a one-line change.
 */
export function FilePreview({ sessionId, path, onClose }: FilePreviewProps) {
  const file = useAsync(path ? `file:${sessionId}:${path}` : "file:none", async () => {
    if (!path) throw new Error("未选择文件");
    return api.files.read(sessionId, path);
  }, { enabled: Boolean(path) });

  const downloadUrl = React.useMemo(
    () => (path ? api.files.downloadUrl(sessionId, path) : ""),
    [sessionId, path],
  );

  if (!path) {
    return (
      <Empty className="flex-1">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <FileTextIcon />
          </EmptyMedia>
          <EmptyTitle>未打开文件</EmptyTitle>
          <EmptyDescription>从左侧文件树选择一个文件即可预览。</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  // Drop stale content while a different path is loading, so the header and
  // the body can never disagree about which file is on screen.
  const content = file.loading ? null : file.data;
  const kind = content ? kindOf(content.path, content.mime_type) : "text";
  const language = content?.language ?? languageOf(path);

  return (
    <div data-testid="file-preview" className="flex h-full min-h-0 min-w-0 flex-col">
      <header className="flex shrink-0 items-center gap-1.5 border-b px-2 py-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-[11px]" title={path}>
          {path}
        </span>
        {content ? (
          <Badge variant="outline" className="shrink-0 font-mono text-[10px]">
            {language === "plaintext" ? (content.mime_type ?? "text") : language}
          </Badge>
        ) : null}
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="下载"
          nativeButton={false}
          render={<a href={downloadUrl} download={baseName(path)} />}
        >
          <DownloadIcon />
        </Button>
        {onClose ? (
          <Button variant="ghost" size="icon-xs" aria-label="关闭预览" onClick={onClose}>
            <XIcon />
          </Button>
        ) : null}
      </header>

      {file.loading ? <PanelSkeleton rows={14} testId="preview-loading" /> : null}

      {file.error ? (
        <PanelError testId="preview-error" message={file.error} onRetry={file.reload} />
      ) : null}

      {content ? (
        <div
          data-testid="file-preview-body"
          className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden"
        >
          {content.truncated ? (
            <div className="shrink-0 border-b bg-muted/40 px-2 py-1 text-[10px] text-muted-foreground">
              文件过大，仅显示首尾片段 · 共 {formatBytes(content.size)}
            </div>
          ) : null}

          {content.binary && kind !== "image" ? (
            <BinaryCard
              path={content.path}
              size={content.size}
              mime={content.mime_type}
              reason="二进制文件无法内联预览"
              downloadUrl={downloadUrl}
            />
          ) : null}

          {kind === "image" ? (
            <ImageSurface content={content} downloadUrl={downloadUrl} />
          ) : null}

          {!content.binary && kind === "html" ? (
            <HtmlPreview content={content.body} path={content.path} />
          ) : null}

          {!content.binary && kind === "markdown" ? (
            <MarkdownPreview content={content.body} />
          ) : null}

          {!content.binary && kind === "table" ? (
            <TablePreview path={content.path} content={content.body} mime={content.mime_type} />
          ) : null}

          {!content.binary && content.body === "" && kind !== "image" ? (
            <Empty className="flex-1">
              <EmptyHeader>
                <EmptyTitle>空文件</EmptyTitle>
                <EmptyDescription>0 字节，没有可预览的内容。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : null}

          {!content.binary &&
          content.body !== "" &&
          kind !== "image" &&
          kind !== "html" &&
          kind !== "markdown" &&
          kind !== "table" &&
          kind !== "pdf" ? (
            <div className="min-h-0 min-w-0 flex-1 overflow-auto">
              <CodePreview path={content.path} content={content.body} language={language} />
            </div>
          ) : null}

          {kind === "pdf" ? (
            <BinaryCard
              path={content.path}
              size={content.size}
              mime={content.mime_type}
              reason="PDF 预览（pdfjs-dist）不在本切片内"
              downloadUrl={downloadUrl}
            />
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** Image branch: a truncated image cannot be zoomed meaningfully, so it downloads. */
function ImageSurface({ content, downloadUrl }: { content: FileContent; downloadUrl: string }) {
  if (content.truncated) {
    return (
      <BinaryCard
        path={content.path}
        size={content.size}
        mime={content.mime_type}
        reason="图片超过预览上限，只返回了片段"
        downloadUrl={downloadUrl}
      />
    );
  }

  const src = toImageSrc(content);
  if (!src) {
    return (
      <BinaryCard
        path={content.path}
        size={content.size}
        mime={content.mime_type}
        reason="接口返回的内容无法组成图片地址"
        downloadUrl={downloadUrl}
      />
    );
  }

  return <ImagePreview src={src} name={baseName(content.path)} />;
}
