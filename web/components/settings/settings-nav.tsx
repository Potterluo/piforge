"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Boxes, KeyRound, KeySquare, SlidersHorizontal, Sparkles } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * The settings area's own navigation.
 *
 * It lives in the settings layout, not in a page, because every settings screen
 * has to offer the same way out. Before this, the only route to four of the five
 * pages was the Ctrl+K palette: the sidebar's 设置 always landed on
 * /settings/providers, and nothing on screen said the others existed.
 *
 * `aria-current="page"` is the accessible marker; `data-active` is what the
 * tests and the styling share, so the current item cannot drift between the two.
 */
const ITEMS = [
  { href: "/settings/providers", label: "模型与凭据", icon: KeyRound, slug: "providers" },
  { href: "/settings/skills", label: "Skill 中心", icon: Sparkles, slug: "skills" },
  { href: "/settings/mcp", label: "MCP 中心", icon: Boxes, slug: "mcp" },
  { href: "/settings/api-keys", label: "API Key", icon: KeySquare, slug: "api-keys" },
  { href: "/settings/general", label: "通用", icon: SlidersHorizontal, slug: "general" },
] as const;

export function SettingsNav() {
  const pathname = usePathname();

  return (
    <nav
      data-testid="settings-nav"
      aria-label="设置导航"
      className="flex w-full shrink-0 flex-row gap-1 overflow-x-auto border-b border-border p-2 md:w-56 md:flex-col md:overflow-visible md:border-e md:border-b-0 md:p-3"
    >
      <p className="hidden px-2 py-1 text-xs font-medium text-muted-foreground md:block">设置</p>
      {ITEMS.map((item) => {
        const active = pathname === item.href || pathname.startsWith(`${item.href}/`);
        const Icon = item.icon;
        return (
          <Link
            key={item.href}
            href={item.href}
            data-testid={`settings-nav-${item.slug}`}
            data-active={active ? "true" : "false"}
            aria-current={active ? "page" : undefined}
            className={cn(
              "flex shrink-0 items-center gap-2 rounded-md px-2 py-1.5 text-sm whitespace-nowrap transition-colors",
              "hover:bg-muted hover:text-foreground",
              active ? "bg-muted font-medium text-foreground" : "text-muted-foreground",
            )}
          >
            <Icon className="size-4 shrink-0" />
            <span>{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}
