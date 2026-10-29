"use client";

import { usePathname, useRouter } from "next/navigation";
import { Suspense, useEffect, useState, type ReactNode } from "react";

import { AppSidebar } from "@/components/shell/app-sidebar";
import { CommandPalette } from "@/components/shell/command-palette";
import { NewProjectDialog } from "@/components/shell/new-project-dialog";
import { NewSessionDialog } from "@/components/shell/new-session-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { SidebarInset, SidebarProvider, SidebarTrigger, useSidebar } from "@/components/ui/sidebar";
import { Skeleton } from "@/components/ui/skeleton";
import { useCurrentUser } from "@/lib/session/queries";
import { ApiError } from "@/lib/types";

function projectIdFromPath(pathname: string): string | null {
  const match = /^\/p\/([^/]+)/.exec(pathname);
  return match?.[1] ?? null;
}

function ShellSkeleton() {
  return (
    <div className="flex h-svh w-full overflow-hidden">
      <div className="hidden w-64 shrink-0 flex-col gap-3 border-r p-3 md:flex">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-6 w-3/4" />
        <Skeleton className="h-6 w-2/3" />
        <Skeleton className="h-6 w-3/4" />
      </div>
      <div className="flex flex-1 items-center justify-center">
        <Skeleton className="h-8 w-48" />
      </div>
    </div>
  );
}

function AppShellFrame({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const me = useCurrentUser();

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [newSessionOpen, setNewSessionOpen] = useState(false);

  const projectId = projectIdFromPath(pathname);

  useEffect(() => {
    if (me.error instanceof ApiError && me.error.status === 401) {
      router.replace("/login");
    }
  }, [me.error, router]);

  // One global listener for the app, owned by the one component that is always
  // mounted. Registering it inside the palette would re-register per route.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        setPaletteOpen((open) => !open);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  if (me.isPending) return <ShellSkeleton />;

  if (me.error) {
    if (me.error instanceof ApiError && me.error.status === 401) {
      return (
        <div className="flex min-h-svh items-center justify-center text-sm text-muted-foreground">
          正在跳转到登录…
        </div>
      );
    }
    return (
      <div className="flex min-h-svh items-center justify-center p-6">
        <Card className="w-full max-w-md">
          <CardHeader>
            <CardTitle>无法连接后端</CardTitle>
            <CardDescription>
              PiForge 需要本地控制面在运行。确认后端已启动后重试。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <p className="text-xs text-muted-foreground">{me.error.message}</p>
            <Button variant="outline" onClick={() => void me.refetch()}>
              重试
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    // h-svh and overflow-hidden are load-bearing: with `min-h-svh` the provider grew with
    // the page, which pushed the sidebar footer — the account menu — below the fold and
    // out of reach.
    <SidebarProvider className="h-svh overflow-hidden">
      <AppSidebar
        onNewProject={() => setNewProjectOpen(true)}
        onNewSession={() => setNewSessionOpen(true)}
      />
      <SidebarInset className="flex h-svh min-h-0 flex-1 flex-col overflow-hidden">
        <ShellTopBar />
        {children}
      </SidebarInset>

      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        onNewSession={() => setNewSessionOpen(true)}
        onNewProject={() => setNewProjectOpen(true)}
      />
      <NewProjectDialog open={newProjectOpen} onOpenChange={setNewProjectOpen} />
      <NewSessionDialog
        open={newSessionOpen}
        onOpenChange={setNewSessionOpen}
        projectId={projectId}
      />
    </SidebarProvider>
  );
}

/**
 * The authenticated frame: sidebar + inset + global shortcuts.
 *
 * Auth is a client concern here because Next is a thin shell over the Go API
 * (the cookie lives on the proxied origin). A 401 from /auth/me is the only
 * reliable "not signed in" signal, so it drives the redirect.
 *
 * `usePathname()` is URL data: with Cache Components enabled it may only be
 * read behind a Suspense boundary, otherwise the route cannot be prerendered.
 * The whole authenticated frame therefore streams in at request time — which
 * is correct, since no page here is meaningful before the session resolves.
 */
export function AppShell({ children }: { children: ReactNode }) {
  return (
    <Suspense fallback={<ShellSkeleton />}>
      <AppShellFrame>{children}</AppShellFrame>
    </Suspense>
  );
}
/**
 * The bar that carries the sidebar trigger.
 *
 * With `collapsible="offcanvas"` the sidebar can be fully hidden, which is what
 * the collapse button promises; that only works if something brings it back.
 * On a wide screen the bar appears exactly when the sidebar is collapsed, and on
 * a narrow one it is always there (the sidebar is an overlay).
 */
function ShellTopBar() {
  const { state } = useSidebar();
  return (
    <div
      className={cn(
        "flex h-10 shrink-0 items-center gap-2 border-b px-2",
        state === "collapsed" ? "flex" : "md:hidden",
      )}
    >
      <SidebarTrigger data-testid="sidebar-trigger" />
      <span className="text-sm font-medium">PiForge</span>
    </div>
  );
}