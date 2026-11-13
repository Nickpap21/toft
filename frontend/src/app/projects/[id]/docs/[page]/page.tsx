"use client";

import { useParams } from "next/navigation";
import { DocReader } from "@/components/docs/reader";

// A page of the docs (D-86). Board: Docs.
export default function DocPageRoute() {
  const { id, page } = useParams<{ id: string; page: string }>();
  return <DocReader projectId={id} pageId={page} />;
}
