"use client";

import { useEffect } from "react";
import { useParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";
import { DocsTree } from "@/components/docs/tree";
import { projectQuery } from "@/lib/queries";

// The project's Docs page (D-86). Boards: Docs, DocsEditor. The tree
// runs the page's full height, flush with the top, as drawn.
export default function DocsLayout({ children }: { children: React.ReactNode }) {
  const projectId = useParams<{ id: string }>().id;
  const { data: project } = useQuery(projectQuery(projectId));
  const { set: setBreadcrumbs } = useBreadcrumbs();
  useEffect(() => {
    if (!project) return;
    setBreadcrumbs([
      { label: "Projects", href: "/projects" },
      { label: project.name, href: `/projects/${projectId}` },
      { label: "Docs", href: "" },
    ]);
  }, [project, projectId, setBreadcrumbs]);
  return (
    <div className="-m-4 flex min-h-[calc(100%+2rem)] flex-col md:-m-8 md:min-h-[calc(100%+4rem)] md:flex-row md:items-stretch">
      <DocsTree projectId={projectId} />
      {children}
    </div>
  );
}
