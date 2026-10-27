"use client";

import { CodeBlock, CodeBlockContent } from "@/components/ui/code-block";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { MarkdownLite } from "@/components/workspace/preview/markdown-lite";

/** Markdown preview with a rendered / source toggle (PRD §10.4.3). */
export function MarkdownPreview({ content }: { content: string }) {
  return (
    <Tabs defaultValue="rendered" className="flex min-h-0 min-w-0 flex-1 flex-col gap-0">
      <div className="shrink-0 border-b px-2 py-1">
        <TabsList className="h-7">
          <TabsTrigger value="rendered" className="text-xs">
            渲染
          </TabsTrigger>
          <TabsTrigger value="source" className="text-xs">
            源码
          </TabsTrigger>
        </TabsList>
      </div>

      <TabsContent value="rendered" className="min-h-0 min-w-0 overflow-auto p-3">
        <MarkdownLite source={content} />
      </TabsContent>

      <TabsContent value="source" className="min-h-0 min-w-0 overflow-auto">
        <CodeBlock className="rounded-none border-0">
          <CodeBlockContent lineNumbers>{content}</CodeBlockContent>
        </CodeBlock>
      </TabsContent>
    </Tabs>
  );
}
