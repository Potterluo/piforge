import { Suspense } from "react";

import { CompareRoute } from "@/components/session/compare-route";

/**
 * The comparison's selection lives in `?ids=`, and this deployment runs Next with
 * Cache Components: awaiting `searchParams` in the page turned a prerendered shell into
 * a dynamic one at runtime and every comparison answered 500. Reading the query on the
 * client keeps the shell static and works for a page whose only input is the URL.
 */
export default function Page() {
  return (
    <Suspense fallback={null}>
      <CompareRoute />
    </Suspense>
  );
}