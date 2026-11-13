"use client";

import { useParams } from "next/navigation";
import { DocEditor } from "@/components/docs/editor";

// Editing a page (D-86). Board: DocsEditor.
export default function EditDocRoute() {
  const { id, page } = useParams<{ id: string; page: string }>();
  return <DocEditor projectId={id} pageId={page} />;
}
