"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronLeft } from "lucide-react";
import { toast } from "sonner";
import { setupQuery, setupService, upgradeQuery } from "@/lib/api/instance.service";
import { TONE_CLASSES } from "@/lib/status";
import { cn, formatDateTime } from "@/lib/utils";
import { card, h2, outline, primary, saveBar } from "@/app/projects/_lib/ui";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { InstanceFrame } from "@/components/instance/instance-frame";
import { KitFile, KitStored, kitChanged, kitFailure, useRecoveryKit } from "@/components/instance/recovery-kit";

/** Instance → Overview → Recovery kit (D-163, board ReworkInstanceKit): the
 *  kit in the normal shell, after a password confirmation, with whether the
 *  one that was stored still restores this panel. Operators only. */
export default function InstanceKitPage() {
  return (
    <InstanceFrame title="Recovery kit">
      <Link href="/instance" className="inline-flex h-6 items-center gap-1 self-start text-[13px] text-fg/70 hover:text-fg pointer-coarse:h-11">
        <ChevronLeft className="h-3.5 w-3.5" aria-hidden="true" />
        Overview
      </Link>
      <div className="grid items-start gap-6 min-[1200px]:grid-cols-[minmax(0,1fr)_340px]">
        <KitCard />
        <section aria-labelledby="kc-h" className={card}>
          <div className="border-b border-subtle px-4 py-3">
            <h2 id="kc-h" className={h2}>
              When to store it again
            </h2>
          </div>
          <div className="flex flex-col gap-3.5 p-4">
            <ul className="list-disc pl-[18px] text-[13px] leading-5 text-fg/85 [&>li+li]:mt-1">
              <li>
                The encryption key is rotated with{" "}
                <code className="inline-flex h-5 items-center whitespace-nowrap rounded border border-strong px-1.5 font-mono text-xs text-fg">toft rotate-encryption-key</code>.
              </li>
              <li>Backups of the panel host are set up, or their repository, access key or repository password changes.</li>
            </ul>
            <p className="text-[13px] leading-[19px] text-muted">
              Every instance operator then sees it here, on Home and in the Inbox, until one of you marks the new kit as stored.
            </p>
          </div>
          <p className="border-t border-subtle px-4 py-2.5 text-[13px] leading-[19px] text-muted">
            Toft keeps a fingerprint of what the stored kit holds, never the kit. Each time someone opens the kit or marks it as stored is in the{" "}
            <Link href="/instance/audit-log" className="touch-target inline-flex items-center text-link hover:underline">
              audit log
            </Link>
            .
          </p>
        </section>
      </div>
    </InstanceFrame>
  );
}

// The shell's buttons, not the wizard's (D-164): Download as its neighbours, Copy the quiet one.
const KIT_BUTTONS = { main: outline, quiet: cn(outline, "border-transparent bg-transparent text-fg/70 hover:bg-raised hover:text-fg") };

function KitCard() {
  const queryClient = useQueryClient();
  const setupRead = useQuery(setupQuery);
  const up = useQuery(upgradeQuery).data;
  const heading = useRef<HTMLHeadingElement>(null);
  const read = useRecoveryKit(heading);
  const { kit } = read;
  const [ticked, setTicked] = useState(false);
  const mark = useMutation({
    mutationFn: () => setupService.patch({ kit_acknowledged: true, fingerprint: kit!.fingerprint }),
    onSuccess: (st) => {
      queryClient.setQueryData(setupQuery.queryKey, st);
      void queryClient.invalidateQueries({ queryKey: ["inbox"] });
      setTicked(false);
      // The button goes with the acknowledgement: focus stays in the card.
      heading.current?.focus();
      toast.success("Recovery kit marked as stored");
    },
    onError: (err) => {
      if (!kitChanged(err)) return;
      // The kit on this page is no longer the panel's: read both again.
      setTicked(false);
      void read.ask();
      void queryClient.invalidateQueries({ queryKey: setupQuery.queryKey });
    },
  });

  const setup = setupRead.data;
  const stored = setup?.kit_acknowledged_at ? formatDateTime(setup.kit_acknowledged_at).slice(0, 10) : "";
  const stale = !!setup?.kit_out_of_date;
  const current = !!stored && !stale;
  // One accent per screen (D-139): the header's Upgrade, when there is one.
  const upgradeShown = up?.install === "installer" && up.available && up.run?.outcome !== "running";
  const tone = TONE_CLASSES[current ? "running" : "pending"];

  return (
    <section aria-labelledby="kit-h" className={card}>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 border-b border-subtle px-4 py-3">
        <h2 id="kit-h" ref={heading} tabIndex={-1} className={h2}>
          Recovery kit
        </h2>
        {setup && (
          <span className={cn("inline-flex items-center gap-1.5 text-[13px] font-medium", tone.text)}>
            <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", tone.dot)} />
            {current ? `Stored ${stored}` : stale ? "Out of date" : "Not stored yet"}
          </span>
        )}
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        {readFailed(setupRead) && <LoadFailed what="whether the kit is stored" onRetry={() => void setupRead.refetch()} />}
        {stale && (
          <div role="status" className="flex gap-2.5 rounded-lg border border-status-pending/45 bg-status-pending/[0.07] p-3 text-[13px] leading-[19px] text-fg/85">
            <AlertTriangle className="mt-px h-4 w-4 flex-none text-status-pending-fg" aria-hidden="true" />
            <div>
              <b className="block font-medium text-fg">The kit stored on {stored} is out of date</b>
              <p className="mt-0.5">
                The encryption key or the panel host’s backup settings changed after it was stored. That kit can’t restore a backup made
                since. Store this one, and keep the old one while backups from before the change exist: restoring one of those may need it.
              </p>
            </div>
          </div>
        )}
        <KitFile {...read} buttons={KIT_BUTTONS} />
        {setup && !current && <KitStored checked={ticked} disabled={!kit} onChange={setTicked} />}
        {mark.error && (
          <p role="alert" className="text-[13px] leading-[19px] text-status-failed-fg">
            {kitChanged(mark.error) ? "The kit changed while this page was open. Store the one shown now, then mark it as stored." : kitFailure(mark.error)}
          </p>
        )}
      </div>
      {setup && (
        <div className={saveBar}>
          {current ? (
            <span>Nothing a restore needs has changed since this kit was stored.</span>
          ) : (
            <>
              <span>Toft records the date and tells every instance operator when the kit changes again.</span>
              <button
                type="button"
                disabled={!ticked || !kit || mark.isPending}
                onClick={() => mark.mutate()}
                className={cn(upgradeShown ? outline : primary, "max-sm:flex-1 max-sm:justify-center")}
              >
                {mark.isPending ? "Marking…" : "Mark as stored"}
              </button>
            </>
          )}
        </div>
      )}
    </section>
  );
}
