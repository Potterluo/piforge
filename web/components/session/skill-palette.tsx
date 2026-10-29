"use client";

import Link from "next/link";
import { Sparkles } from "lucide-react";

import type { Skill } from "@/lib/types";
import { truncate } from "@/lib/session/format";
import { cn } from "@/lib/utils";

/**
 * The `/` skill palette (FR-14.6).
 *
 * It is presentational on purpose: the composer owns the text, the filter and
 * the keyboard, so the same list can be rendered without a focus trap and the
 * palette cannot steal a keystroke from the textarea. The listbox pattern
 * (`role=listbox` + `aria-activedescendant` on the input) is what makes it
 * usable without a mouse: the focus never leaves the composer.
 */
export interface SkillPaletteProps {
  id: string;
  /** Skills matching the typed filter, in display order. */
  items: Skill[];
  /** Every skill the account has, before filtering. */
  total: number;
  activeIndex: number;
  query: string;
  loading?: boolean;
  onSelect: (skill: Skill) => void;
  onHover?: (index: number) => void;
  className?: string;
}

/** `v1.0.0 · 1a2b3c4d` — the version a reader can compare at a glance. */
export function skillVersionLabel(skill: Pick<Skill, "version" | "content_hash">): string {
  const version = (skill.version || "0.0.0").trim();
  const hash = (skill.content_hash || "").slice(0, 8);
  return hash ? `v${version} · ${hash}` : `v${version}`;
}

/** The invocation the composer inserts. A trailing space is intentional. */
export function skillInvocation(skill: Pick<Skill, "name">): string {
  return `/skill:${skill.name} `;
}

function optionId(paletteId: string, skill: Skill): string {
  return `${paletteId}-${skill.id}`;
}

export function SkillPalette({
  id,
  items,
  total,
  activeIndex,
  query,
  loading = false,
  onSelect,
  onHover,
  className,
}: SkillPaletteProps) {
  const empty = total === 0;

  return (
    <div
      data-testid="skill-palette"
      className={cn("rounded-md border border-border bg-popover shadow-sm", className)}
    >
      <div
        id={id}
        role="listbox"
        aria-label="Skill 命令"
        aria-busy={loading}
        className="max-h-64 overflow-y-auto p-1"
      >
        {loading && empty ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">正在读取 Skill…</p>
        ) : empty ? (
          <div data-testid="skill-palette-empty" className="flex flex-col gap-1 px-2 py-2">
            <p className="text-xs text-muted-foreground">这个账号还没有可用的 Skill。</p>
            <Link
              href="/settings/skills"
              className="w-fit text-xs font-medium text-primary underline-offset-2 hover:underline"
              data-testid="skill-palette-empty-link"
            >
              去 Skill 中心创建一个
            </Link>
          </div>
        ) : items.length === 0 ? (
          <p data-testid="skill-palette-no-match" className="px-2 py-2 text-xs text-muted-foreground">
            没有匹配「{query ? `/${query}` : "/"}」的 Skill
          </p>
        ) : (
          items.map((skill, index) => {
            const active = index === activeIndex;
            return (
              <button
                key={skill.id}
                type="button"
                role="option"
                id={optionId(id, skill)}
                aria-selected={active}
                data-testid="skill-palette-item"
                data-name={skill.name}
                data-version={skill.version}
                data-hash={skill.content_hash}
                // Mouse hover moves the same cursor the arrow keys use, so
                // clicking and pressing Enter always agree about the target.
                onMouseEnter={() => onHover?.(index)}
                onMouseDown={(event) => {
                  // Keep focus in the textarea; otherwise the click blurs it and
                  // the next keystroke goes nowhere.
                  event.preventDefault();
                }}
                onClick={() => onSelect(skill)}
                className={cn(
                  "flex w-full flex-col gap-0.5 rounded-sm px-2 py-1.5 text-left",
                  active ? "bg-accent text-accent-foreground" : "hover:bg-muted/60",
                )}
              >
                <span className="flex items-center gap-1.5">
                  <Sparkles className="size-3 shrink-0" />
                  <span className="truncate text-xs font-medium">{skill.name}</span>
                  <span className="ml-auto shrink-0 font-mono text-[10px] text-muted-foreground">
                    {skillVersionLabel(skill)}
                  </span>
                </span>
                <span className="line-clamp-1 pl-[18px] text-[11px] text-muted-foreground">
                  {skill.description ? truncate(skill.description, 90) : "没有描述"}
                </span>
              </button>
            );
          })
        )}
      </div>
      <p className="border-t border-border/60 px-2 py-1 text-[10px] text-muted-foreground">
        选中后插入 <span className="font-mono">/skill:名字</span>
        ，再补一句要它做什么，然后发送。
      </p>
    </div>
  );
}
