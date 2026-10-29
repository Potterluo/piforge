"use client";

import { usePathname, useRouter } from "next/navigation";
import { Suspense, useEffect, useState, type ReactNode } from "react";
import { Search } from "lucide-react";

import { AppSidebar } from "@/components/shell/app-sidebar";
import { CommandPalette } from "@/components/shell/command-palette";
import { NewProjectDialog } from "@/components/shell/new-project-dialog";
import { NewSessionDialog } from "@/components/shell/new-session-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { humanizeError } from "@/lib/errors";
import { readSidebarOpen, writeSidebarOpen } from "@/lib/shell/sidebar-preference";
import { SidebarInset, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";
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
  // The provider's initial state has to be the remembered one, or the sidebar
  // paints expanded and then snaps to the rail on a reload — the flash the user
  // asked us to remove. This is safe to read on the first render because the
  // authenticated frame only mounts after `/auth/me` resolves, i.e. after the
  // shell has already left the server-rendered skeleton behind.
  //
  // The state is *controlled* here on purpose: shadcn's provider treats an
  // `onOpenChange` without an `open` prop as "someone else owns this", so passing
  // the writer alone produced a toggle that persisted the choice but never moved
  // the sidebar. Open + writer together are the working pair.
  const [sidebarOpen, setSidebarOpen] = useState(() => readSidebarOpen());
  const handleSidebarOpenChange = (open: boolean) => {
    setSidebarOpen(open);
    writeSidebarOpen(open);
  };

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
            <p className="text-xs text-muted-foreground">{humanizeError(me.error, "无法连接后端")}</p>
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
    <SidebarProvider
      open={sidebarOpen}
      onOpenChange={handleSidebarOpenChange}
      className="h-svh overflow-hidden"
    >
      <AppSidebar
        onNewProject={() => setNewProjectOpen(true)}
        onNewSession={() => setNewSessionOpen(true)}
        onOpenPalette={() => setPaletteOpen(true)}
      />
      <SidebarInset className="flex h-svh min-h-0 flex-1 flex-col overflow-hidden">
        <ShellTopBar onOpenPalette={() => setPaletteOpen(true)} />
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
 * The bar that carries the sidebar trigger on small screens.
 *
 * The desktop sidebar no longer disappears into a 0px offcanvas state — it
 * collapses to an icon rail that keeps its own toggle — so a bar that appeared
 * only while collapsed would do nothing but shove the whole main column down by
 * 40px (and back up) on every toggle. Below `md` the sidebar is an overlay sheet,
 * where the bar is the only way in, so it stays.
 */
function ShellTopBar({ onOpenPalette }: { onOpenPalette: () => void }) {
  return (
    <div className="flex h-10 shrink-0 items-center gap-2 border-b px-2 md:hidden">
      <SidebarTrigger data-testid="sidebar-trigger" />
      <span className="text-sm font-medium">PiForge</span>
      <Button
        variant="ghost"
        size="xs"
        className="ml-auto"
        aria-label="打开命令面板"
        data-testid="command-palette-trigger-topbar"
        onClick={onOpenPalette}
      >
        <Search />
        搜索命令
      </Button>
    </div>
  );
}