"use client";

import { AlertTriangleIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

/**
 * Shared loading and error blocks.
 *
 * Every panel in this folder renders one of these instead of an empty white
 * box: `PanelSkeleton` while the first request is in flight, `PanelError` with
 * a retry button afterwards.
 */

export function PanelSkeleton({
  rows = 6,
  className,
  testId,
}: {
  rows?: number;
  className?: string;
  testId?: string;
}) {
  return (
    <div
      data-slot="panel-skeleton"
      data-testid={testId}
      className={cn("flex min-w-0 flex-col gap-1.5 p-2", className)}
      aria-busy="true"
      aria-live="polite"
    >
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton
          key={index}
          className="h-5"
          style={{ width: `${52 + ((index * 13) % 38)}%` }}
        />
      ))}
      <span className="sr-only">正在加载</span>
    </div>
  );
}

export function PanelError({
  message,
  onRetry,
  className,
  testId,
}: {
  message: string;
  onRetry?: () => void;
  className?: string;
  testId?: string;
}) {
  return (
    <div
      data-slot="panel-error"
      data-testid={testId}
      role="alert"
      className={cn(
        "flex min-w-0 flex-col items-start gap-2 border-destructive/30 border-dashed p-3",
        className,
      )}
    >
      <p className="flex min-w-0 items-start gap-1.5 text-xs text-destructive">
        <AlertTriangleIcon className="mt-px size-3.5 shrink-0" />
        <span className="min-w-0 break-words">{message}</span>
      </p>
      {onRetry ? (
        <Button variant="outline" size="xs" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  );
}
