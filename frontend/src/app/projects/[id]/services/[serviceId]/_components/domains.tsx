"use client";

import type { ReactNode } from "react";
import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Clock, Copy, ExternalLink, Layers, X } from "lucide-react";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { domainService, type Domain, type DomainCheck } from "@/lib/api/domain.service";
import type { Service } from "@/lib/api/service.service";
import type { Server } from "@/lib/api/server.service";
import { ApiError } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { isStoppable } from "@/lib/status";
import { TEMPORARY_NOTE, TemporaryChip, domainURL } from "@/components/ui/temporary-chip";

// Settings → Domains (board: private/design/canvas/ReworkServiceSettings.dc.html,
// D-155): the card's body below its header. Every domain with its DNS and
// certificate, looked up when the page opens (GET …/domains/check), the port
// and routing fields (children), and where to point DNS.

const mono = "font-mono text-xs";
const help = "text-xs leading-[17px] text-muted";
const outline = "inline-flex h-[30px] items-center gap-1.5 rounded-md border border-subtle bg-raised px-2.5 text-xs font-medium text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50 pointer-coarse:min-h-11";
const ghost = "inline-flex h-[30px] items-center justify-center gap-1.5 rounded-md px-2.5 text-xs font-medium text-muted hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:min-h-11";

interface DomainsProps {
  service: Service;
  serviceId: string;
  server?: Server;
  canDeploy: boolean;
  deployLabel: string;
  deployBusy: boolean;
  onDeploy: () => void;
  /** Its environment is protected: onDeploy confirms first (D-55). */
  deployConfirms?: boolean;
  /** The add dialog, opened by the card's Add domain. */
  adding: boolean;
  onAdding: (open: boolean) => void;
  /** The port and routing fields, in the card's body. */
  children: ReactNode;
}

