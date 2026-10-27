"use client";

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { assignLanes, type LaneNode } from "@/lib/git/lanes";
import { absoluteTime, relativeTime } from "@/lib/git/relative-time";
import type { GitCommit, GitRef } from "@/lib/types";

/**
 * The commit DAG, drawn as inline SVG (PRD §10.6).
 *
 * Layout comes from `assignLanes()` — a pure function with exact-lane tests.
 * This file is only about painting and scrolling:
 *  - one absolutely positioned row per commit, 28px tall;
 *  - only rows in view ±OVERSCAN are mounted, so 5000 commits first-paint in
 *    one screenful of DOM nodes instead of 5000;
 *  - lane colours are derived from `--chart-1..5` mixed with `--foreground`, so
 *    the graph follows light/dark without a single hard-coded hex value.
 */
const ROW_HEIGHT = 28;
const LANE_WIDTH = 18;
const NODE_RADIUS = 4.5;
const OVERSCAN = 20;
const CHART_TOKENS = 5;

function laneColor(lane: number): string {
  const token = `--chart-${(lane % CHART_TOKENS) + 1}`;
  // Mixing with the foreground keeps the near-white `--chart-1` visible on a
  // light background and keeps the near-black `--chart-5` visible on a dark one.
  return `color-mix(in oklab, var(${token}) 62%, var(--foreground) 38%)`;
}

function refVariant(type: GitRef["type"]): "default" | "secondary" | "outline" {
  if (type === "head") return "default";
  if (type === "branch") return "secondary";
  return "outline";
}

const x = (lane: number) => lane * LANE_WIDTH + LANE_WIDTH / 2;

/** One row: lane segments + the node + the label column. */
function GraphRow({
  node,
  commit,
  laneCount,
  active,
  onSelect,
}: {
  node: LaneNode;
  commit: GitCommit;
  laneCount: number;
  active: boolean;
  onSelect: (commit: GitCommit) => void;
}) {
  const incoming = node.incoming ?? [];
  const outgoing = node.outgoing ?? [];
  const refs = (commit.refs ?? []).slice(0, 3);
  const extraRefs = (commit.refs?.length ?? 0) - refs.length;

  const segments: React.ReactNode[] = [];
  for (let lane = 0; lane < laneCount; lane++) {
    const above = incoming[lane] != null;
    const below = outgoing[lane] != null;
    if (!above && !below) continue;

    if (lane === node.lane) {
      if (above) {
        segments.push(
          <line
            key={`up-${lane}`}
            x1={x(lane)}
            y1={0}
            x2={x(lane)}
            y2={ROW_HEIGHT / 2}
            stroke={laneColor(lane)}
            strokeWidth={1.5}
          />,
        );
      }
      if (below) {
        segments.push(
          <line
            key={`down-${lane}`}
            x1={x(lane)}
            y1={ROW_HEIGHT / 2}
            x2={x(lane)}
            y2={ROW_HEIGHT}
            stroke={laneColor(lane)}
            strokeWidth={1.5}
          />,
        );
      }
      continue;
    }

    if (above && below) {
      segments.push(
        <line
          key={`thru-${lane}`}
          x1={x(lane)}
          y1={0}
          x2={x(lane)}
          y2={ROW_HEIGHT}
          stroke={laneColor(lane)}
          strokeWidth={1.5}
        />,
      );
    }
  }

  // Merge parents: cubic bezier from this node's lane to the parent lane.
  const merges: React.ReactNode[] = [];
  for (const parent of node.parents) {
    if (parent.lane === node.lane) continue;
    const from = x(node.lane);
    const to = x(parent.lane);
    const mid = ROW_HEIGHT / 2 + ROW_HEIGHT / 4;
    merges.push(
      <path
        key={`merge-${parent.hash}`}
        d={`M ${from} ${ROW_HEIGHT / 2} C ${from} ${mid}, ${to} ${mid}, ${to} ${ROW_HEIGHT}`}
        fill="none"
        stroke={laneColor(parent.lane)}
        strokeWidth={1.5}
      />,
    );
  }

  return (
    <button
      type="button"
      onClick={() => onSelect(commit)}
      data-testid="git-graph-row"
      data-hash={commit.hash}
      title={`${commit.hash}\n${commit.author} · ${absoluteTime(commit.ts)}\n${commit.subject}`}
      className={cn(
        "absolute inset-x-0 flex min-w-0 items-center gap-1.5 px-1 text-left transition-colors",
        "hover:bg-accent/60 focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none",
        active && "bg-accent",
      )}
      style={{ top: node.row * ROW_HEIGHT, height: ROW_HEIGHT }}
    >
      <svg
        width={Math.max(laneCount, 1) * LANE_WIDTH}
        height={ROW_HEIGHT}
        className="shrink-0 overflow-visible"
        aria-hidden
      >
        {segments}
        {merges}
        <circle
          cx={x(node.lane)}
          cy={ROW_HEIGHT / 2}
          r={NODE_RADIUS}
          fill={laneColor(node.lane)}
          stroke="var(--background)"
          strokeWidth={1}
        />
      </svg>

      <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{commit.short}</span>

      <span className="min-w-0 flex-1 truncate text-[11px]">{commit.subject}</span>

      {/* Author and time share one slot: at 280px a separate author column
          would squeeze the subject to nothing. */}
      <span
        className="max-w-36 shrink-0 truncate text-[10px] text-muted-foreground"
        title={`${commit.author} · ${absoluteTime(commit.ts)}`}
      >
        {commit.author} · {relativeTime(commit.ts)}
      </span>

      <span className="flex shrink-0 items-center gap-0.5">
        {refs.map((ref) => (
          <Badge
            key={`${ref.type}:${ref.name}`}
            variant={refVariant(ref.type)}
            className="h-4 max-w-24 truncate px-1 text-[9px]"
          >
            {ref.name}
          </Badge>
        ))}
        {extraRefs > 0 ? (
          <Badge variant="outline" className="h-4 px-1 text-[9px]">
            +{extraRefs}
          </Badge>
        ) : null}
      </span>
    </button>
  );
}

