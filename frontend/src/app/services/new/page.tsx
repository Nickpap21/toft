"use client";

import { Suspense } from "react";
import { useAuth } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { NewServiceForm } from "@/app/projects/_lib/new-service";

/** Home's Deploy your first app (7g): a service in a new project named after
 *  it, deployed once created. ?source= and ?template= pick the way. */
export default function DeployAppPage() {
  const { user } = useAuth();
  useDocumentTitle(`Deploy an app · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`);
  return (
    <Suspense>
      <NewServiceForm projectId="" first />
    </Suspense>
  );
}
