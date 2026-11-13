"use client";

import { Suspense } from "react";
import { useParams, useSearchParams } from "next/navigation";
import { DocEditor } from "@/components/docs/editor";

// A new page, under ?parent= (D-86). Board: DocsEditor.
export default function NewDocRoute() {
  return (
    <Suspense>
      <NewDoc />
    </Suspense>
  );
}

function NewDoc() {
  const { id } = useParams<{ id: string }>();
  const parent = useSearchParams().get("parent") ?? undefined;
  return <DocEditor projectId={id} parentId={parent} />;
}
