"use client";

import { useRef, useState, useSyncExternalStore } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cancelButton, dangerButton, dialogContent, dialogHeader, dialogText, dialogTitle, primaryButton } from "@/components/settings/settings-frame";
import { useReauth } from "@/components/reauth";
import { ApiError } from "@/lib/api/client";
import { setupQuery, setupService } from "@/lib/api/instance.service";
import { cn } from "@/lib/utils";

const WHY = "Stopping the answer on the IP address changes where this panel can be opened, so Toft asks for your password first.";

const noop = () => () => {};
const mono = "break-all font-mono text-xs text-fg";
/** The panel refused the stop: its own check didn't come back through the domain. */
const unreachable = (err: unknown) => err instanceof ApiError && (err.body as { data?: { code?: unknown } } | null)?.data?.code === "panel_domain_unreachable";

/** "Stop answering on the IP…" (D-164): a restored panel then answers only at
 *  its domain, before the panel has seen the domain reach it with a trusted
 *  certificate. A browser on the domain shows that the domain reaches this
 *  panel. From any other address the panel checks it when the dialog opens
 *  and again when it stops; while the domain doesn't reach it, the stop
 *  needs the operator's word that they can get to the server, where
 *  `toft answer-on-ip` is the way back (D-165). */
export function StopIpDialog({ domain, open, onClose, returnFocus }: { domain: string; open: boolean; onClose: () => void; returnFocus: React.RefObject<HTMLElement | null> }) {
  const queryClient = useQueryClient();
  const confirm = useReauth();
  const host = useSyncExternalStore(noop, () => window.location.hostname, () => "");
  const here = host === domain;
  const cancel = useRef<HTMLButtonElement>(null);
  const [sure, setSure] = useState(false);
  const [refused, setRefused] = useState(false);
  // Asked each time the dialog opens, and not again when the tab the link
  // opened is left for this one: that would take the warning and the tick away.
  const check = useQuery({ queryKey: ["instance", "setup", "reaches", domain], queryFn: () => setupService.checkDomain(domain), enabled: open && !here && host !== "", retry: false, staleTime: 0, refetchOnWindowFocus: false });
  const state = here ? "here" : refused ? "away" : check.isFetching || host === "" ? "checking" : check.data?.reaches_panel ? "reaches" : "away";
  const stop = useMutation({
    // The name the panel has, saved again: that ends the IP answer. The
    // panel checks the domain first, unless this browser or the operator's
    // word already answers for it.
    mutationFn: () => setupService.setDomain(domain, here || sure),
    onError: (err) => unreachable(err) && setRefused(true),
    onSuccess: (st) => {
      queryClient.setQueryData(setupQuery.queryKey, st);
      onClose();
      if (here) toast.success(`The panel answers only at ${domain} now`);
      // The address this browser is on is gone, and a sign-in is per address.
      else window.location.assign(`https://${domain}/?next=${encodeURIComponent("/instance")}`);
    },
  });
  // That refusal is the warning below, not an error.
  const error = stop.error && !unreachable(stop.error) ? (stop.error instanceof ApiError && stop.error.status < 500 ? stop.error.message : "Couldn’t reach Toft. Try again.") : "";
  const close = () => {
    onClose();
    stop.reset();
    setSure(false);
    setRefused(false);
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !stop.isPending && close()}>
      <DialogContent
        role="alertdialog"
        className={dialogContent}
        showClose={false}
        returnFocus={returnFocus}
        // Not the link in the middle of the warning, which is first.
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          cancel.current?.focus();
        }}
      >
        <DialogHeader className={dialogHeader}>
          <DialogTitle className={dialogTitle}>Stop answering on the IP address?</DialogTitle>
        </DialogHeader>
        {/* Every paragraph describes the dialog: what follows the first is the warning. */}
        <DialogDescription asChild>
          <div className={cn(dialogText, "mt-1.5 flex flex-col gap-3 px-5")}>
            <p className="pr-7">
              The panel then answers only at <span className={mono}>{domain}</span>. The address with the server’s IP stops at once, for everyone.
            </p>
            {here ? (
              <p>You opened this panel at its domain, so it keeps working for you.</p>
            ) : (
              <>
                <p>
                  You opened this panel at <span className={mono}>{host}</span>, which stops answering.
                </p>
                {/* The check answers after the dialog has opened. */}
                <div aria-live="polite" className="flex flex-col gap-3">
                  {state === "checking" && (
                    <p>
                      Checking whether <span className={mono}>{domain}</span> reaches this panel…
                    </p>
                  )}
                  {state === "reaches" && (
                    <p>
                      <span className={mono}>{domain}</span> reaches this panel right now, so it stays open there. You sign in again at that address.
                    </p>
                  )}
                  {state === "away" && (
                    <>
                      <p>
                        {check.isError && !refused ? (
                          <>
                            Toft couldn’t check whether <span className={mono}>{domain}</span> reaches this panel.
                          </>
                        ) : (
                          <>
                            <span className={mono}>{domain}</span> doesn’t reach this panel right now.
                          </>
                        )}{" "}
                        After the stop, while the domain points at another server, nobody can open this panel. If the domain does open this panel in your browser,{" "}
                        {/* A new tab: this one is signed in on the address that still works. */}
                        <a href={`https://${domain}/instance`} target="_blank" rel="noreferrer" className="touch-target inline-flex items-center font-medium text-link underline underline-offset-2">
                          open the panel at its domain<span className="sr-only"> (opens in a new tab)</span>
                        </a>{" "}
                        and stop it from there: only this restored panel offers to.
                      </p>
                      <p>
                        If nobody can open the panel after the stop, the way back is on the server, over SSH: <span className="block font-mono text-xs text-fg">sudo toft answer-on-ip</span>
                      </p>
                    </>
                  )}
                </div>
              </>
            )}
          </div>
        </DialogDescription>
        {state === "away" && (
          <label className="touch-target mx-5 mt-2 flex items-start gap-2.5 py-1 text-[13px] leading-[19px] text-fg">
            <input type="checkbox" checked={sure} onChange={(e) => setSure(e.target.checked)} disabled={stop.isPending} className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]" />
            I can reach the server over SSH if the panel can’t be opened
          </label>
        )}
        {error && (
          <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
            {error}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
          <button ref={cancel} type="button" className={cancelButton} disabled={stop.isPending} onClick={close}>
            Cancel
          </button>
          <button
            type="button"
            className={state === "here" || state === "reaches" ? primaryButton : dangerButton}
            disabled={stop.isPending || state === "checking" || (state === "away" && !sure)}
            onClick={async () => (await confirm(WHY, { returnFocus: cancel })) && stop.mutate()}
          >
            {stop.isPending ? "Stopping…" : "Stop answering on the IP"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
