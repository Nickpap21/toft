"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Plus, Trash2 } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { dnsProviderService } from "@/lib/api/server.service";
import { cn, formatDateTime } from "@/lib/utils";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  primaryButton,
  smallButton,
} from "@/components/settings/settings-frame";
import { btnSm, ghostSm } from "@/app/projects/_lib/ui";
import { Row, settingsHead, titleRow } from "@/app/servers/[id]/aside";
import { dnsProviderName } from "@/app/servers/[id]/server-view";

type Row = { name: string; value: string };

const message = (err: unknown, fallback: string) => (err instanceof ApiError && err.status < 500 ? err.message : fallback);

/** A server's DNS provider, which wildcard domains (*.example.com) on it
 *  need (board: Server, DNS provider). Owners set it; everyone sees the
 *  provider and its variable names, never their values. */
export function DNSProviderCard({ serverId, serverName, canManage, provisionedAt }: { serverId: string; serverName: string; canManage: boolean; provisionedAt?: string | null }) {
  const queryClient = useQueryClient();
  const queryKey = ["dns-provider", serverId];
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [provider, setProvider] = useState("");
  const [rows, setRows] = useState<Row[]>([{ name: "", value: "" }]);

  const { data, error, isLoading } = useQuery({
    queryKey,
    queryFn: () => dnsProviderService.get(serverId),
    retry: false,
  });
  const none = error instanceof ApiError && error.status === 404;

  const save = useMutation({
    mutationFn: () =>
      dnsProviderService.put(
        serverId,
        provider.trim(),
        Object.fromEntries(rows.filter((r) => r.name.trim()).map((r) => [r.name.trim(), r.value])),
      ),
    onSuccess: (saved) => {
      queryClient.setQueryData(queryKey, saved);
      setEditing(false);
      setRows([{ name: "", value: "" }]);
      toast.success("DNS provider saved. Re-provision the server to apply it.");
    },
  });
  const remove = useMutation({
    mutationFn: () => dnsProviderService.delete(serverId),
    onSuccess: () => {
      setConfirming(false);
      queryClient.removeQueries({ queryKey });
      queryClient.invalidateQueries({ queryKey });
      toast.success("DNS provider removed. Re-provision the server to remove its credentials there.");
    },
    onError: (err) => toast.error(message(err, "Failed to remove the DNS provider")),
  });

  function startEditing() {
    setProvider(data?.provider ?? "");
    setRows((data?.credential_keys ?? [""]).map((name) => ({ name, value: "" })));
    save.reset();
    setEditing(true);
  }

  // Provisioning gives Traefik the saved provider: applied once a run
  // finished after the save.
  const applied = !!data && !!provisionedAt && new Date(provisionedAt) >= new Date(data.updated_at);

  return (
    <section aria-labelledby="dns-h" className={card}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="dns-h" className="text-[15px] font-semibold leading-[22px] text-fg">DNS provider</h2>
          <span className="text-xs text-muted">for wildcard certificates</span>
        </div>
        {canManage && !editing && !isLoading && (data || none) && (
          <span className="flex flex-wrap items-center gap-1.5">
            {data && (
              <button type="button" className={ghostSm} onClick={() => setConfirming(true)}>
                Remove…
              </button>
            )}
            <button type="button" className={btnSm} onClick={startEditing}>
              {data ? "Replace" : "Set up"}
            </button>
          </span>
        )}
      </div>

      {isLoading ? (
        <p className="px-4 py-3 text-[13px] text-muted">Loading…</p>
      ) : error && !none ? (
        <p role="alert" className="px-4 py-3 text-[13px] text-status-failed-fg">The DNS provider could not be loaded.</p>
      ) : !editing ? (
        data ? (
          <dl>
            <Row k="Provider" first>
              <span className="font-mono text-[13px]">{data.provider}</span>
            </Row>
            <Row k="Variables">
              <span className="min-w-0">
                <span className="font-mono text-[13px] [overflow-wrap:anywhere]">{data.credential_keys.join(", ")}</span>{" "}
                <span className="text-muted">· {data.credential_keys.length === 1 ? "value" : "values"} hidden</span>
              </span>
            </Row>
            <Row k="Applied">
              <span className={cn("inline-flex items-center gap-1.5", applied ? "text-fg" : "text-status-pending-fg")}>
                {applied ? (
                  <>
                    <Check className="h-3.5 w-3.5 flex-none text-status-running" aria-hidden="true" />
                    <span>
                      by the provisioning run of <span className="whitespace-nowrap font-mono text-xs">{formatDateTime(provisionedAt!).slice(0, 10)}</span>
                    </span>
                  </>
                ) : (
                  <>
                    <AlertTriangle className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
                    Not yet: Re-provision to give it to Traefik
                  </>
                )}
              </span>
            </Row>
          </dl>
        ) : (
          <p className="px-4 py-3 text-[13px] text-fg/70">
            No DNS provider: a deploy with a wildcard domain (<span className="font-mono text-xs">*.example.com</span>) is refused on this server.
          </p>
        )
      ) : (
        <form
          className="flex flex-col gap-3 px-4 pb-4 pt-3"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <p className="text-xs leading-[18px] text-muted">
            Wildcard domains (<span className="font-mono">*.example.com</span>) here need a certificate from a DNS-01 challenge, so the server needs your
            DNS provider’s API credentials. Any{" "}
            <a href="https://go-acme.github.io/lego/dns/" target="_blank" rel="noreferrer" className="inline-link">
              lego provider
            </a>{" "}
            works.
          </p>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="dns-provider" className={fieldLabel}>Provider</label>
            <input
              id="dns-provider"
              value={provider}
              onChange={(e) => setProvider(e.target.value.toLowerCase())}
              placeholder="cloudflare"
              className={cn(fieldInput, "font-mono")}
              required
            />
          </div>
          <fieldset className="flex flex-col gap-2">
            <legend className={cn(fieldLabel, "mb-0.5")}>Variables</legend>
            <p className="text-xs text-muted">Saving replaces all of them. Values are never shown again.</p>
            {rows.map((row, i) => (
              <div key={i} className="flex gap-2">
                <input
                  aria-label={`Variable ${i + 1} name`}
                  value={row.name}
                  onChange={(e) => setRows(rows.map((r, j) => (j === i ? { ...r, name: e.target.value.toUpperCase() } : r)))}
                  placeholder="CF_DNS_API_TOKEN"
                  className={cn(fieldInput, "min-w-0 flex-1 font-mono text-xs")}
                />
                <input
                  aria-label={`Variable ${i + 1} value`}
                  type="password"
                  value={row.value}
                  placeholder="New value"
                  onChange={(e) => setRows(rows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
                  className={cn(fieldInput, "min-w-0 flex-1 font-mono text-xs")}
                />
                <button
                  type="button"
                  aria-label={`Remove variable ${i + 1}`}
                  className="flex h-9 w-9 flex-none items-center justify-center rounded-lg text-fg/70 hover:bg-raised hover:text-fg"
                  onClick={() => setRows(rows.length > 1 ? rows.filter((_, j) => j !== i) : [{ name: "", value: "" }])}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                </button>
              </div>
            ))}
            <button type="button" className={cn(smallButton, "self-start")} onClick={() => setRows([...rows, { name: "", value: "" }])}>
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
              Add variable
            </button>
          </fieldset>
          <p className="text-xs text-muted">
            Applied at the next provisioning run. Example: <span className="font-mono">cloudflare</span> with a{" "}
            <span className="font-mono">CF_DNS_API_TOKEN</span> that can edit the zone’s DNS.
          </p>
          {save.error && (
            <p role="alert" className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
              {message(save.error, "The DNS provider was not saved.")}
            </p>
          )}
          <div className="flex gap-2">
            <button type="submit" className={primaryButton} disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</button>
            <button type="button" className={cancelButton} onClick={() => setEditing(false)}>Cancel</button>
          </div>
        </form>
      )}

      {data && (
        <Dialog open={confirming} onOpenChange={(o) => !remove.isPending && setConfirming(o)}>
          <DialogContent className={dialogContent}>
            <DialogHeader className={dialogHeader}>
              <DialogTitle className={dialogTitle}>
                Remove {dnsProviderName(data.provider) ?? <span className="font-mono font-medium">{data.provider}</span>} from{" "}
                <span className="font-mono font-medium">{serverName}</span>?
              </DialogTitle>
              <DialogDescription className={dialogText}>
                Deploys with a wildcard domain are refused here from now on. The next re-provision takes the credentials off the server, and its wildcard
                certificates stop renewing.
              </DialogDescription>
            </DialogHeader>
            <div className={dialogFooter}>
              <button type="button" className={cancelButton} autoFocus disabled={remove.isPending} onClick={() => setConfirming(false)}>
                Cancel
              </button>
              <button type="button" className={dangerButton} disabled={remove.isPending} onClick={() => remove.mutate()}>
                {remove.isPending ? "Removing…" : "Remove DNS provider"}
              </button>
            </div>
          </DialogContent>
        </Dialog>
      )}
    </section>
  );
}
