"use client";

import Link from "next/link";
import { Sparkles } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { formatRelative } from "@/lib/session/format";
import { useSessionSkills } from "@/lib/session/queries";

/**
 * The read-only record of what this session was mounted with (FR-14.5).
 *
 * It lives in the logs/info sheet because that is where a user goes to answer
 * "why did the run behave like that". The values are a snapshot: editing a
 * skill afterwards does not change this list, which is exactly what makes it
 * useful when comparing two sessions started before and after an edit.
 */
export function SessionSkillsSection({ sessionId }: { sessionId: string }) {
  const query = useSessionSkills(sessionId);
  const records = query.data?.skills ?? [];

  return (
    <section
      aria-label="本会话使用的 Skill"
      data-testid="session-skills"
      className="flex flex-col gap-2 border-t border-border pt-3"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="flex items-center gap-1.5 text-xs font-medium">
          <Sparkles className="size-3.5" />
          本会话使用的 Skill
        </h3>
        <span className="text-[10px] text-muted-foreground">
          挂载时记录；之后修改 Skill 不会改写这里
        </span>
      </div>

      {query.isPending ? (
        <p className="text-xs text-muted-foreground">读取中…</p>
      ) : query.isError ? (
        <p className="rounded-md bg-destructive/10 p-2 text-xs text-destructive">
          读取失败：这个会话的 Skill 记录取不到。
        </p>
      ) : records.length === 0 ? (
        <p data-testid="session-skills-empty" className="text-xs text-muted-foreground">
          这个会话还没有挂载记录。首次打开工作区（或发第一条消息）时会记录；也可以在{" "}
          <Link href="/settings/skills" className="underline underline-offset-2">
            Skill 中心
          </Link>{" "}
          添加。
        </p>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {records.map((record) => (
            <li
              key={record.skill_id}
              data-testid="session-skill-item"
              data-name={record.name}
              data-version={record.version}
              data-hash={record.content_hash}
              className="rounded-md border border-border/70 px-2 py-1.5"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate text-xs font-medium">{record.name}</span>
                <Badge variant="outline" className="font-mono text-[10px]">
                  v{record.version || "0.0.0"}
                </Badge>
                <span
                  className="font-mono text-[10px] text-muted-foreground"
                  title={record.content_hash}
                >
                  {record.content_hash.slice(0, 12)}
                </span>
                <span className="ml-auto text-[10px] text-muted-foreground">
                  {formatRelative(record.recorded_at)}
                </span>
              </div>
              <p
                className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground"
                title={record.mounted_path}
                data-testid="session-skill-path"
              >
                {record.mounted_path || "未记录路径"}
              </p>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
