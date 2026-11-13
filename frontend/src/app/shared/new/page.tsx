"use client";

import { Suspense } from "react";
import { useAuth } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { NewServiceForm } from "@/app/projects/_lib/new-service";

/** A workspace service has no project: the New service form with its scope
 *  fixed to Workspace (D-47). */
export default function NewWorkspaceServicePage() {
  const { user } = useAuth();
  useDocumentTitle(`New workspace service · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`);
  return (
    <Suspense>
      <NewServiceForm projectId="" />
    </Suspense>
  );
}
