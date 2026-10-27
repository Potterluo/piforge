"use client";

import { ShieldAlertIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

/**
 * HTML preview in a fully sandboxed iframe.
 *
 * `sandbox=""` disables scripts, forms, popups and same-origin access: the
 * document is repo content, i.e. untrusted input, and the PRD's "preview
 * domain" escape hatch does not exist yet.
 */
export function HtmlPreview({ content, path }: { content: string; path: string }) {
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2 p-2">
      <div className="flex shrink-0 flex-wrap items-center gap-1.5">
        <Badge variant="outline" className="gap-1 text-[10px]">
          <ShieldAlertIcon className="size-3" />
          脚本已禁用
        </Badge>
        <span className="min-w-0 truncate font-mono text-[10px] text-muted-foreground">{path}</span>
      </div>

      <div className="min-h-0 min-w-0 flex-1 overflow-hidden rounded-md border bg-white">
        <iframe
          title={`HTML 预览: ${path}`}
          sandbox=""
          srcDoc={content}
          className="size-full border-0"
        />
      </div>
    </div>
  );
}
