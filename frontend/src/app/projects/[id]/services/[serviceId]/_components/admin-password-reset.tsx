"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { useOrgRole } from "@/context/AuthContext";
import { useReauth } from "@/components/reauth";
import { ApiError } from "@/lib/api/client";
import { catalogService } from "@/lib/api/catalog.service";
import type { Environment } from "@/lib/api/environment.service";
import { serviceService, type Service } from "@/lib/api/service.service";
import { EnvChip } from "@/components/ui/env-chip";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cancelButton, dialogContent, dialogFooter, dialogHeader, dialogText, dialogTitle, fieldInput, fieldLabel, primaryButton } from "@/components/settings/settings-frame";
import { cn } from "@/lib/utils";

// Settings → Access: a catalog app's admin login, with Reset (D-160).
// Owners and developers, their password confirmed before the dialog opens;
// a protected environment asks for the service's name. MinIO takes the new
// password at its next start, so its Reset deploys at once: this dialog is
// that deploy's protected confirm, so a cancelled deploy can't leave a
// password MinIO doesn't use. The new password then waits on Overview.

const smallButton =
  "inline-flex h-[30px] items-center rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:border-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60 pointer-coarse:min-h-11";

export function AdminLoginRow({
  service,
  serviceId,
  env,
  onDeploy,
}: {
  service: Service;
  serviceId: string;
  env?: Pick<Environment, "name" | "color" | "protected">;
  /** Deploys the service, the protected confirm already given. */
  onDeploy: () => void;
}) {
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
  const { data: templates } = useQuery({ queryKey: ["catalog"], queryFn: catalogService.list, staleTime: Infinity, enabled: !!data?.reset });
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<{ text: string; output?: string } | null>(null);

  const app = templates?.find((t) => t.slug === service.addon_slug)?.name ?? service.name;
  const deploys = data?.reset === "deploy";
  const typeName = !!env?.protected;
  const reset = useMutation({
    mutationFn: () => serviceService.resetAdminPassword(serviceId),
    onSuccess: () => {
      setOpen(false);
      setTyped("");
      queryClient.invalidateQueries({ queryKey: key });
      toast.success(deploys ? `${app} has a new admin password, and ${service.name} is deploying to use it. It’s on Overview.` : `${app} has a new admin password. It’s on Overview.`);
      if (deploys) onDeploy();
    },
    onError: (err) => {
      const data = err instanceof ApiError ? (err.body as { data?: { code?: string; output?: string } } | null)?.data : undefined;
      if (data?.code === "reset_unconfirmed") queryClient.invalidateQueries({ queryKey: key }); // the new one waits on Overview
      setError(
        data?.code === "not_running"
          ? { text: `${service.name} isn’t running. Deploy it, then reset its password.` }
          : data?.code === "reset_failed"
            ? { text: `${app} refused the new password, so nothing changed. Check that its admin account still exists, then try again.${data.output ? ` ${app} said:` : ""}`, output: data.output }
            : data?.code === "reset_unconfirmed"
              ? { text: `${app} didn’t answer in time. It most likely has the new password, which waits on Overview: if it doesn’t sign in, reset again.` }
              : { text: "Couldn’t reset the password. Try again." },
      );
    },
  });

  if (!data?.reset) return null;
  const close = () => {
    if (reset.isPending) return;
    setOpen(false);
    setTyped("");
    setError(null);
  };
  // The password first, so its prompt never stacks on this dialog.
  const start = async () => {
    if (await confirmPassword(`Toft asks for your password before it resets ${app}’s admin password.`, { title: "Confirm it’s you", button: "Continue" })) setOpen(true);
  };

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-subtle pt-4">
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="text-[13px] text-fg">Admin login</span>
        <span className="text-xs leading-[17px] text-muted">
          {data.user ? (
            <>
              <span className="font-mono text-fg/85">{data.user}</span> signs in to {app}.{" "}
            </>
          ) : null}
          Reset gives the admin login a new password and stops the old one working.
        </span>
      </div>
      <button type="button" className={smallButton} onClick={start}>
        Reset admin password…
      </button>

      <Dialog open={open} onOpenChange={(o) => !o && close()}>
        <DialogContent className={cn(dialogContent, "max-w-[480px]")} showClose={false}>
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={cn(dialogTitle, "flex flex-wrap items-center gap-2")}>
              Reset {app}’s admin password?{env?.protected && <EnvChip env={env} size="md" />}
            </DialogTitle>
            <DialogDescription className={dialogText}>
              {deploys
                ? `Toft sets a new password and deploys ${service.name} now: ${app} uses it from that start. The old one then stops working, for you and for any app that signs in with it.`
                : `${app} gets a new password now. The old one stops working, for you and for any app that signs in with it.`}{" "}
              The new one waits on Overview, for you only, until you delete it or 7 days pass.
            </DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setError(null);
              reset.mutate();
            }}
          >
            {typeName && (
              <label className="flex flex-col gap-1.5 px-5 pt-4">
                <span className={fieldLabel}>
                  This environment is protected. Type <span className="font-mono text-fg">{service.name}</span> to {deploys ? "reset and deploy" : "reset"}.
                </span>
                <input className={fieldInput} value={typed} autoComplete="off" spellCheck={false} onChange={(e) => setTyped(e.target.value)} />
              </label>
            )}
            {error && (
              <div role="alert" className="mx-5 mt-3 flex flex-col gap-2 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] leading-[19px] text-status-failed-fg">
                <p>{error.text}</p>
                {error.output && <pre role="group" tabIndex={0} aria-label="What the command printed" className="max-h-32 overflow-auto whitespace-pre-wrap break-words font-mono text-xs">{error.output}</pre>}
              </div>
            )}
            <div className={dialogFooter}>
              <button type="button" className={cancelButton} autoFocus disabled={reset.isPending} onClick={close}>
                Cancel
              </button>
              <button type="submit" className={primaryButton} disabled={reset.isPending || (typeName && typed.trim() !== service.name)}>
                {reset.isPending ? "Resetting…" : deploys ? "Reset and deploy" : "Reset password"}
              </button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
