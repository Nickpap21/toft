"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, CheckCircle2, RefreshCw, XCircle } from "lucide-react";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import {
  cancelButton, dangerButton, dialogContent, dialogHeader, dialogText, dialogTitle, primaryButton,
} from "@/components/settings/settings-frame";
import { ApiError } from "@/lib/api/client";
import { serverService, type HostKeyChanged, type HostKeyScan, type Server } from "@/lib/api/server.service";
import { useReauth } from "@/components/reauth";

/** The password confirmation was cancelled: nothing to report. */
class Cancelled extends Error {}

/** The public host key file on the server for a key type, for ssh-keygen -lf. */
export function hostKeyFile(keyType: string | null | undefined): string {
  if (keyType?.startsWith("ecdsa-")) return "/etc/ssh/ssh_host_ecdsa_key.pub";
  if (keyType === "ssh-rsa") return "/etc/ssh/ssh_host_rsa_key.pub";
  return "/etc/ssh/ssh_host_ed25519_key.pub";
}

interface HostKeyDialogProps {
  server: Server;
  onOpenChange: (open: boolean) => void;
}

/**
 * Verify or replace a server's pinned SSH host key (instance operators only).
 * Mount it only while open: it scans the server when it mounts.
 */
export function HostKeyDialog({ server, onOpenChange }: HostKeyDialogProps) {
  const queryClient = useQueryClient();
  const reauth = useReauth();
  const [confirmed, setConfirmed] = useState(false);

  const scan = useQuery({
    queryKey: ["server-host-key-scan", server.id],
    queryFn: () => serverService.scanHostKey(server.id),
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });

  const reset = useMutation({
    // Replacing the pin needs the operator's password confirmed (D-53).
    mutationFn: async (fingerprint: string) => {
      if (!(await reauth())) throw new Cancelled();
      return serverService.resetHostKey(server.id, fingerprint);
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(["server", server.id], updated);
      queryClient.invalidateQueries({ queryKey: ["server", server.id] });
      queryClient.invalidateQueries({ queryKey: ["servers"] });
      toast.success(
        server.host_key_fingerprint ? "SSH host key replaced" : "SSH host key pinned",
        { description: `${updated.host_key_type ?? ""} ${updated.host_key_fingerprint ?? ""}`.trim() },
      );
      onOpenChange(false);
    },
    onError: (err: Error) => {
      if (err instanceof Cancelled) return;
      if (err instanceof ApiError && err.status === 409) {
        const changed = (err.body as { data?: HostKeyChanged } | undefined)?.data;
        toast.error("The server's key changed since the scan", {
          description: changed
            ? `It now presents ${changed.key_type} ${changed.presented_fingerprint}. Compare it again before pinning.`
            : err.message,
        });
        setConfirmed(false);
        scan.refetch();
        return;
      }
      toast.error(err.message || "Failed to pin the SSH host key");
    },
  });

  const result: HostKeyScan | undefined = scan.data;
  const scanning = scan.isFetching;
  const busy = scanning || reset.isPending;
  const pinned = result?.pinned_fingerprint ?? null;
  // A pin date without a fingerprint: the stored key cannot be parsed.
  const unreadable = !server.host_key_fingerprint && !!server.host_key_pinned_at;
  const needsPin = !!result && !result.matches_pinned;

  const command = `ssh-keygen -lf ${hostKeyFile(result?.key_type)}`;
  const note = "flex items-start gap-2.5 rounded-lg px-3 py-2.5 text-[13px] leading-[19px]";
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className={cn(dialogContent, "max-w-[640px]")}>
        <DialogHeader className={dialogHeader}>
          <DialogTitle className={dialogTitle}>
            SSH host key of <span className="font-mono font-medium">{server.name}</span>
          </DialogTitle>
          <DialogDescription className={dialogText}>
            Every connection the panel makes must present the pinned key, or it is refused before any credential is sent. Pin a key only after
            checking it on the server itself.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3.5 px-5 pb-5 pt-1">
          <table className="w-full border-collapse text-[13px]">
            <tbody>
              <tr>
                <th scope="row" className="w-[104px] py-2 text-left align-top font-normal text-muted">Pinned</th>
                <td className="break-all py-2 font-mono text-xs leading-[18px] text-fg">
                  {pinned ? (
                    <><span className="text-muted">{result?.pinned_key_type}</span> {pinned}</>
                  ) : server.host_key_fingerprint ? (
                    <><span className="text-muted">{server.host_key_type}</span> {server.host_key_fingerprint}</>
                  ) : unreadable ? (
                    <span className="font-sans text-status-failed-fg">The stored key can’t be read</span>
                  ) : (
                    <span className="font-sans text-status-pending-fg">Not pinned yet</span>
                  )}
                </td>
              </tr>
              <tr>
                <th scope="row" className="border-t border-subtle py-2 text-left align-top font-normal text-muted">Presented now</th>
                <td className="break-all border-t border-subtle py-2 font-mono text-xs leading-[18px]">
                  {scanning ? (
                    <span className="font-sans text-muted">Scanning…</span>
                  ) : result ? (
                    <span className={pinned && !result.matches_pinned ? "text-status-failed-fg" : "text-fg"}>
                      <span className="text-muted">{result.key_type}</span> {result.presented_fingerprint}
                    </span>
                  ) : (
                    <span className="font-sans text-muted">—</span>
                  )}
                </td>
              </tr>
            </tbody>
          </table>

          {scan.isError && !scanning && (
            <p role="alert" className={cn(note, "bg-status-failed-soft text-status-failed-fg")}>
              <XCircle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
              <span>Could not reach the server: {(scan.error as Error).message}</span>
            </p>
          )}

          {result && !scanning && (
            result.matches_pinned ? (
              <p className={cn(note, "bg-status-running-soft text-status-running-fg")}>
                <CheckCircle2 className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
                The server presents the pinned key. Nothing to change.
              </p>
            ) : pinned ? (
              <p role="alert" className={cn(note, "bg-status-failed-soft text-status-failed-fg")}>
                <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
                <span>
                  The server presents a different key. If you did not rebuild it or regenerate its SSH host keys, keep the pin: someone may be
                  intercepting the connection. Until this is resolved, deploys to {server.name} fail.
                </span>
              </p>
            ) : unreadable ? (
              <p role="alert" className={cn(note, "bg-status-failed-soft text-status-failed-fg")}>
                <XCircle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
                The stored key can’t be read, so every connection to this server fails. Replace it with the key the server presents, after checking
                its fingerprint.
              </p>
            ) : (
              <p className={cn(note, "bg-status-pending-soft text-status-pending-fg")}>
                <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
                No key is pinned yet. Pin it now to check it yourself instead of trusting the first connection.
              </p>
            )
          )}

          {needsPin && !scanning && (
            <div className="flex flex-col gap-2.5 rounded-lg border border-subtle p-3">
              <p className="text-[13px] text-fg/85">On the server’s console (your provider’s web console, or a session you already trust), run:</p>
              <div className="flex items-center gap-2 rounded-md border border-subtle bg-ground py-1.5 pl-3 pr-1.5">
                <code role="group" tabIndex={0} aria-label="Command to run on the server" className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs text-fg">{command}</code>
                <button
                  type="button"
                  className="h-7 rounded-md border border-subtle bg-raised px-2.5 text-xs text-fg hover:bg-raised/70"
                  onClick={() => navigator.clipboard?.writeText(command)}
                >
                  Copy
                </button>
              </div>
              <p className="text-xs text-muted">
                Its <span className="font-mono">SHA256:</span> fingerprint must be exactly the one presented above.
              </p>
              <label className="flex min-h-6 cursor-pointer items-start gap-2.5 text-[13px]">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]"
                  checked={confirmed}
                  onChange={(e) => setConfirmed(e.target.checked)}
                  disabled={busy}
                />
                <span>The fingerprint matches what the server’s console shows</span>
              </label>
            </div>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t border-subtle px-5 py-3">
          <button
            type="button"
            className="inline-flex h-9 items-center gap-1.5 rounded-lg px-2.5 text-[13px] text-fg/70 hover:bg-raised hover:text-fg disabled:opacity-50"
            onClick={() => { setConfirmed(false); scan.refetch(); }}
            disabled={busy}
          >
            <RefreshCw className={cn("h-4 w-4", scanning && "motion-safe:animate-spin")} aria-hidden="true" />
            Scan again
          </button>
          <span className="flex-1" />
          <button type="button" className={cancelButton} onClick={() => onOpenChange(false)} disabled={reset.isPending}>
            {needsPin && pinned ? "Keep the pin" : needsPin ? "Cancel" : "Close"}
          </button>
          {needsPin && result && (
            <button
              type="button"
              className={pinned || unreadable ? dangerButton : primaryButton}
              disabled={busy || !confirmed}
              onClick={() => reset.mutate(result.presented_fingerprint)}
            >
              {reset.isPending ? "Pinning…" : pinned || unreadable ? "Replace pinned key" : "Pin this key"}
            </button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
