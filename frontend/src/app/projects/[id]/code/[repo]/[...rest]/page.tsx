"use client";

import Link from "next/link";
import { cn } from "@/lib/utils";
import { useCode } from "@/components/code/repo";
import { card, h2, outline } from "@/app/projects/_lib/ui";

// Anything else under a repository (a v0.3.1 link is redirected by the
// layout before this shows).
export default function NotACodePage() {
  const { base } = useCode();
  return (
    <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
      <h1 className={h2}>This page doesn’t exist</h1>
      <Link href={base} className={cn(outline, "self-start")}>
        The repository’s files
      </Link>
    </section>
  );
}
