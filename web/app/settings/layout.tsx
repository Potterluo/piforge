import type { ReactNode } from "react";

import { AppShell } from "@/components/shell/app-shell";
import { SettingsNav } from "@/components/settings/settings-nav";

/**
 * Every settings page gets the same frame: the app shell, then a local
 * navigation column and the page itself. Pages keep their own scrolling and
 * headers, which is why the content cell is a flex column rather than another
 * scroll container.
 */
export default function SettingsLayout({ children }: { children: ReactNode }) {
  return (
    <AppShell>
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden md:flex-row" data-testid="settings-frame">
        <SettingsNav />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">{children}</div>
      </div>
    </AppShell>
  );
}
