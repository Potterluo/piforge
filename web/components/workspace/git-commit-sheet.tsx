"use client";

import { GitCommitHorizontalIcon, InfoIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Marker, MarkerContent, MarkerIcon } from "@/components/ui/marker";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { absoluteTime, relativeTime } from "@/lib/git/relative-time";
import type { GitCommit } from "@/lib/types";

/**
 * Commit details drawer.
 *
 * Honest limitation: the current API client exposes `git.diff(sessionId,
 * scope, path)` with no commit parameter, so there is no way to ask the backend
 * "diff of commit X". Rather than invent a request (or silently show HEAD's
 * diff, which would be wrong), the drawer shows the full metadata and says what
 * is missing. Adding `?scope=commit&commit=<hash>` on the backend and a fourth
 * argument on the client is all this needs.
 */
export function GitCommitSheet({
  commit,
  open,
  onOpenChange,
  onSelectParent,
}: {
  commit: GitCommit | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSelectParent?: (hash: string) => void;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="flex min-w-0 items-center gap-1.5 font-mono text-sm">
            <GitCommitHorizontalIcon className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 truncate">{commit?.short ?? "commit"}</span>
          </SheetTitle>
          <SheetDescription className="break-all font-mono text-[10px]">
            {commit?.hash ?? ""}
          </SheetDescription>
        </SheetHeader>

        {commit ? (
          <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto px-4 pb-4">
            <p className="text-sm break-words">{commit.subject}</p>

            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[11px]">
              <dt className="text-muted-foreground">作者</dt>
              <dd className="min-w-0 truncate">
                {commit.author}
                {commit.email ? (
                  <span className="text-muted-foreground"> &lt;{commit.email}&gt;</span>
                ) : null}
              </dd>
              <dt className="text-muted-foreground">时间</dt>
              <dd className="min-w-0">
                {absoluteTime(commit.ts)}
                <span className="text-muted-foreground"> · {relativeTime(commit.ts)}</span>
              </dd>
              <dt className="text-muted-foreground">父提交</dt>
              <dd className="flex min-w-0 flex-wrap gap-1">
                {commit.parents.length === 0 ? (
                  <span className="text-muted-foreground">（根提交）</span>
                ) : (
                  commit.parents.map((parent) => (
                    <button
                      key={parent}
                      type="button"
                      onClick={() => onSelectParent?.(parent)}
                      className="font-mono text-[10px] underline underline-offset-2 hover:text-foreground"
                      title={parent}
                    >
                      {parent.slice(0, 7)}
                    </button>
                  ))
                )}
              </dd>
            </dl>

            {commit.refs && commit.refs.length > 0 ? (
              <div className="flex flex-wrap gap-1">
                {commit.refs.map((ref) => (
                  <Badge
                    key={`${ref.type}:${ref.name}`}
                    variant={ref.type === "head" ? "default" : ref.type === "branch" ? "secondary" : "outline"}
                    className="text-[10px]"
                  >
                    {ref.name}
                  </Badge>
                ))}
              </div>
            ) : null}

            <Marker variant="border">
              <MarkerIcon>
                <InfoIcon />
              </MarkerIcon>
              <MarkerContent className="break-words">
                还没有单提交 diff 接口。需要后端支持
                <code className="mx-1 rounded bg-muted px-1 font-mono text-[10px]">
                  GET /v1/sessions/{"{id}"}/git/diff?scope=commit&commit={commit.short}
                </code>
                以及客户端多传一个 commit 参数；在那之前这里只展示元数据，不假装能看 diff。
              </MarkerContent>
            </Marker>
          </div>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
