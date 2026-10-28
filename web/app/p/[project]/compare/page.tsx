import { Suspense } from "react";

import { ComparePage } from "@/components/session/compare-page";

/**
 * FR-2.5 lives on its own route so a comparison can be linked and reloaded: the
 * selection is in the query string (`?ids=a,b`), not in component state.
 */
export default function Page({
  params,
  searchParams,
}: {
  params: Promise<{ project: string }>;
  searchParams: Promise<{ ids?: string }>;
}) {
  return (
    <Suspense fallback={null}>
      <Route params={params} searchParams={searchParams} />
    </Suspense>
  );
}

async function Route({
  params,
  searchParams,
}: {
  params: Promise<{ project: string }>;
  searchParams: Promise<{ ids?: string }>;
}) {
  const [{ project }, query] = await Promise.all([params, searchParams]);
  const ids = (query.ids ?? "")
    .split(",")
    .map((id) => id.trim())
    .filter(Boolean);
  return <ComparePage projectId={project} ids={ids} />;
}