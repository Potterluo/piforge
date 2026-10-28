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
import type { SearchResult } from "@/lib/types";

/**
 * Quick open (PRD FR-6.4, ⌘P / Ctrl+P): name search across the whole checkout.
 *
 * It is deliberately separate from the file tree's filter box. That one filters
 * the rows already loaded, which is honest but useless for a file you have never
 * expanded; this one asks the server to run a name search, so it finds a file
 * twenty directories deep without walking the tree.
 */
export function QuickOpen({
  sessionId,
  onOpenFile,
}: {
  sessionId: string;
  onOpenFile: (path: string) => void;
}): React.JSX.Element {
  const [open, setOpen] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const [results, setResults] = React.useState<SearchResult[]>([]);
  const [searching, setSearching] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() === "p" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        setOpen((current) => !current);
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

    // Debounced: a name search per keystroke would spawn one `rg` per character.
    const handle = window.setTimeout(() => {
      let cancelled = false;
      setSearching(true);
      api.files
        .search(sessionId, trimmed, false)
        .then((response) => {
          if (cancelled) return;
          setResults(response.results ?? []);
          setError(null);
        })
        .catch((cause: unknown) => {
          if (cancelled) return;
          setError(cause instanceof Error ? cause.message : "搜索失败");
        })
        .finally(() => {
          if (!cancelled) setSearching(false);
        });
      return () => {
        cancelled = true;
      };
    }, 180);

    return () => window.clearTimeout(handle);
  }, [open, query, sessionId]);

  const choose = React.useCallback(
    (path: string) => {
      onOpenFile(path);
      setOpen(false);
      setQuery("");
    },
    [onOpenFile],
  );

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        // Closing drops the query: reopening with last time's dead search is how a
        // palette looks broken.
        if (!next) {
          setQuery("");
          setResults([]);
          setError(null);
        }
      }}
    >
      <DialogContent
        className="top-24 translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-xl"
        showCloseButton={false}
      >
        <DialogHeader className="sr-only">
          <DialogTitle>快速打开文件</DialogTitle>
          <DialogDescription>按文件名搜索工作区里的文件</DialogDescription>
        </DialogHeader>
        <Command shouldFilter={false} className="rounded-none">
          <div className="flex items-center gap-2 border-b border-border px-3">
            <SearchIcon className="size-4 shrink-0 text-muted-foreground" />
            <CommandInput
              data-testid="quick-open-input"
              value={query}
              onValueChange={setQuery}
              placeholder="按文件名搜索…"
              className="h-11"
            />
          </div>
          <CommandList className="max-h-80">
            {error ? (
              <div className="px-3 py-6 text-xs text-destructive">{error}</div>
            ) : query.trim().length === 0 ? (
              <div className="px-3 py-6 text-xs text-muted-foreground">
                输入文件名开始搜索。{shortcutLabel()} 打开，Esc 关闭。
              </div>
            ) : !searching && results.length === 0 ? (
              <CommandEmpty>没有匹配的文件</CommandEmpty>
            ) : (
              <CommandGroup heading={searching ? "搜索中…" : `${results.length} 个结果`}>
                {results.map((result) => (
                  <CommandItem
                    key={result.path}
                    value={result.path}
                    data-testid="quick-open-result"
                    onSelect={() => choose(result.path)}
                    className="gap-2"
                  >
                    <FileTextIcon className="size-3.5 shrink-0 text-muted-foreground" />
                    <span className="truncate font-mono text-xs">{result.path}</span>
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