"use client";

import Link from "next/link";
import { useState } from "react";
import { FolderGit2, Plus } from "lucide-react";

import { NewProjectDialog } from "@/components/shell/new-project-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { humanizeError } from "@/lib/errors";
import { formatRelative, sandboxLabel } from "@/lib/session/format";
import { useProjects } from "@/lib/session/queries";

/**
 * Project grid — the home page of the product (PRD §10.2).
 *
 * Landing on a project automatically would hide the fact that there is more
 * than one, so this page stays a chooser instead of a redirect.
 */
export function HomePage() {
  const projects = useProjects();
  const [dialogOpen, setDialogOpen] = useState(false);
  const list = projects.data?.projects ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <header className="flex flex-wrap items-start gap-3 border-b border-border p-4">
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className="text-base font-medium">项目</h1>
          <p className="max-w-2xl text-xs text-muted-foreground">
            每个项目对应一个 git 仓库和一个专属沙箱。打开项目继续工作，或者新建一个。
          </p>
        </div>
        <Button size="sm" className="ml-auto" data-testid="new-project-button" onClick={() => setDialogOpen(true)}>
          <Plus />
          新建项目
        </Button>
      </header>

      <div className="p-4">
        {projects.isPending ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            <Skeleton className="h-32 w-full" />
            <Skeleton className="h-32 w-full" />
            <Skeleton className="h-32 w-full" />
          </div>
        ) : projects.isError ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            项目列表加载失败：{humanizeError(projects.error)}
            <Button size="sm" variant="outline" onClick={() => void projects.refetch()}>
              重试
            </Button>
          </div>
        ) : list.length === 0 ? (
          <Empty className="border border-dashed border-border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <FolderGit2 />
              </EmptyMedia>
              <EmptyTitle>还没有项目</EmptyTitle>
              <EmptyDescription>
                项目对应一个 git 仓库。创建一个之后，就能在专用沙箱里开会话、让 agent 改代码、看 diff。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              {/* Distinct id: the header CTA has the canonical one, so tests
                  and the command palette can address it unambiguously. */}
              <Button data-testid="new-project-button-empty" onClick={() => setDialogOpen(true)}>
                <Plus />
                新建项目
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {list.map((project) => (
              <Link
                key={project.id}
                href={`/p/${project.id}`}
                data-testid="project-card"
                className="rounded-xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
              >
                <Card className="h-full transition-colors hover:bg-muted/40">
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2 text-sm">
                      <FolderGit2 className="size-4 text-muted-foreground" />
                      <span className="truncate">{project.name}</span>
                    </CardTitle>
                    <CardDescription className="truncate font-mono text-xs">
                      {project.repo_url || "空白工作区"}
                    </CardDescription>
                  </CardHeader>
                  <CardContent className="flex flex-wrap items-center gap-1.5">
                    <Badge variant="outline">{project.default_branch}</Badge>
                    <Badge variant="secondary">沙箱 · {sandboxLabel(project.sandbox_state)}</Badge>
                    <Badge variant="outline">{project.session_count} 个会话</Badge>
                    {project.last_active_at ? (
                      <span className="ml-auto text-[11px] text-muted-foreground">
                        {formatRelative(project.last_active_at)}
                      </span>
                    ) : null}
                  </CardContent>
                </Card>
              </Link>
            ))}
          </div>
        )}
      </div>

      <NewProjectDialog open={dialogOpen} onOpenChange={setDialogOpen} />
    </div>
  );
}
