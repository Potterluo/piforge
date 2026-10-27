"use client";

import * as React from "react";
import { Tree, type NodeRendererProps } from "react-arborist";
import {
  CheckIcon,
  ChevronRightIcon,
  CopyIcon,
  DownloadIcon,
  MoreVerticalIcon,
  PencilIcon,
  TerminalIcon,
  Trash2Icon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { fileIconElement } from "@/components/workspace/file-icon";
import type { TreeItem } from "@/components/workspace/tree-model";

/**
 * The virtualised tree surface.
 *
 * Kept apart from `file-tree.tsx` because `react-arborist` drags in
 * `react-dnd-html5-backend`, which touches `window` at mount time; the parent
 * imports this module with `next/dynamic` + `ssr: false`.
 *
 * Row geometry is fixed by the design system: 28px rows (same as the git DAG),
 * 12px per indent level.
 */
const ROW_HEIGHT = 28;
const INDENT = 12;

/** Git chip colours. `!` is a conflict and deliberately reads as destructive. */
const GIT_CHIP: Record<string, string> = {
  M: "text-amber-600 dark:text-amber-400",
  A: "text-emerald-600 dark:text-emerald-400",
  D: "text-red-600 dark:text-red-400",
  U: "text-muted-foreground",
  "!": "text-destructive",
};

export interface FileTreeCanvasProps {
  items: TreeItem[];
  /** Filter mode renders a flat list of matching files instead of the tree. */
  flat?: boolean;
  gitMap: Map<string, string>;
  activePath?: string | null;
  onOpen: (path: string) => void;
  onToggleDir: (path: string, open: boolean) => void;
  onDelete: (item: { path: string; isDir: boolean }) => void;
  onDownload: (path: string) => void;
  onCopyPath: (path: string) => void;
  onRevealInTerminal: (path: string, isDir: boolean) => void;
}

interface RowContextValue extends Omit<FileTreeCanvasProps, "items"> {
  copiedPath: string | null;
}

const RowContext = React.createContext<RowContextValue | null>(null);

function useRowContext(): RowContextValue {
  const value = React.useContext(RowContext);
  if (!value) throw new Error("FileTreeRow must be rendered inside FileTreeCanvas");
  return value;
}

function FileTreeRow({ node, style }: NodeRendererProps<TreeItem>) {
  const ctx = useRowContext();
  const [menuOpen, setMenuOpen] = React.useState(false);
  const item = node.data;
  const git = ctx.gitMap.get(item.path);
  const copied = ctx.copiedPath === item.path;

  return (
    <div
      role="treeitem"
      aria-expanded={item.isDir ? node.isOpen : undefined}
      aria-selected={node.isSelected}
      data-testid="file-node"
      data-path={item.path}
      data-kind={item.isDir ? "dir" : "file"}
      style={{ ...style, height: ROW_HEIGHT }}
      onClick={(event) => {
        node.handleClick(event);
        // A single click opens files (IDE behaviour); a double click also
        // fires `onActivate`, which is deliberately a no-op for leaves.
        if (!item.isDir) ctx.onOpen(item.path);
      }}
      onContextMenu={(event) => {
        event.preventDefault();
        node.select();
        setMenuOpen(true);
      }}
      className={cn(
        "group/row flex min-w-0 cursor-default items-center gap-1 rounded-sm pr-1 text-xs outline-none select-none",
        "hover:bg-accent/60 focus-visible:ring-1 focus-visible:ring-ring",
        node.isSelected && "bg-accent text-accent-foreground",
        ctx.activePath === item.path && "text-foreground",
      )}
    >
      <span
        className="flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden"
        style={{ paddingLeft: node.level * INDENT + 2 }}
      >
        {node.isInternal ? (
          <button
            type="button"
            tabIndex={-1}
            aria-label={node.isOpen ? "折叠" : "展开"}
            onClick={(event) => {
              event.stopPropagation();
              node.toggle();
            }}
            className="flex size-4 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:text-foreground"
          >
            <ChevronRightIcon
              className={cn("size-3 transition-transform", node.isOpen && "rotate-90")}
            />
          </button>
        ) : (
          <span className="size-4 shrink-0" />
        )}

        {fileIconElement(
          item.path,
          item.isDir,
          node.isOpen,
          cn(
            "size-3.5 shrink-0",
            item.isDir ? "text-muted-foreground" : "text-muted-foreground/80",
          ),
        )}

        <Tooltip>
          <TooltipTrigger
            render={
              <span
                className={cn(
                  "min-w-0 truncate",
                  item.isDir && "font-medium",
                  ctx.activePath === item.path && "font-semibold",
                )}
              >
                {item.name}
              </span>
            }
          />
          <TooltipContent side="right">{item.path}</TooltipContent>
        </Tooltip>

        {git ? (
          <span
            className={cn("shrink-0 font-mono text-[10px] leading-none", GIT_CHIP[git] ?? "")}
            title={`git: ${git}`}
          >
            {git}
          </span>
        ) : null}

        {copied ? <CheckIcon className="size-3 shrink-0 text-emerald-500" /> : null}
      </span>

      <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label="文件操作"
              className={cn(
                "shrink-0 opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100",
                menuOpen && "opacity-100",
              )}
              onClick={(event: React.MouseEvent) => event.stopPropagation()}
            />
          }
        >
          <MoreVerticalIcon />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-44">
          <DropdownMenuItem
            onClick={() => {
              if (item.isDir) node.toggle();
              else ctx.onOpen(item.path);
            }}
          >
            <PencilIcon />
            打开
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => ctx.onCopyPath(item.path)}>
            <CopyIcon />
            复制路径
          </DropdownMenuItem>
          {!item.isDir ? (
            <DropdownMenuItem onClick={() => ctx.onDownload(item.path)}>
              <DownloadIcon />
              下载
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem onClick={() => ctx.onRevealInTerminal(item.path, item.isDir)}>
            <TerminalIcon />
            在终端中打开
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onClick={() => ctx.onDelete({ path: item.path, isDir: item.isDir })}
          >
            <Trash2Icon />
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

/**
 * Renders the arborist tree, or a flat match list when `flat` is set.
 *
 * A row size of 0 (hidden tab) is ignored instead of unmounting the tree, so
 * opening another tab does not collapse everything.
 */
export function FileTreeCanvas(props: FileTreeCanvasProps) {
  const containerRef = React.useRef<HTMLDivElement | null>(null);
  // A deliberate fallback instead of "wait for the first measurement": a
  // container that never reports a size (hidden tab, jsdom) would otherwise
  // render no rows at all. The ResizeObserver overwrites this on the first
  // real frame.
  const [size, setSize] = React.useState<{ width: number; height: number }>({
    width: 320,
    height: 480,
  });
  const [copiedPath, setCopiedPath] = React.useState<string | null>(null);
  const openRef = React.useRef(new Set<string>());

  React.useEffect(() => {
    const element = containerRef.current;
    if (!element) return;

    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect || rect.height < 2 || rect.width < 2) return;
      setSize({ width: Math.floor(rect.width), height: Math.floor(rect.height) });
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  React.useEffect(() => {
    if (!copiedPath) return;
    const timer = setTimeout(() => setCopiedPath(null), 1500);
    return () => clearTimeout(timer);
  }, [copiedPath]);

  const context: RowContextValue = {
    flat: props.flat,
    gitMap: props.gitMap,
    activePath: props.activePath,
    onOpen: props.onOpen,
    onToggleDir: props.onToggleDir,
    onDelete: props.onDelete,
    onDownload: props.onDownload,
    onCopyPath: (path: string) => {
      setCopiedPath(path);
      props.onCopyPath(path);
    },
    onRevealInTerminal: props.onRevealInTerminal,
    copiedPath,
  };

  const handleToggle = React.useCallback(
    (id: string) => {
      const open = openRef.current.has(id);
      if (open) openRef.current.delete(id);
      else openRef.current.add(id);
      props.onToggleDir(id, !open);
    },
    [props],
  );

  return (
    <RowContext.Provider value={context}>
      <div ref={containerRef} className="min-h-0 min-w-0 flex-1" data-testid="file-tree-canvas">
        <Tree<TreeItem>
          data={props.items}
          idAccessor="id"
          childrenAccessor={(item: TreeItem) => (item.isDir ? (item.children ?? []) : null)}
          width={size.width}
          height={size.height}
          rowHeight={ROW_HEIGHT}
          indent={0}
          // Directories start open. The first request already returns two
          // levels, so this shows the shape of the repo immediately instead of
          // a single closed folder; deeper levels are still lazy and load on
          // demand. Clicking a directory row collapses it again.
          openByDefault
          disableDrag
          disableDrop
          disableEdit
          disableMultiSelection
          selection={props.activePath ?? undefined}
          onActivate={(node) => {
            // Clicking a directory's name should open it. Only the chevron
            // toggles by default, which makes the tree feel dead: the user
            // clicks the row, nothing happens, and lazy children never load.
            if (node.data.isDir) {
              node.toggle();
              return;
            }
            props.onOpen(node.data.path);
          }}
          onToggle={handleToggle}
          aria-label="文件树"
        >
          {FileTreeRow}
        </Tree>
      </div>
    </RowContext.Provider>
  );
}

export { ROW_HEIGHT as FILE_TREE_ROW_HEIGHT };
