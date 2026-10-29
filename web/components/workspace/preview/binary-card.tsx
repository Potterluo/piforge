"use client";

import { DownloadIcon, FileArchiveIcon, FileTypeIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { baseName, formatBytes } from "@/lib/fs/file-kind";

/**
 * Download card for anything we cannot (or should not) render inline:
 * binaries, PDFs (pdfjs is not in this slice), images the API truncated, and
 * files above the preview size budget.
 */
export function BinaryCard({
  path,
  size,
  mime,
  reason,
  downloadUrl,
}: {
  path: string;
  size?: number;
  mime?: string;
  reason: string;
  downloadUrl: string;
}) {
  const isPdf = mime === "application/pdf" || path.toLowerCase().endsWith(".pdf");

  return (
    <div
      data-testid="file-preview-binary"
      className="flex min-h-0 min-w-0 flex-1 items-center justify-center p-4"
    >
      <Card className="w-full max-w-sm min-w-0">
        <CardHeader>
          <CardTitle className="flex min-w-0 items-center gap-2 text-sm">
            {isPdf ? (
              <FileTypeIcon className="size-4 shrink-0 text-muted-foreground" />
            ) : (
              <FileArchiveIcon className="size-4 shrink-0 text-muted-foreground" />
            )}
            <span className="min-w-0 truncate font-mono">{baseName(path)}</span>
          </CardTitle>
          <CardDescription className="min-w-0 text-xs break-all">{path}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <p className="text-xs text-muted-foreground">
            {reason} · {formatBytes(size)}
            {mime ? ` · ${mime}` : ""}
          </p>
          <Button
            variant="outline"
            size="sm"
            className="w-fit"
            data-testid="preview-binary-download"
            nativeButton={false}
            render={<a href={downloadUrl} download={baseName(path)} />}
          >
            <DownloadIcon />
            下载文件
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
