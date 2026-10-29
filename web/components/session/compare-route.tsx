"use client";

import { useParams, useSearchParams } from "next/navigation";

import { ComparePage } from "@/components/session/compare-page";

/** Reads the project and the selected sessions from the URL. */
export function CompareRoute() {
  const params = useParams<{ project: string }>();
  const search = useSearchParams();
  const ids = (search.get("ids") ?? "")
    .split(",")
    .map((id) => id.trim())
    .filter(Boolean);
  return <ComparePage projectId={params.project} ids={ids} />;
}