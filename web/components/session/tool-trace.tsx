"use client";

import { useState } from "react";
import {
  Bot,
  FileSearch,
  FileText,
  FolderSearch,
  Globe,
  ListChecks,
  Pencil,
  Search,
  Terminal,
  Wrench,
} from "lucide-react";

import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/components/ui/code-block";
import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceDescription,
  TraceIcon,
  TraceLine,
  TraceTitle,
} from "@/components/ui/trace";
import { formatDuration, truncate } from "@/lib/session/format";
import type { Block } from "@/lib/session/reduce";
import { cn } from "@/lib/utils";

type ToolBlock = Extract<Block, { kind: "tool" }>;

const TOOL_META: Record<string, { label: string; icon: React.ComponentType<{ className?: string }> }> = {
  bash: { label: "执行命令", icon: Terminal },
  shell: { label: "执行命令", icon: Terminal },
  read: { label: "读取文件", icon: FileText },
  view: { label: "查看文件", icon: FileText },
  write: { label: "写入文件", icon: Pencil },
  edit: { label: "修改文件", icon: Pencil },
  grep: { label: "搜索内容", icon: Search },
  glob: { label: "查找文件", icon: FolderSearch },
  search: { label: "搜索", icon: FileSearch },
  webfetch: { label: "访问网页", icon: Globe },
  web_fetch: { label: "访问网页", icon: Globe },
  browser: { label: "浏览器", icon: Globe },
  todo: { label: "更新计划", icon: ListChecks },
  task: { label: "子任务", icon: Bot },
  agent: { label: "子任务", icon: Bot },
};

const ARG_KEYS = [
  "command",
  "cmd",
  "path",
  "file_path",
  "query",
  "pattern",
  "url",
  "description",
  "prompt",
  "label",
];

/**
 * The collapsed row has to answer "what is it doing to what" without a click:
 * tool label, the first useful argument, duration and status. This is the row
 * users stare at while a long command runs (PRD §10.4.1).
 */
export function summarizeToolArgs(args: unknown): string {
  if (!args || typeof args !== "object") return "";
  const record = args as Record<string, unknown>;

  for (const key of ARG_KEYS) {
    const value = record[key];
    if (typeof value === "string" && value.trim()) return truncate(value, 88);
  }

  const keys = Object.keys(record);
  if (keys.length === 0) return "";
  try {
    return truncate(JSON.stringify(record), 88);
  } catch {
    return "";
  }
}

function toolMeta(name: string) {
  return TOOL_META[name.toLowerCase()] ?? { label: name || "工具", icon: Wrench };
}

export function ToolTrace({ block }: { block: ToolBlock }) {
  const [open, setOpen] = useState(false);
  const meta = toolMeta(block.name);
  const summary = summarizeToolArgs(block.args);
  const Icon = meta.icon;
  const status = block.done ? (block.isError ? "error" : undefined) : "running";
  const statusLabel = block.done ? (block.isError ? "失败" : "完成") : "运行中";
  const argsText = (() => {
    if (!block.args || typeof block.args !== "object") return "";
    const keys = Object.keys(block.args as Record<string, unknown>);
    if (keys.length === 0) return "";
    try {
      return JSON.stringify(block.args, null, 2);
    } catch {
      return "";
    }
  })();

  return (
    <Trace>
      <TraceCollapsible open={open} onOpenChange={setOpen}>
        <TraceCollapsibleTrigger data-testid="tool-trace">
          <TraceLine status={status}>
            <TraceIcon>
              <Icon />
            </TraceIcon>
            <TraceTitle className="font-medium">{meta.label}</TraceTitle>
            {summary ? (
              <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">
                {summary}
              </span>
            ) : null}
            {block.durationMs !== undefined ? (
              <TraceDescription>{formatDuration(block.durationMs)}</TraceDescription>
            ) : null}
            <span
              className={cn(
                "shrink-0 text-[11px]",
                block.isError ? "text-destructive" : "text-muted-foreground/70",
              )}
            >
              {statusLabel}
            </span>
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent>
          <div className="flex flex-col gap-2">
            <CodeBlock
              size="sm"
              variant={block.isError ? "destructive" : "default"}
              data-testid="tool-output"
            >
              <CodeBlockHeader>
                <CodeBlockTitle>
                  {meta.label}
                  <span className="ml-1.5 font-mono text-xs font-normal text-muted-foreground">
                    {block.name}
                  </span>
                </CodeBlockTitle>
                <CodeBlockActions>
                  <CodeBlockCopyButton value={block.chunk} disabled={block.chunk.length === 0} />
                </CodeBlockActions>
              </CodeBlockHeader>
              <CodeBlockContent wrap>
                {block.chunk.length > 0
                  ? block.chunk
                  : block.done
                    ? "（没有输出）"
                    : "等待输出…"}
              </CodeBlockContent>
            </CodeBlock>
            {argsText ? (
              <p className="font-mono text-[11px] break-all whitespace-pre-wrap text-muted-foreground">
                {argsText}
              </p>
            ) : null}
          </div>
        </TraceCollapsibleContent>
      </TraceCollapsible>
    </Trace>
  );
}
/**
 * One line for a whole group of tool calls.
 *
 * The transcript is for reading what was said; a run that shells out twelve times put
 * twelve rows of machinery in the middle of it. The calls still exist and still open,
 * but they are summarised into one row, and the detail lives behind it.
 */
export function ToolGroup({ blocks }: { blocks: ToolBlock[] }) {
  const [open, setOpen] = useState(false);
  if (blocks.length === 0) return null;

  const failed = blocks.filter((block) => block.isError).length;
  const running = blocks.filter((block) => !block.done).length;
  const totalMs = blocks.reduce((sum, block) => sum + (block.durationMs ?? 0), 0);
  const names = Array.from(new Set(blocks.map((block) => toolMeta(block.name).label)));
  const label = names.length <= 2 ? names.join("、") : `${names[0]} 等 ${names.length} 类`;

  return (
    <Trace>
      <TraceCollapsible open={open} onOpenChange={setOpen}>
        <TraceCollapsibleTrigger data-testid="tool-group">
          <TraceLine status={running > 0 ? "running" : failed > 0 ? "error" : undefined}>
            <TraceIcon>
              <Wrench />
            </TraceIcon>
            <TraceTitle className="font-medium">{blocks.length} 个工具调用</TraceTitle>
            <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">{label}</span>
            {failed > 0 ? (
              <span className="shrink-0 text-[11px] text-destructive">{failed} 个失败</span>
            ) : null}
            {running > 0 ? (
              <span className="shrink-0 text-[11px] text-muted-foreground">{running} 个运行中</span>
            ) : null}
            {totalMs > 0 ? <TraceDescription>{formatDuration(totalMs)}</TraceDescription> : null}
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent>
          <div className="flex flex-col gap-1.5">
            {blocks.map((block) => (
              <ToolTrace key={block.id} block={block} />
            ))}
          </div>
        </TraceCollapsibleContent>
      </TraceCollapsible>
    </Trace>
  );
}