import { Suspense } from "react";

import { Workbench, WorkbenchSkeleton } from "@/components/session/workbench";

export default function Page({
  params,
}: {
  params: Promise<{ project: string; session: string }>;
}) {
  return (
    <Suspense fallback={<WorkbenchSkeleton />}>
      <SessionRoute params={params} />
    </Suspense>
  );
}

async function SessionRoute({
  params,
}: {
  params: Promise<{ project: string; session: string }>;
}) {
  const { project, session } = await params;
  return <Workbench sessionId={session} projectId={project} />;
}
