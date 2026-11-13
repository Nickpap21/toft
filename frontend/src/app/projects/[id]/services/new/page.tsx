"use client";

import { Suspense } from "react";
import { useParams } from "next/navigation";
import { useAuth } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { NewServiceForm } from "../../../_lib/new-service";

export default function NewServicePage() {
  const { id } = useParams<{ id: string }>();
  const { user } = useAuth();
  useDocumentTitle(`New service · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`);
  // useSearchParams (?env=, ?scope=) needs a boundary on a page Next prerenders.
  return (
    <Suspense>
      <NewServiceForm projectId={id} />
    </Suspense>
  );
}