export function Domains({ service, serviceId, server, canDeploy, deployLabel, deployBusy, onDeploy, deployConfirms, adding, onAdding, children }: DomainsProps) {
  const queryClient = useQueryClient();
  const isCompose = service.source_type === "docker-compose";
  const name = service.name;
  const serverName = server?.name ?? "the panel host";
  const target = isCompose
    ? `${service.compose_route_service || "its only service"}:${service.port}`
    : service.network_alias
      ? `${service.network_alias}:${service.port}`
      : `port ${service.port}`;
  const [hostname, setHostname] = useState("");
  const [removing, setRemoving] = useState<Domain | null>(null);

  const listRead = useQuery({
    queryKey: ["domains", serviceId],
    queryFn: () => domainService.list(serviceId),
  });
  const { data: domains, isPending: listPending, refetch: refetchList } = listRead;
  const { data: check, isFetching: checking, refetch, dataUpdatedAt: checkedAt } = useQuery({
    queryKey: ["domain-check", serviceId],
    queryFn: () => domainService.check(serviceId),
    enabled: service.port > 0,
    refetchOnWindowFocus: false,
    retry: false,
  });
  const facts = new Map((check?.domains ?? []).map((d) => [d.id, d]));
  const address = check?.server.address ?? "";
  const running = isStoppable(service.runtime_status);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["domains", serviceId] });
    queryClient.invalidateQueries({ queryKey: ["domain-check", serviceId] });
  };
  const close = () => {
    onAdding(false);
    setHostname("");
  };
  // The variable: Add and deploy (D-145), which deploys once it is added.
  const create = useMutation<Domain, Error, boolean>({
    mutationFn: () => domainService.create(serviceId, hostname.trim()),
    onSuccess: (_d, deploy) => {
      toast.success(`${hostname.trim().toLowerCase()} added${deploy ? "" : `: it is routed from ${name}'s next deploy`}`);
      close();
      invalidate();
      if (deploy) onDeploy();
    },
    onError: (err: unknown) => toast.error(err instanceof ApiError ? err.message : "Failed to add the domain"),
  });
  const remove = useMutation({
    mutationFn: (d: Domain) => domainService.delete(serviceId, d.id),
    onSuccess: (_v, d) => {
      toast.success(`${d.hostname} removed`);
      setRemoving(null);
      invalidate();
    },
    onError: (err: unknown) => toast.error(err instanceof ApiError ? err.message : "Failed to remove the domain"),
  });

  // What waits for the next deploy: domains not routed yet, and names still
  // routed though removed. Only known while something runs.
  const added = check?.routing_known ? (domains ?? []).filter((d) => !facts.get(d.id)?.routed) : [];
  const removed = check?.routing_known ? check.routed_removed : [];
  const changes = added.length + removed.length;

  const wildcard = hostname.trim().startsWith("*.");
  const list = domains ?? [];

  return (
    <>
      {running && changes > 0 && (
        <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-subtle bg-status-pending-soft/40 px-4 py-3">
          <Clock className="h-4 w-4 flex-none text-status-pending-fg" aria-hidden="true" />
          <p className="min-w-0 flex-[1_1_240px] text-[13px] leading-5 text-fg">
            <span className="font-semibold">
              {changes === 1 ? "1 change waits" : `${changes} changes wait`} for the next deploy:
            </span>{" "}
            {[
              ...added.map((d) => <><span className={mono}>{d.hostname}</span> is added</>),
              ...removed.map((h) => <><span className={mono}>{h}</span> is removed</>),
            ].map((el, i, all) => (
              <span key={i}>
                {el}
                {i < all.length - 2 ? ", " : i === all.length - 2 ? " and " : ". "}
              </span>
            ))}
            Until {name} deploys, Traefik{" "}
            {added.length > 0 && removed.length === 0
              ? `doesn't route ${added.length === 1 ? "it" : "them"}`
              : removed.length > 0 && added.length === 0
              ? `still routes ${removed.length === 1 ? "it" : "them"}`
              : "routes the names of the live deploy"}.
          </p>
          {canDeploy && (
            <button type="button" className={outline} disabled={deployBusy} onClick={onDeploy}>
              <Layers className="h-3.5 w-3.5" aria-hidden="true" />
              {deployBusy ? "Deploying…" : deployLabel}
            </button>
          )}
        </div>
      )}

      {readFailed(listRead) ? (
        <div className="border-b border-subtle p-4">
          <LoadFailed what={`the domains of ${name}`} onRetry={() => void refetchList()} />
        </div>
      ) : listPending ? (
        <div className="h-24 border-b border-subtle bg-raised/40 motion-safe:animate-pulse" />
      ) : list.length === 0 ? (
        service.port > 0 && (
          <div className="flex flex-col gap-1 border-b border-subtle px-4 py-4">
            <p className="text-[13px] text-fg">No domains yet.</p>
            <p className="max-w-[560px] text-[13px] leading-5 text-muted">
              Add a name such as <span className={mono}>app.example.com</span>, point its DNS at {serverName}, and deploy:
              Traefik routes it to {name} and gets its certificate.
            </p>
          </div>
        )
      ) : (
        <div className="border-b border-subtle">
          {/* Phones: each row is a card, the domain over its status and action. */}
          <table className="w-full border-collapse text-[13px] max-sm:block">
            <thead className="max-sm:hidden">
              <tr className="text-left text-xs text-muted">
                <th scope="col" className="border-b border-subtle px-4 py-2 font-medium">Domain</th>
                <th scope="col" className="border-b border-subtle px-4 py-2 font-medium">Status</th>
                <th scope="col" className="border-b border-subtle px-4 py-2"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody className="max-sm:block">
              {list.map((d) => {
                const f = facts.get(d.id);
                const isWild = d.hostname.startsWith("*.");
                return (
                  <tr
                    key={d.id}
                    className="border-t border-subtle first:border-t-0 max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:items-center max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3"
                  >
                    <td className="px-4 py-2.5 align-middle max-sm:col-span-2 max-sm:p-0">
                      <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <span className={cn(mono, "break-all text-fg")}>{d.hostname}</span>
                        {isWild && <span className="text-xs text-muted">wildcard</span>}
                        {d.temporary && <TemporaryChip />}
                      </span>
                      <span className="mt-0.5 block text-xs text-muted">
                        Added{d.created_by ? ` by ${d.created_by}` : ""} on {d.created_at.slice(0, 10)}
                        {f?.routed && d.routed_at ? ` · routed since ${d.routed_at.slice(0, 10)}` : ""}
                      </span>
                    </td>
                    <td className="px-4 py-2.5 align-middle max-sm:p-0">
                      <DomainStatus f={f} d={d} checking={checking && !f} known={!!check?.routing_known} running={running} address={address} now={checkedAt} />
                    </td>
                    <td className="whitespace-nowrap px-4 py-2 text-right align-middle max-sm:self-start max-sm:p-0">
                      <span className="inline-flex gap-1">
                        {!isWild && f?.routed && (
                          <a
                            href={domainURL(d)}
                            target="_blank"
                            rel="noreferrer"
                            aria-label={`Open ${d.hostname} in a new tab`}
                            className={cn(ghost, "w-[30px] px-0 pointer-coarse:min-w-11")}
                          >
                            <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                          </a>
                        )}
                        {canDeploy && (
                          <button type="button" aria-label={`Remove ${d.hostname}`} onClick={() => setRemoving(d)} className={ghost}>
                            Remove
                          </button>
                        )}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {service.port > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-subtle px-4 py-2.5 text-xs text-muted">
              <span className="min-w-0 flex-[1_1_320px]">
                Adding or removing a domain takes effect at {name}&apos;s next deploy. DNS and certificates are checked when this page
                opens{check ? <>: last at {new Date(check.checked_at).toTimeString().slice(0, 5)}.</> : checking ? ": checking…" : "."}
                {list.some((d) => d.temporary) && ` A temporary name under sslip.io is ${TEMPORARY_NOTE.charAt(0).toLowerCase()}${TEMPORARY_NOTE.slice(1)}`}
              </span>
              <button type="button" className={outline} disabled={checking} onClick={() => refetch()}>
                {checking ? "Checking…" : "Check again"}
              </button>
            </div>
          )}
        </div>
      )}

      <div className="flex flex-col gap-4 p-4">
        {service.port === 0 && (
          <div className="flex flex-col gap-1">
            <p className="text-[13px] font-medium text-fg">{name} has no port, so it can&apos;t take a domain</p>
            <p className="text-[13px] leading-[19px] text-muted">
              Traefik routes a domain to the port a service listens on. {name} has none, which is right for a queue worker or a cron
              job: nothing reaches it from outside, and other services don&apos;t call it. To serve HTTP from {name}, set its port
              below and save, then add a domain.
            </p>
          </div>
        )}
        {children}
        {service.port > 0 && (
          <div className="flex flex-col gap-3 border-t border-subtle pt-4">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
              <span className="text-[13px] font-medium text-fg">Point DNS here</span>
              {address ? (
                <div className="flex min-w-0 max-w-[360px] flex-1 items-stretch overflow-hidden rounded-lg border border-strong/70 pointer-coarse:min-h-11">
                  <span className="flex items-center border-r border-subtle bg-raised px-2.5 text-xs text-muted">A record</span>
                  <span className="flex flex-1 items-center px-2.5 py-2 font-mono text-[13px] text-fg">{address}</span>
                  <button
                    type="button"
                    aria-label="Copy the address"
                    onClick={() => { navigator.clipboard?.writeText(address); toast.success("Address copied"); }}
                    className="flex w-9 items-center justify-center border-l border-subtle text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus pointer-coarse:w-11"
                  >
                    <Copy className="h-3.5 w-3.5" aria-hidden="true" />
                  </button>
                </div>
              ) : (
                <span className="text-[13px] leading-5 text-muted">
                  {checking ? "Looking up the address…" : `The panel can't tell ${serverName}'s public address: it is the one this panel's own domain points at.`}
                </span>
              )}
            </div>
            {service.server_id && check && (
              <div className="flex flex-col gap-1.5 text-[13px]">
                {check.server.wildcards_ok ? (
                  <>
                    <span className="text-fg/85">
                      Wildcards: DNS-01 through {serverName}&apos;s DNS provider
                      {check.server.dns_provider ? <>, <span className={mono}>{check.server.dns_provider}</span></> : ""}.
                    </span>
                    {check.server.owner && check.server.dns_provider_saved_at && (
                      check.server.provisioned_at && check.server.provisioned_at >= check.server.dns_provider_saved_at ? (
                        <span className="flex items-start gap-1.5 text-xs text-muted">
                          <Check className="mt-0.5 h-3 w-3 flex-none text-status-running-fg" aria-hidden="true" />
                          <span>Applied by the provisioning run of <span className="font-mono">{check.server.provisioned_at.slice(0, 10)}</span></span>
                        </span>
                      ) : (
                        <span className="flex items-start gap-1.5 text-xs text-status-pending-fg">
                          <AlertTriangle className="mt-0.5 h-3 w-3 flex-none" aria-hidden="true" />
                          <span>Saved on {check.server.dns_provider_saved_at.slice(0, 10)}, not provisioned since: Re-provision {serverName} to apply it.</span>
                        </span>
                      )
                    )}
                  </>
                ) : (
                  <span className={cn("flex items-start gap-1.5", list.some((d) => d.hostname.startsWith("*.")) ? "text-status-pending-fg" : "text-fg/85")}>
                    {list.some((d) => d.hostname.startsWith("*.")) && <AlertTriangle className="mt-0.5 h-3.5 w-3.5 flex-none" aria-hidden="true" />}
                    <span>Wildcards: {serverName} has no DNS provider, so a wildcard can&apos;t get a certificate there.</span>
                  </span>
                )}
                {check.server.owner && (
                  <Link href={`/servers/${service.server_id}`} className="self-start text-[13px] text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
                    DNS provider on {serverName}
                  </Link>
                )}
              </div>
            )}
            <details>
              <summary className="cursor-pointer text-xs text-muted hover:text-fg pointer-coarse:min-h-11">
                How DNS and certificates work
              </summary>
              <div className="mt-2.5 grid gap-x-6 gap-y-3 md:grid-cols-2">
                <p className={help}>
                  Create an A record with the address above for each name, and one record for <span className={mono}>*.name</span>{" "}
                  for a wildcard. A name reaches {name} once its record points at {serverName} and {name} has deployed with it.
                </p>
                <div className="flex flex-col gap-2">
                  <dl className="flex flex-col gap-1.5 text-xs">
                    <div className="grid grid-cols-[84px_1fr] gap-3">
                      <dt className="text-muted">Exact names</dt>
                      <dd className="text-fg/85">HTTP-01 from Let&apos;s Encrypt, by Traefik on {serverName}, when a deploy adds the route. Nothing to set up.</dd>
                    </div>
                    <div className="grid grid-cols-[84px_1fr] gap-3">
                      <dt className="text-muted">Wildcards</dt>
                      <dd className="text-fg/85">
                        {service.server_id
                          ? "DNS-01, through the server's DNS provider."
                          : "Not on the panel host: wildcards run on an added server with a DNS provider."}
                      </dd>
                    </div>
                  </dl>
                  <p className={help}>
                    An exact name on this server always wins over a wildcard. Without a DNS provider, a deploy with a wildcard is
                    refused before anything changes. Wildcards don&apos;t run on the control-plane server.
                  </p>
                </div>
              </div>
            </details>
          </div>
        )}
      </div>

      <Dialog open={adding} onOpenChange={(o) => { if (!create.isPending && !o) close(); }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Add a domain to {name}</DialogTitle>
            <DialogDescription>
              Traefik on {serverName} starts routing it to {target} at {name}&apos;s next deploy, and asks Let&apos;s Encrypt for its
              certificate at that deploy, so point the record here first.
            </DialogDescription>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={(e) => { e.preventDefault(); if (hostname.trim()) create.mutate(canDeploy && !deployBusy); }}
          >
            <label htmlFor="add-domain" className="text-[13px] font-medium text-fg">Domain</label>
            <Input
              id="add-domain"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              value={hostname}
              onChange={(e) => setHostname(e.target.value)}
              aria-describedby="add-help"
              className="font-mono"
            />
            <p id="add-help" className="text-xs leading-[17px] text-muted">
              An exact name, such as <span className={mono}>app.example.com</span>, or a wildcard, <span className={mono}>*.example.com</span>,
              which routes every name one level below it that has no route of its own. Saved in lower case.
            </p>
            {hostname.trim() && (
              <dl className="flex flex-col gap-2 rounded-lg border border-subtle bg-raised/50 p-3 text-[13px]">
                <div className="grid grid-cols-[84px_1fr] gap-2">
                  <dt className="text-muted">DNS</dt>
                  <dd className="text-fg/85">
                    Create an A record for <span className={mono}>{hostname.trim().toLowerCase()}</span>
                    {address ? <> pointing at <span className={mono}>{address}</span></> : ` pointing at ${serverName}`}.
                  </dd>
                </div>
                <div className="grid grid-cols-[84px_1fr] gap-2">
                  <dt className="text-muted">Certificate</dt>
                  <dd className={cn(wildcard && !check?.server.wildcards_ok ? "text-status-pending-fg" : "text-fg/85")}>
                    {!wildcard
                      ? "HTTP-01 at the deploy: nothing to set up."
                      : check?.server.wildcards_ok
                      ? <>A wildcard needs DNS-01. {serverName} has a DNS provider{check.server.dns_provider ? <>, <span className={mono}>{check.server.dns_provider}</span></> : ""}.</>
                      : `A wildcard needs DNS-01, and ${serverName} has no DNS provider: deploys with it are refused until it has one.`}
                  </dd>
                </div>
              </dl>
            )}
            <DialogFooter className="mt-1">
              <Button type="button" variant="ghost" className="sm:mr-auto" onClick={close} disabled={create.isPending}>Cancel</Button>
              {canDeploy && !deployBusy ? (
                <>
                  <Button type="button" variant="outline" onClick={() => create.mutate(false)} disabled={!hostname.trim() || create.isPending}>Add only</Button>
                  <Button type="submit" disabled={!hostname.trim() || create.isPending}>
                    {create.isPending ? "Adding…" : `Add and deploy ${name}${deployConfirms ? "…" : ""}`}
                  </Button>
                </>
              ) : (
                <Button type="submit" disabled={!hostname.trim() || create.isPending}>{create.isPending ? "Adding…" : "Add domain"}</Button>
              )}
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={!!removing} onOpenChange={(o) => { if (!o && !remove.isPending) setRemoving(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove {removing?.hostname} from {name}?</DialogTitle>
            <DialogDescription>
              {removing && facts.get(removing.id)?.routed
                ? `Traefik stops routing ${removing.hostname.startsWith("*.") ? "names under it" : "it"} at ${name}'s next deploy. Until then requests to ${removing.hostname.startsWith("*.") ? "them" : "it"} still reach ${name}.`
                : `It isn't routed yet, so nothing changes for anyone: it won't be added at ${name}'s next deploy.`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRemoving(null)} disabled={remove.isPending}>Cancel</Button>
            <Button variant="destructive" onClick={() => removing && remove.mutate(removing)} disabled={remove.isPending}>
              {remove.isPending ? "Removing…" : "Remove domain"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

type Fact = DomainCheck["domains"][number] | undefined;

function Mark({ ok, text }: { ok: boolean; text: string }) {
  return (
    <span className={cn("inline-flex items-center gap-1", ok ? "text-status-running-fg" : "text-muted")}>
      {ok ? <Check className="h-3 w-3" aria-hidden="true" /> : <X className="h-3 w-3" aria-hidden="true" />}
      {text}
    </span>
  );
}

/** The board's Status column: DNS and TLS marks, or "Waits for the next
 *  deploy", and below them what is wrong, when something is. */
function DomainStatus({ f, d, checking, known, running, address, now }: { f: Fact; d: Domain; checking: boolean; known: boolean; running: boolean; address: string; now: number }) {
  if (checking) return <span className="text-xs text-muted motion-safe:animate-pulse">Checking…</span>;
  if (!f) return <span className="text-xs text-muted">—</span>;
  if (running && known && !f.routed)
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-status-pending-fg">
        <Clock className="h-3 w-3" aria-hidden="true" />
        Waits for the next deploy
      </span>
    );
  const v4 = f.addresses.filter((a) => !a.includes(":"));
  const until = f.not_after?.slice(0, 10);
  const dns =
    f.dns === "here"
      ? ""
      : f.dns === "elsewhere"
      ? `DNS points at ${v4.join(", ")}${address && !d.temporary ? `; needs A ${address}` : ""}`
      : f.dns === "none"
      ? `No DNS record yet${address ? `: needs A ${address}` : ""}`
      : f.addresses.length
      ? `DNS can't be compared: A ${f.addresses.join(", ")}`
      : "DNS lookup failed: try Check again";
  const tls =
    f.certificate === "valid"
      ? ""
      : f.certificate === "invalid"
      ? `${f.not_after && new Date(f.not_after).getTime() < now ? "Certificate expired" : "Certificate doesn't verify"}${until ? `, expires ${until}` : ""}${f.issuer ? ` · ${f.issuer}` : ""}`
      : f.certificate === "unknown"
      ? `Certificate couldn't be read: ${address ? "the server didn't answer on 443" : "the server's address is unknown"}`
      : running && f.routed && d.temporary
      ? "Certificate pending: answers over HTTP meanwhile"
      : `No certificate yet: ${!running ? "the service isn't running" : f.routed ? "Let's Encrypt hasn't issued it, check the DNS" : "after DNS and the next deploy"}`;
  const problems = [dns, tls, !running || !known ? `Not running: routed once ${running ? "it runs" : "deployed"}` : ""].filter(Boolean);
  return (
    <span className="flex flex-col gap-0.5 text-xs">
      <span className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
        <Mark ok={f.dns === "here"} text={d.temporary && f.dns === "here" ? "DNS (sslip.io)" : "DNS"} />
        <Mark ok={f.certificate === "valid"} text="TLS" />
        {f.certificate === "valid" && until && <span className="text-muted">until {until}</span>}
      </span>
      {problems.map((p) => (
        <span key={p} className={cn("leading-4", f.certificate === "invalid" && p === tls ? "text-status-failed-fg" : "text-muted")}>
          {p}
        </span>
      ))}
    </span>
  );
}

