"use client";

import { useEffect } from "react";
import { useParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";
import { projectQuery } from "@/lib/queries";

// The project's Issues page (D-81). Boards: IssueList, Issues.
export default function IssuesLayout({ children }: { children: React.ReactNode }) {
  const projectId = useParams<{ id: string }>().id;
  const { data: project } = useQuery(projectQuery(projectId));
  const { set: setBreadcrumbs } = useBreadcrumbs();
  useEffect(() => {
    if (!project) return;
    setBreadcrumbs([
      { label: "Projects", href: "/projects" },
      { label: project.name, href: `/projects/${projectId}` },
      { label: "Issues", href: "" },
    ]);
  }, [project, projectId, setBreadcrumbs]);
  return <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">{children}</div>;
}
