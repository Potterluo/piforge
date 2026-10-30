"use client";

import * as React from "react";
import { FileTextIcon, SearchIcon } from "lucide-react";

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api/client";
import { humanizeError } from "@/lib/errors";
import { cn } from "@/lib/utils";
import type { SearchResult } from "@/lib/types";

/**
 * Quick open (PRD FR-6.4 ⌘P / FR-6.6 ⌘⇧F).
 *
 * Two searches, one surface, because they answer the same question — "where is
 * the thing" — and two palettes with two shortcuts would make the user remember
 * which one they are in. ⌘P searches names; ⌘⇧F searches file contents and each
 * result carries the line it matched on, so clicking it opens the file *there*.
 *
 * It is deliberately separate from the file tree's filter box. That one filters
 * the rows already loaded, which is honest but useless for a file you have never
 * expanded; this one asks the server to run a search, so it finds a file twenty
 * directories deep without walking the tree.
 */
type Mode = "name" | "content";

export function QuickOpen({
  sessionId,
  onOpenFile,
}: {
  sessionId: string;
  /** `line` is set by a content hit and is what makes it a jump rather than an open. */
  onOpenFile: (path: string, line?: number) => void;
}): React.JSX.Element {
  const [open, setOpen] = React.useState(false);
  const [mode, setMode] = React.useState<Mode>("name");
  const [query, setQuery] = React.useState("");
  const [results, setResults] = React.useState<SearchResult[]>([]);
  const [searching, setSearching] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const mod = event.metaKey || event.ctrlKey;
      if (!mod) return;
      const key = event.key.toLowerCase();
      // ⌘P is names. Shift is excluded so ⌘⇧P does not silently land here.
      if (key === "p" && !event.shiftKey) {
        event.preventDefault();
        setMode("name");
        setOpen((current) => !current);
        return;
      }
      // ⌘⇧F is content (FR-6.6). It opens rather than toggles: the browser's own
      // find is not what this shortcut means in this app.
      if (key === "f" && event.shiftKey) {
        event.preventDefault();
        setMode("content");
        setOpen(true);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  React.useEffect(() => {
    if (!open) return;
    const trimmed = query.trim();
    if (trimmed.length === 0) {
      setResults([]);
      setError(null);
      return;
    }

    // Debounced: one search per keystroke would spawn one `rg` per character.
    const handle = window.setTimeout(() => {
      let cancelled = false;
      setSearching(true);
      api.files
        .search(sessionId, trimmed, mode === "content")
        .then((response) => {
          if (cancelled) return;
          setResults(response.results ?? []);
          setError(null);
        })
        .catch((cause: unknown) => {
          if (cancelled) return;
          setError(humanizeError(cause, "搜索失败"));
        })
        .finally(() => {
          if (!cancelled) setSearching(false);
        });
      return () => {
        cancelled = true;
      };
    }, 180);

    return () => window.clearTimeout(handle);
  }, [open, query, mode, sessionId]);

  const choose = React.useCallback(
    (result: SearchResult) => {
      onOpenFile(result.path, result.line);
      setOpen(false);
      setQuery("");
    },
    [onOpenFile],
  );

  const close = React.useCallback(() => {
    setOpen(false);
    // Closing drops the query: reopening with last time's dead search is how a
    // palette looks broken.
    setQuery("");
    setResults([]);
    setError(null);
  }, []);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) setOpen(true);
        else close();
      }}
    >
      <DialogContent
        className="top-24 translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-xl"
        showCloseButton={false}
      >
        <DialogHeader className="sr-only">
          <DialogTitle>{mode === "content" ? "搜索文件内容" : "快速打开文件"}</DialogTitle>
          <DialogDescription>
            {mode === "content"
              ? "按文件内容搜索工作区，结果可点击跳到对应行"
              : "按文件名搜索工作区里的文件"}
          </DialogDescription>
        </DialogHeader>
        <Command shouldFilter={false} className="rounded-none">
          <div className="flex items-center gap-2 border-b border-border px-3">
            <SearchIcon className="size-4 shrink-0 text-muted-foreground" />
            <CommandInput
              data-testid="quick-open-input"
              value={query}
              onValueChange={setQuery}
              placeholder={mode === "content" ? "按内容搜索（⌘⇧F / Ctrl+Shift+F）…" : "按文件名搜索…"}
              className="h-11"
            />
            <span
              data-testid="quick-open-mode"
              data-mode={mode}
              className={cn(
                "shrink-0 rounded border px-1.5 py-0.5 font-mono text-[10px]",
                mode === "content"
                  ? "border-amber-500/40 text-amber-600 dark:text-amber-400"
                  : "text-muted-foreground",
              )}
            >
              {mode === "content" ? "内容" : "文件名"}
            </span>
          </div>
          <CommandList className="max-h-80">
            {error ? (
              <div className="px-3 py-6 text-xs text-destructive">{error}</div>
            ) : query.trim().length === 0 ? (
              <div className="px-3 py-6 text-xs text-muted-foreground">
                {mode === "content"
                  ? `输入要查找的文本，例如函数名或报错原句。${contentShortcutLabel()} 打开，Esc 关闭。`
                  : `输入文件名开始搜索。${shortcutLabel()} 打开，Esc 关闭。`}
              </div>
            ) : !searching && results.length === 0 ? (
              <CommandEmpty>{mode === "content" ? "没有匹配的内容" : "没有匹配的文件"}</CommandEmpty>
            ) : (
              <CommandGroup
                heading={
                  searching
                    ? "搜索中…"
                    : `${results.length} 个结果${mode === "content" ? "（按行）" : ""}`
                }
              >
                {results.slice(0, 100).map((result) => (
                  <CommandItem
                    key={`${result.path}:${result.line ?? 0}`}
                    value={`${result.path}:${result.line ?? 0}`}
                    data-testid="quick-open-result"
                    data-path={result.path}
                    data-line={result.line ?? undefined}
                    onSelect={() => choose(result)}
                    className="items-start gap-2"
                  >
                    <FileTextIcon className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
                    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span className="truncate font-mono text-xs">
                        {result.path}
                        {mode === "content" && result.line ? (
                          <span className="text-muted-foreground">:{result.line}</span>
                        ) : null}
                      </span>
                      {mode === "content" && result.text ? (
                        <span className="truncate font-mono text-[11px] text-muted-foreground">
                          {result.text.trim()}
                        </span>
                      ) : null}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
/** The shortcut as this platform spells it. */
function shortcutLabel(): string {
  if (typeof navigator === "undefined") return "⌘P";
  return /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? "⌘P" : "Ctrl+P";
}

function contentShortcutLabel(): string {
  if (typeof navigator === "undefined") return "⌘⇧F";
  return /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
    ? "⌘⇧F"
    : "Ctrl+Shift+F";
}
