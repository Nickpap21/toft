"use client";

import { useParams } from "next/navigation";
import { DeletedDocs } from "@/components/docs/history";

// Pages deleted in the last 30 days (D-86).
export default function DeletedDocsRoute() {
  return <DeletedDocs projectId={useParams<{ id: string }>().id} />;
}
