"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api/client";
import { setupQuery, setupService, type Setup } from "@/lib/api/instance.service";
import { Notice, authLink, useInstanceHost } from "@/components/shell/auth-shell";
import { KitFile, KitStored, kitChanged, kitFailure, useRecoveryKit } from "@/components/instance/recovery-kit";
import { SetupPage } from "@/components/setup/setup-page";
import { previousStep, setupBack, setupBody, setupCard, setupFoot, setupPrimary } from "@/components/setup/setup-frame";

// Step 4 (board ReworkSetup, D-62, D-64): required. Shown after a password
// confirmation; Finish waits for the checkbox, which is recorded, then ends
// setup and opens Home, or the app the installer was asked for (--app, D-61).

const button =
  "inline-flex h-9 pointer-coarse:h-11 items-center rounded-lg border border-strong/60 bg-raised px-3 text-[13px] font-medium text-fg hover:bg-surface focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link";

export default function KitStep() {
  return (
    <SetupPage step="kit" title="Save your recovery kit">
      {(setup) => <Kit setup={setup} />}
    </SetupPage>
  );
}

function Kit({ setup }: { setup: Setup }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const host = useInstanceHost();
  // The step's heading is the frame's, above this card.
  const heading = useRef<HTMLElement | null>(null);
  useEffect(() => {
    heading.current = document.getElementById("setup-h");
  }, []);
  const read = useRecoveryKit(heading);
  const { kit } = read;
  // An out-of-date kit is stored again (D-163).
  const stored = !!setup.kit_acknowledged_at && !setup.kit_out_of_date;
  const [ticked, setTicked] = useState(stored);

  const finish = useMutation({
    mutationFn: async (domainChecked: boolean) => {
      const acked = stored ? setup : await setupService.patch({ kit_acknowledged: true, fingerprint: kit!.fingerprint });
      // Reopened from Instance → This panel after setup: nothing to finish.
      if (setup.finished_at) return acked;
      return setupService.patch(domainChecked ? { finished: true, domain_checked: true } : { finished: true });
    },
    onSuccess: (st) => {
      queryClient.setQueryData(setupQuery.queryKey, st);
      router.push(st.app ? `/services/new?template=${encodeURIComponent(st.app)}` : "/dashboard");
    },
    onError: (err) => {
      if (!kitChanged(err)) return;
      // The kit on this page is no longer the panel's: read it again.
      setTicked(false);
      void read.ask();
    },
  });

  const unreachable = finish.error instanceof ApiError && (finish.error.body as { data?: { code?: string } } | null)?.data?.code === "panel_domain_unreachable";

  return (
    <form
      className={setupCard}
      onSubmit={(e) => {
        e.preventDefault();
        // Already on the domain: this browser has checked it, whatever the server sees.
        finish.mutate(host === setup.domain);
      }}
    >
      <div className={setupBody}>
        <KitFile {...read} />
        <KitStored checked={ticked} disabled={!kit} onChange={setTicked} />
        {unreachable ? (
          <>
            <Notice kind="warn" alert className="[overflow-wrap:anywhere]">
              {setup.domain} doesn’t answer from this server yet. When setup finishes, Toft stops answering on {host}, so if the domain
              doesn’t work you can’t get back in. Open{" "}
              <a href={`https://${setup.domain}`} target="_blank" rel="noopener noreferrer" className={`${authLink} touch-target inline-flex items-center`}>
                https://{setup.domain}
              </a>{" "}
              in another tab to check.
            </Notice>
            <div className="flex flex-wrap gap-2">
              <button type="button" className={button} onClick={() => router.push("/setup/address")}>
                Change the address
              </button>
              <button type="button" className={setupBack} onClick={() => finish.mutate(true)}>
                It opens: finish anyway
              </button>
            </div>
          </>
        ) : (
          finish.error && (
            <Notice kind="error">
              {kitChanged(finish.error) ? "The kit changed while this page was open. Store the one shown now, then finish." : kitFailure(finish.error)}
            </Notice>
          )
        )}
      </div>
      <div className={setupFoot}>
        <button type="button" onClick={() => router.push(previousStep("kit"))} className={setupBack}>
          Back
        </button>
        <button type="submit" disabled={!ticked || !kit || finish.isPending} className={setupPrimary}>
          {finish.isPending ? "Finishing…" : "Finish and open Home"}
        </button>
      </div>
    </form>
  );
}
