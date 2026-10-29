"use client";

import * as React from "react";
import {
  DownloadIcon,
  FileTextIcon,
  Pencil as PencilIcon,
  XIcon,
} from "lucide-react";

import {
  Badge } from "@/components/ui/badge";
import {
  Button } from "@/components/ui/button";
import {
  Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import api from "@/lib/api/client";
import {
  type FileKind,
  baseName,
  formatBytes,
  imageMimeOf,
  kindOf,
  languageOf,
} from "@/lib/fs/file-kind";
import type { FileContent } from "@/lib/types";
import {
  PanelError, PanelSkeleton } from "@/components/workspace/panel-state";
import {
  useAsync } from "@/components/workspace/use-async";
import {
  BinaryCard } from "@/components/workspace/preview/binary-card";
import {
  CodePreview } from "@/components/workspace/preview/code-preview";
import {
  HtmlPreview } from "@/components/workspace/preview/html-preview";
import {
  ImagePreview } from "@/components/workspace/preview/image-preview";
import {
  MarkdownPreview } from "@/components/workspace/preview/markdown-preview";
import {
  TablePreview } from "@/components/workspace/preview/table-preview";

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
  // Editing is a plain textarea on purpose: this is a coding tool whose real editor
  // is the agent, and a half-wired Monaco would be a bigger promise than it keeps.
  // What matters is that a one-line fix does not require leaving the page (FR-7.1).
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [saveError, setSaveError] = React.useState<string | null>(null);

  React.useEffect(() => {
    // Changing file while editing would silently throw the draft away.
    setEditing(false);
    setDraft("");
    setSaveError(null);
  }, [path]);


  const file = useAsync(path ? `file:${sessionId}:${path}` : "file:none", async () => {
    if (!path) throw new Error("未选择文件");
    return api.files.read(sessionId, path);
  }, { enabled: Boolean(path) });

  const save = React.useCallback(async () => {
    if (!path) return;
    setSaving(true);
    setSaveError(null);
    try {
      await api.files.write(sessionId, path, draft);
      setEditing(false);
      setDraft("");
      file.reload();
    } catch (error) {
      setSaveError(error instanceof Error ? error.message : "保存失败");
    } finally {
      setSaving(false);
    }
  }, [draft, file, path, sessionId]);

  const inlineUrl = React.useMemo(
    () => (path ? api.files.inlineUrl(sessionId, path) : ""),
    [path, sessionId],
  );
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
        {content && EDITABLE.has(kind) && !content.truncated ? (
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={editing ? "取消编辑" : "编辑"}
            data-testid="preview-edit"
            onClick={() => {
              if (editing) {
                setEditing(false);
                setDraft("");
                return;
              }
              setDraft(content.body);
              setEditing(true);
            }}
          >
            {editing ? <XIcon /> : <PencilIcon />}
          </Button>
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

      {content && editing ? (
        <div className="flex min-h-0 flex-1 flex-col" data-testid="file-editor">
          <textarea
            data-testid="file-editor-input"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            spellCheck={false}
            className="min-h-0 flex-1 resize-none bg-background p-3 font-mono text-[12px] leading-relaxed outline-none"
          />
          <div className="flex shrink-0 items-center justify-end gap-2 border-t px-2 py-1.5">
            {saveError ? (
              <span className="me-auto truncate text-[11px] text-destructive">{saveError}</span>
            ) : null}
            <Button
              size="xs"
              variant="ghost"
              data-testid="file-editor-cancel"
              onClick={() => {
                setEditing(false);
                setDraft("");
              }}
            >
              取消
            </Button>
            <Button size="xs" data-testid="file-editor-save" disabled={saving} onClick={() => void save()}>
              {saving ? "保存中…" : "保存"}
            </Button>
          </div>
        </div>
      ) : null}

      {content && !editing ? (
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
            <div data-testid="preview-binary" className="flex min-h-0 min-w-0 flex-1 flex-col">
              <BinaryCard
                path={content.path}
                size={content.size}
                mime={content.mime_type}
                reason="二进制文件，不可预览"
                downloadUrl={downloadUrl}
              />
            </div>
          ) : null}

          {kind === "image" ? (
            <ImageSurface content={content} downloadUrl={downloadUrl} inlineUrl={inlineUrl} />
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

          {kind === "pdf" && !content.truncated ? (
            // No pdfjs: every browser worth targeting renders PDFs natively, and a
            // sandboxed iframe of the download URL is both smaller and more correct
            // than a hand-rolled canvas viewer.
            <div className="min-h-0 min-w-0 flex-1" data-testid="pdf-preview">
              <iframe
                src={inlineUrl}
                title={content.path}
                className="h-full w-full border-0 bg-background"
                sandbox="allow-same-origin allow-scripts"
              />
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Image branch.
 *
 * Images come back from the file API as `binary: true` with an empty body — the
 * bytes are only available from the inline endpoint. Building a data URL from the
 * body therefore always failed, which is why a captured PNG showed "接口返回的内容无法
 * 组成图片地址" instead of the picture.
 */
function ImageSurface({
  content,
  downloadUrl,
  inlineUrl,
}: {
  content: FileContent;
  downloadUrl: string;
  inlineUrl: string;
}) {
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

  // Prefer the bytes the API actually holds; fall back to the inline endpoint,
  // which streams the whole file with a renderable content type.
  const src = toImageSrc(content) || inlineUrl;
  if (!src) {
    return (
      <BinaryCard
        path={content.path}
        size={content.size}
        mime={content.mime_type}
        reason="接口没有返回可用的图片地址"
        downloadUrl={downloadUrl}
      />
    );
  }

  return <ImagePreview src={src} name={baseName(content.path)} />;
}
/**
 * Kinds that are text under the hood. A `.txt` reports "text" and a `.ts` reports
 * "code", and both are one keystroke away from a fix — excluding either would have made
 * the edit button depend on the file extension in a way nobody could predict.
 */
const EDITABLE = new Set<FileKind>(["text", "code", "markdown", "html", "table"]);