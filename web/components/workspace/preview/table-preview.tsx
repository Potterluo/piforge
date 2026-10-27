"use client";

import * as React from "react";

import { CodeBlock, CodeBlockContent } from "@/components/ui/code-block";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { delimiterFor, parseDelimited } from "@/lib/fs/csv";

const MAX_ROWS = 200;
const MAX_COLUMNS = 12;

/** CSV/TSV table preview with a raw-text toggle (PRD §10.4.3). */
export function TablePreview({ path, content, mime }: { path: string; content: string; mime?: string }) {
  const parsed = React.useMemo(
    () => parseDelimited(content, delimiterFor(path, mime), MAX_ROWS, MAX_COLUMNS),
    [content, path, mime],
  );

  return (
    <Tabs defaultValue="table" className="flex min-h-0 min-w-0 flex-1 flex-col gap-0">
      <div className="flex shrink-0 items-center gap-2 border-b px-2 py-1">
        <TabsList className="h-7">
          <TabsTrigger value="table" className="text-xs">
            表格
          </TabsTrigger>
          <TabsTrigger value="raw" className="text-xs">
            原文
          </TabsTrigger>
        </TabsList>
        <span className="ms-auto truncate text-[10px] text-muted-foreground">
          {parsed.totalRows} 行
          {parsed.clipped ? `（仅显示前 ${MAX_ROWS} 行 / ${MAX_COLUMNS} 列）` : ""}
        </span>
      </div>

      <TabsContent value="table" className="min-h-0 min-w-0 overflow-auto">
        {parsed.header.length === 0 ? (
          <p className="p-3 text-xs text-muted-foreground">无法解析出表头。</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8 text-[10px] text-muted-foreground">#</TableHead>
                {parsed.header.map((cell, index) => (
                  <TableHead key={index} className="max-w-40 truncate text-xs">
                    {cell}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {parsed.rows.map((row, rowIndex) => (
                <TableRow key={rowIndex}>
                  <TableCell className="font-mono text-[10px] text-muted-foreground">
                    {rowIndex + 1}
                  </TableCell>
                  {parsed.header.map((_, cellIndex) => (
                    <TableCell
                      key={cellIndex}
                      className="max-w-40 truncate font-mono text-[11px]"
                      title={row[cellIndex] ?? ""}
                    >
                      {row[cellIndex] ?? ""}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </TabsContent>

      <TabsContent value="raw" className="min-h-0 min-w-0 overflow-auto">
        <CodeBlock className="rounded-none border-0">
          <CodeBlockContent lineNumbers>{content}</CodeBlockContent>
        </CodeBlock>
      </TabsContent>
    </Tabs>
  );
}