export interface GitGraphProps {
  commits: GitCommit[];
  activeHash?: string | null;
  onSelect: (commit: GitCommit) => void;
  className?: string;
}

export function GitGraph({ commits, activeHash, onSelect, className }: GitGraphProps) {
  const scrollerRef = React.useRef<HTMLDivElement | null>(null);
  const layout = React.useMemo(() => assignLanes(commits), [commits]);
  const byHash = React.useMemo(
    () => new Map(commits.map((commit) => [commit.hash, commit])),
    [commits],
  );
  const [range, setRange] = React.useState({ start: 0, end: 60 });

  const recompute = React.useCallback(() => {
    const element = scrollerRef.current;
    if (!element) return;
    const start = Math.max(0, Math.floor(element.scrollTop / ROW_HEIGHT) - OVERSCAN);
    const visible = Math.ceil(element.clientHeight / ROW_HEIGHT) + OVERSCAN * 2;
    setRange({ start, end: start + visible });
  }, []);

  React.useEffect(() => {
    recompute();
    const element = scrollerRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(recompute);
    observer.observe(element);
    return () => observer.disconnect();
  }, [recompute, commits]);

  const end = Math.min(layout.nodes.length, range.end);
  const visible = layout.nodes.slice(range.start, end);

  return (
    <div
      ref={scrollerRef}
      onScroll={recompute}
      data-testid="git-graph"
      className={cn("relative min-h-0 min-w-0 flex-1 overflow-y-auto", className)}
      data-slot="git-graph"
    >
      <div style={{ height: layout.nodes.length * ROW_HEIGHT }} className="relative min-w-0">
        {visible.map((node) => {
          const commit = byHash.get(node.hash);
          if (!commit) return null;
          return (
            <GraphRow
              key={node.hash}
              node={node}
              commit={commit}
              laneCount={layout.laneCount}
              active={activeHash === node.hash}
              onSelect={onSelect}
            />
          );
        })}
      </div>
    </div>
  );
}

export { ROW_HEIGHT as GIT_GRAPH_ROW_HEIGHT };
