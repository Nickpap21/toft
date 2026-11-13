"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy, KeyRound } from "lucide-react";
import { useOrgRole } from "@/context/AuthContext";
import { useReauth } from "@/components/reauth";
import { catalogService } from "@/lib/api/catalog.service";
import { serviceService, type AdminPasswordValue, type Service } from "@/lib/api/service.service";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cancelButton, dialogContent, dialogFooter, dialogHeader, dialogText, dialogTitle, primaryButton } from "@/components/settings/settings-frame";
import { cn } from "@/lib/utils";

// A catalog app's admin password (or temporary login), kept for the person
// who created the service until they dismiss it or 7 days pass (D-160).
// Owners and developers only; the value needs their password confirmed.
// Not on a board: built from the Overview's card and the backup
// password dialog's shown-once parts.

const card = "min-w-0 overflow-hidden rounded-[10px] border border-subtle bg-surface";
const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const smallButton =
  "inline-flex h-[30px] items-center gap-1.5 rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:border-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60 pointer-coarse:min-h-11";

const when = (iso?: string) =>
  iso ? new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }) : "";

export function AdminPasswordCard({ service, serviceId }: { service: Service; serviceId: string }) {
  const role = useOrgRole();
  const queryClient = useQueryClient();
  const confirmPassword = useReauth();
  const key = ["admin-password", serviceId];
  const { data } = useQuery({
    queryKey: key,
    queryFn: () => serviceService.adminPassword(serviceId),
    enabled: role === "owner" || role === "developer",
    retry: false,
  });
  const { data: templates, isError: catalogFailed } = useQuery({ queryKey: ["catalog"], queryFn: catalogService.list, staleTime: Infinity, enabled: !!data?.pending });
  const [shown, setShown] = useState<AdminPasswordValue | null>(null);
  const [copied, setCopied] = useState(false);
  const [asking, setAsking] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 2000);
    return () => clearTimeout(t);
  }, [copied]);

  const temporary = data?.what === "temporary_login";
  const app = templates?.find((t) => t.slug === service.addon_slug)?.name ?? service.name;
  const title = temporary ? `Your ${service.name} temporary login` : `Your ${app} admin password`;
  const show = useMutation({
    mutationFn: async () => {
      if (!(await confirmPassword(`Toft asks for your password before it shows ${title.replace(/^Your /, "the ")}.`, { title: "Confirm it’s you", button: "Show password" }))) return null;
      return serviceService.adminPasswordValue(serviceId);
    },
    onSuccess: (v) => v && setShown(v),
    onError: () => toast.error("Couldn’t show the password. Try again."),
  });
  const dismiss = useMutation({
    mutationFn: () => serviceService.dismissAdminPassword(serviceId),
    onSuccess: () => {
      setShown(null);
      setAsking(false);
      queryClient.setQueryData(key, { pending: false });
      toast.success(temporary ? "Temporary login password deleted" : `${app} admin password deleted`);
    },
    onError: () => toast.error("Couldn’t dismiss it. Try again."),
  });

  // The catalog names the app: nothing until it has, so the title doesn't
  // change under the reader. If it can't, the service's own name will do.
  if (!data?.pending || (service.addon_slug && !templates && !catalogFailed)) return null;
  const why = temporary
    ? `Toft put this login in front of ${service.name} when you created it. `
    : data.reason === "reset"
      ? `Toft set this password when you reset it. `
      : `Toft generated this password when you created ${service.name}. `;

  return (
    <section aria-labelledby="ap-h" className={card}>
      <div className={cardHead}>
        <h2 id="ap-h" className={cn(h2, "flex items-center gap-2")}>
          <KeyRound className="h-4 w-4 text-muted" aria-hidden="true" />
          {title}
        </h2>
        <span className="text-xs text-muted">Only you can see it</span>
      </div>
      <div className="flex flex-col gap-3 p-4">
        <p className="text-[13px] leading-[19px] text-fg/80">
          {why}Copy it into your password manager: once you’re done here, or on {when(data.expires_at)}, it’s deleted and you won’t see it again.
        </p>
        {shown ? (
          <div className="flex flex-col gap-1.5">
            {shown.user && (
              <span className="text-[13px] text-muted">
                User <span className="font-mono text-fg">{shown.user}</span>
              </span>
            )}
            <div className="flex items-center gap-2">
              <input
                readOnly
                autoFocus
                aria-label={temporary ? "Temporary login password" : `${app} admin password`}
                value={shown.password}
                onFocus={(e) => e.target.select()}
                className="h-[34px] min-w-0 flex-1 rounded-md border border-strong bg-ground px-2.5 font-mono text-[13px] text-fg"
              />
              <button
                type="button"
                className={smallButton}
                onClick={() =>
                  navigator.clipboard.writeText(shown.password).then(
                    () => setCopied(true),
                    () => toast.error("Couldn’t copy: select it and copy it"),
                  )
                }
              >
                {copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
                <span aria-live="polite">{copied ? "Copied" : "Copy"}</span>
              </button>
            </div>
          </div>
        ) : null}
        <div className="flex flex-wrap gap-2">
          {!shown && (
            <button type="button" className={smallButton} disabled={show.isPending} onClick={() => show.mutate()}>
              Show password…
            </button>
          )}
          <button type="button" className={smallButton} disabled={dismiss.isPending} onClick={() => (shown ? dismiss.mutate() : setAsking(true))}>
            {shown ? "Done, I stored it" : "Delete password…"}
          </button>
        </div>
      </div>

      {/* Deleting a copy nobody has seen yet: confirmed, with what it costs. */}
      <Dialog open={asking} onOpenChange={(o) => !o && !dismiss.isPending && setAsking(false)}>
        <DialogContent className={cn(dialogContent, "max-w-[480px]")} showClose={false}>
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={dialogTitle}>Delete {title.replace(/^Your /, "your ")}?</DialogTitle>
            <DialogDescription className={dialogText}>
              You haven’t shown it yet. Once it’s deleted, you won’t see it again.
            </DialogDescription>
          </DialogHeader>
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={() => setAsking(false)}>Keep it</button>
            <button type="button" className={primaryButton} disabled={dismiss.isPending} onClick={() => dismiss.mutate()}>Delete password</button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
