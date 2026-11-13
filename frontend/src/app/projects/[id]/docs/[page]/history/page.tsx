"use client";

import { useParams } from "next/navigation";
import { DocHistory } from "@/components/docs/history";

// A page's versions (D-86). No board: built from the Docs board's parts.
export default function DocHistoryRoute() {
  const { id, page } = useParams<{ id: string; page: string }>();
  return <DocHistory projectId={id} pageId={page} />;
}
