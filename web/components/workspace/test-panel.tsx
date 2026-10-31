"use client";

import * as React from "react";
import {
  CheckCircle2Icon,
  ChevronDownIcon,
  CircleHelpIcon,
  FlaskConicalIcon,
  Loader2Icon,
  RefreshCwIcon,
  TimerIcon,
  XCircleIcon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { absoluteTime, relativeTime } from "@/lib/git/relative-time";
import type { TestFailure, TestRun } from "@/lib/types";
import { cn } from "@/lib/utils";
import { PanelError, PanelSkeleton } from "@/components/workspace/panel-state";

/**
 * The test-results panel (PRD FR-12.5).
 *
 * It answers one question at a glance: did the tests run, and what failed. The
 * data is the session's own derived runs (`GET /v1/sessions/{id}/test-runs`),
 * so it covers both an agent's bash call and a command the user typed in the
 * terminal — and it survives a reload because the rows are persisted, not
 * reconstructed from whatever is on screen.
 *
 * `unrecognized` is rendered deliberately differently: it says 未识别格式 and
 * shows the raw tail, because "the command ran but its output could not be
 * read" is useful information and a made-up pass/fail is not.
 */
export interface TestResultsPanelProps {
  sessionId: string;
  runs: TestRun[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  /** Opens a failure's file in the editor at that line. */
  onOpenLocation?: (path: string, line: number) => void;
}

const STATUS_META = {
  passed: {
    label: "通过",
    state: "pass",
    icon: CheckCircle2Icon,
    className: "text-emerald-600 dark:text-emerald-400",
    badge: "border-emerald-600/40 text-emerald-700 dark:text-emerald-400",
  },
  failed: {
    label: "失败",
    state: "fail",
    icon: XCircleIcon,
    className: "text-red-600 dark:text-red-400",
    badge: "border-red-600/40 text-red-700 dark:text-red-400",
  },
  running: {
    label: "运行中",
    state: "running",
    icon: Loader2Icon,
    className: "text-amber-600 dark:text-amber-400",
    badge: "border-amber-600/40 text-amber-700 dark:text-amber-400",
  },
  unrecognized: {
    label: "未识别格式",
    state: "unknown",
    icon: CircleHelpIcon,
    className: "text-muted-foreground",
    badge: "text-muted-foreground",
  },
} as const;

/** The tab badge's state, derived from the latest run. */
export function latestTestState(runs: TestRun[] | null | undefined): "pass" | "fail" | "running" | "unknown" | null {
  const latest = runs?.[0];
  if (!latest) return null;
  return STATUS_META[latest.status]?.state ?? "unknown";
}

/** A compact badge for the 测试 tab: visible without opening the panel. */
export function TestTabBadge({ runs }: { runs: TestRun[] | null | undefined }) {
  const latest = runs?.[0];
  if (!latest) return null;
  const meta = STATUS_META[latest.status] ?? STATUS_META.unrecognized;
  const Icon = meta.icon;
  // A runner that reports no count (go test without -v) must not put a "0" next
  // to a green check: the badge would claim zero tests ran.
  const count =
    latest.total > 0
      ? latest.status === "passed"
        ? latest.passed
        : latest.status === "failed"
          ? latest.failed
          : null
      : null;
  return (
    <span
      data-testid="test-tab-badge"
      data-state={meta.state}
      title={
        latest.status === "running"
          ? `运行中：${latest.command}`
          : `${meta.label}：${latest.command}`
      }
      aria-label={`最近一次测试：${meta.label}`}
      className={cn("inline-flex items-center gap-0.5 text-[10px] leading-none", meta.className)}
    >
      <Icon className={cn("size-3", latest.status === "running" && "animate-spin")} />
      {count !== null ? <span className="font-mono">{count}</span> : null}
    </span>
  );
}

function durationLabel(run: TestRun): string | null {
  if (run.status === "running" || !run.duration_ms) return null;
  if (run.duration_ms < 1000) return `${run.duration_ms}ms`;
  return `${(run.duration_ms / 1000).toFixed(2)}s`;
}

function Counts({ run }: { run: TestRun }) {
  if (run.status === "running") {
    return (
      <span className="shrink-0 font-mono text-[10px] text-amber-600 dark:text-amber-400">
        运行中
      </span>
    );
  }
  if (run.status === "unrecognized") {
    return <span className="shrink-0 text-[10px] text-muted-foreground">未识别格式</span>;
  }
  // `go test` without -v prints only `ok pkg 0.002s`: it passed, but it never
  // said how many tests ran. Showing "✅ 0" there would be a fabricated count,
  // so the row says 通过 instead.
  if (run.total === 0 && run.passed === 0 && run.failed === 0) {
    return (
      <span
        className={cn(
          "shrink-0 text-[10px]",
          run.status === "passed"
            ? "text-emerald-600 dark:text-emerald-400"
            : "text-red-600 dark:text-red-400",
        )}
        title="测试框架没有报告用例数（go test 需要 -v）"
      >
        {run.status === "passed" ? "✅ 通过" : "❌ 失败"}
      </span>
    );
  }
  return (
    <span className="flex shrink-0 items-center gap-1.5 font-mono text-[10px]">
      <span className="text-emerald-600 dark:text-emerald-400">✅ {run.passed}</span>
      <span className={run.failed > 0 ? "text-red-600 dark:text-red-400" : "text-muted-foreground"}>
        ❌ {run.failed}
      </span>
      {run.skipped > 0 ? (
        <span className="text-muted-foreground">⏭ {run.skipped}</span>
      ) : null}
    </span>
  );
}

function FailureRow({
  failure,
  onOpenLocation,
}: {
  failure: TestFailure;
  onOpenLocation?: (path: string, line: number) => void;
}) {
  const location = failure.file
    ? `${failure.file}${failure.line ? `:${failure.line}` : ""}`
    : "";
  return (
    <div data-testid="test-failure" data-file={failure.file ?? ""} data-line={failure.line ?? ""} className="py-1">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <span className="min-w-0 break-words font-mono text-[11px] font-medium">{failure.name}</span>
        {location ? (
          onOpenLocation ? (
            <button
              type="button"
              data-testid="test-failure-location"
              title={`在编辑器中打开 ${location}`}
              className="shrink-0 font-mono text-[10px] text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground"
              onClick={() => onOpenLocation(failure.file!, failure.line ?? 0)}
            >
              {location}
            </button>
          ) : (
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{location}</span>
          )
        ) : null}
      </div>
      {failure.message ? (
        <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-sm bg-muted/50 p-1.5 font-mono text-[10px] leading-relaxed text-muted-foreground">
          {failure.message}
        </pre>
      ) : null}
    </div>
  );
}

function RunRow({
  run,
  onOpenLocation,
}: {
  run: TestRun;
  onOpenLocation?: (path: string, line: number) => void;
}) {
  const [expanded, setExpanded] = React.useState(false);
  const meta = STATUS_META[run.status] ?? STATUS_META.unrecognized;
  const Icon = meta.icon;
  const duration = durationLabel(run);
  const canExpand = run.status === "failed" || run.status === "unrecognized";
  const expandedNow = canExpand && expanded;

  return (
    <div
      data-testid="test-run"
      data-status={run.status}
      data-runner={run.runner}
      data-source={run.source}
      data-run-id={run.id}
      className="border-b last:border-b-0"
    >
      <button
        type="button"
        aria-expanded={expandedNow}
        title={run.command}
        onClick={() => setExpanded((value) => !value)}
        className="flex w-full min-w-0 items-start gap-2 px-2 py-1.5 text-start hover:bg-accent/50"
      >
        <Icon
          className={cn(
            "mt-0.5 size-3.5 shrink-0",
            meta.className,
            run.status === "running" && "animate-spin",
          )}
        />
        <div className="min-w-0 flex-1">
          <div className="truncate font-mono text-[11px]" title={run.command}>
            {run.command}
          </div>
          <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[10px] text-muted-foreground">
            <span data-testid="test-run-state" className={meta.className}>
              {meta.label}
            </span>
            <span aria-hidden>·</span>
            <span className="truncate">{run.runner}</span>
            <span aria-hidden>·</span>
            <span title={absoluteTime(run.started_at)}>{relativeTime(run.started_at)}</span>
            {run.source === "terminal" ? (
              <>
                <span aria-hidden>·</span>
                <span>终端</span>
              </>
            ) : null}
            {duration ? (
              <>
                <span aria-hidden>·</span>
                <span className="inline-flex items-center gap-0.5">
                  <TimerIcon className="size-2.5" />
                  {duration}
                </span>
              </>
            ) : null}
          </div>
        </div>
        <Counts run={run} />
        {canExpand ? (
          <ChevronDownIcon
            className={cn(
              "mt-0.5 size-3 shrink-0 text-muted-foreground transition-transform",
              expandedNow && "rotate-180",
            )}
          />
        ) : null}
      </button>

      {expandedNow ? (
        <div data-testid="test-run-details" className="border-t bg-muted/20 px-2 py-1">
          {run.status === "unrecognized" ? (
            <>
              <p className="text-[10px] text-muted-foreground">
                这条命令的输出无法解析，下面是原始输出末尾：
              </p>
              <pre
                data-testid="test-raw-tail"
                className="mt-1 max-h-52 overflow-auto whitespace-pre-wrap break-words font-mono text-[10px] leading-relaxed"
              >
                {run.output_tail?.trim() || "（没有输出）"}
              </pre>
            </>
          ) : run.failures.length > 0 ? (
            run.failures.map((failure, index) => (
              <FailureRow
                key={`${failure.name}-${index}`}
                failure={failure}
                onOpenLocation={onOpenLocation}
              />
            ))
          ) : (
            <p className="text-[10px] text-muted-foreground">
              这次运行失败了，但测试框架没有给出具体的失败用例。
            </p>
          )}
        </div>
      ) : null}
    </div>
  );
}

export function TestResultsPanel({
  sessionId,
  runs,
  loading,
  error,
  onRetry,
  onOpenLocation,
}: TestResultsPanelProps) {
  if (loading && runs.length === 0) {
    return <PanelSkeleton rows={5} testId="test-panel-loading" className="p-2" />;
  }

  if (error) {
    return <PanelError testId="test-panel-error" message={error} onRetry={onRetry} />;
  }

  return (
    <div data-testid="test-panel" data-panel-session={sessionId} className="flex h-full min-h-0 min-w-0 flex-col">
      <div className="flex shrink-0 items-center gap-1.5 border-b px-2 py-1">
        <span className="text-[10px] text-muted-foreground">
          {runs.length > 0 ? `最近 ${runs.length} 次运行` : "测试结果"}
        </span>
        <Button
          variant="ghost"
          size="icon-xs"
          className="ms-auto"
          aria-label="刷新测试结果"
          data-testid="test-refresh"
          onClick={onRetry}
        >
          <RefreshCwIcon />
        </Button>
      </div>

      {runs.length === 0 ? (
        <Empty data-testid="test-empty" className="flex-1 p-4">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <FlaskConicalIcon />
            </EmptyMedia>
            <EmptyTitle>还没有测试运行</EmptyTitle>
            <EmptyDescription>
              这里会显示它认识的测试命令的结果：通过 / 失败数量、耗时，以及失败用例的文件、行号和断言。
              识别的命令是 <code className="font-mono">go test</code>、<code className="font-mono">pytest</code>、
              <code className="font-mono">npm test</code> / <code className="font-mono">jest</code> /{" "}
              <code className="font-mono">vitest</code>、<code className="font-mono">node --test</code>
              或直接运行测试文件 <code className="font-mono">node x.test.js</code>、
              <code className="font-mono">cargo test</code>、<code className="font-mono">mvn test</code>、
              <code className="font-mono">dotnet test</code>，在终端里输入也算。
              其他脚本（例如 <code className="font-mono">node check.js</code> 或{" "}
              <code className="font-mono">./run-tests.sh</code>）不会被识别，这里也不会编造通过或失败。
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <div className="min-h-0 min-w-0 flex-1 overflow-y-auto">
          {runs.map((run) => (
            <RunRow key={run.id} run={run} onOpenLocation={onOpenLocation} />
          ))}
        </div>
      )}
    </div>
  );
}
