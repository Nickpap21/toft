"use client";

import { useEffect } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { cycleService } from "@/lib/api/issue.service";
import { Read } from "@/components/shell/unreachable";
import { useDocumentTitle } from "@/lib/use-document-title";

// Cycles (D-90): the current cycle's page, or the latest one's.
export default function CyclesPage() {
  useDocumentTitle("Cycles · Toft");
  const router = useRouter();
  const current = useQuery({ queryKey: ["cycles", "current"], queryFn: () => cycleService.list("current", undefined, 1) });
  const latest = useQuery({ queryKey: ["cycles", "list", "latest"], queryFn: () => cycleService.list(undefined, undefined, 1), enabled: current.data?.data.length === 0 });
  const to = current.data?.data[0]?.number ?? latest.data?.data[0]?.number;
  useEffect(() => {
    if (to) router.replace(`/cycles/${to}`);
  }, [to, router]);
  const skeleton = <div role="status" aria-label="Loading" className="h-80 rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  if (!current.data) return <Read query={current} what="the cycles" heading="h1" loading={skeleton}>{() => null}</Read>;
  if (!latest.data && current.data.data.length === 0) return <Read query={latest} what="the cycles" heading="h1" loading={skeleton}>{() => null}</Read>;
  if (latest.data && !to) {
    return (
      <section className="flex max-w-[640px] flex-col gap-2">
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Cycles</h1>
        <p className="text-[13px] text-fg/70">
          The workspace has no cycles yet. An owner turns them on in{" "}
          <Link href="/settings#cyc-h" className="inline-link">
            Settings → Workspace
          </Link>
          : rounds of work of one to four weeks, started on time and carrying what is unfinished to the next.
        </p>
      </section>
    );
  }
  return skeleton;
}
