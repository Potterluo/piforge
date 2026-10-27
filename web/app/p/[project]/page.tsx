import { Suspense } from "react";

import {
  ProjectOverview,
  ProjectOverviewSkeleton,
} from "@/components/shell/project-overview";

/**
 * Next 16 route params are async, and Cache Components wants runtime reads
 * behind a Suspense boundary. The page shell is static; the project is
 * streamed in once the params resolve.
 */
export default function Page({ params }: { params: Promise<{ project: string }> }) {
  return (
    <Suspense fallback={<ProjectOverviewSkeleton />}>
      <ProjectRoute params={params} />
    </Suspense>
  );
}

async function ProjectRoute({ params }: { params: Promise<{ project: string }> }) {
  const { project } = await params;
  return <ProjectOverview projectId={project} />;
}
