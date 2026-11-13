"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import Link from "next/link";
import { AlertTriangle } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import type { Environment } from "@/lib/api/environment.service";
import type { Server } from "@/lib/api/server.service";
import type { Service } from "@/lib/api/service.service";
import { previewService } from "@/lib/api/preview.service";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { dialogContent, dialogFooter, dialogHeader, dialogText, dialogTitle } from "@/components/settings/settings-frame";
import { cn } from "@/lib/utils";
import { card, field, ghostSm, h2, help, label, outline, primarySm, saveBar, sum } from "@/app/projects/_lib/ui";
import { addressExample, copiesLine, joinNames, previewRowText, registeredDomain } from "./view";

/** A project's previews (boards ProjectPreviews 1 to 5, D-97, and
 *  ReworkProjectSettings): on or off, the environment they copy, how many
 *  run at once, their domain and its wildcard record. Owners change it;
 *  developers read it. */
export function PreviewsSettingsCard({ projectId, projectName, environments, services, servers, isOwner }: {
  projectId: string;
  projectName: string;
  environments: Environment[] | undefined;
  services: Service[];
  servers: Server[];
  isOwner: boolean;
}) {
  const queryClient = useQueryClient();
  const settingsKey = ["preview-settings", projectId];
  const settingsRead = useQuery({ queryKey: settingsKey, queryFn: () => previewService.settings(projectId) });
  const { data: settings, refetch: refetchSettings } = settingsRead;
  const settingsFailed = readFailed(settingsRead);
  const own = (environments ?? []).filter((e) => !e.preview);
  const [draft, setDraft] = useState<{ on: boolean; env: string; domain: string; max: string } | null>(null);
  const saved = {
    on: !!settings?.environment_id,
    env: settings?.environment_id ?? "",
    domain: settings?.domain ?? "",
    max: String(settings?.max_per_project ?? 5),
  };
  const fallbackEnv = own.find((e) => e.name === "staging")?.id ?? own[0]?.id ?? "";
  const cur = draft ?? saved;
  const env = cur.env || fallbackEnv;
  const max = Number(cur.max);
  const maxErr = /^\d+$/.test(cur.max.trim()) && max >= 1 && max <= 20 ? "" : "1 to 20 at once.";
  const changes = settings
    ? [
        cur.on !== saved.on ? "Previews" : "",
        cur.on && saved.on && env !== saved.env ? "Copies" : "",
        cur.on && cur.max.trim() !== saved.max ? "At once" : "",
        cur.on && cur.domain.trim() !== saved.domain ? "Domain" : "",
      ].filter(Boolean)
    : [];
  const changed = changes.length > 0;
  const copied = own.find((e) => e.id === env);
  const envServices = services.filter((s) => s.environment_id === env);
  const server = servers.find((s) => s.id === envServices[0]?.server_id);
  const serverLabel = (e: Environment) => {
    const first = services.find((s) => s.environment_id === e.id);
    const srv = servers.find((s) => s.id === first?.server_id);
    return [e.name, first ? (srv?.name ?? "the panel's server") : "", e.protected ? "protected" : ""].filter(Boolean).join(" · ");
  };

  const openRead = useQuery({
    queryKey: ["project-previews", projectId],
    queryFn: () => previewService.list(projectId).then((p) => p.data),
  });
  const { data: open, refetch: refetchOpen } = openRead;
  const openFailed = readFailed(openRead);
  const removable = (open ?? []).filter((p) => p.status !== "removing");
  const started = removable.filter((p) => p.environment);
  const waiting = removable.length - started.length;
  const approvals = removable.filter((p) => p.status === "needs_approval").length;
  const removes = saved.on && (!cur.on || env !== saved.env);
  const willRemove = removes && removable.length > 0;
  const headSum = !saved.on
    ? "off"
    : [`${open ? started.length : "—"} open`, approvals > 0 ? `${approvals} waiting for approval` : ""].filter(Boolean).join(" · ");

  const check = useQuery({
    queryKey: ["preview-domain-check", projectId, saved.domain, saved.env],
    queryFn: () => previewService.domainCheck(projectId),
    enabled: saved.on && !!saved.domain,
    retry: false,
    meta: { quietFailure: "The record's check: without an answer the help says what the record must be, which holds either way." },
  });

  const [confirming, setConfirming] = useState(false);
  const save = useMutation({
    mutationFn: () =>
      previewService.saveSettings(projectId, cur.on ? env : null, cur.on && cur.domain.trim() ? cur.domain.trim().toLowerCase() : null,
        cur.on ? max : undefined),
    onSuccess: (s) => {
      queryClient.setQueryData(settingsKey, s);
      queryClient.invalidateQueries({ queryKey: ["project-previews", projectId] });
      queryClient.invalidateQueries({ queryKey: ["preview-domain-check", projectId] });
      setDraft(null);
      setConfirming(false);
      toast.success(s.environment_id ? "Previews saved" : "Previews turned off");
    },
    onError: (err) => toast.error(err instanceof ApiError && err.status < 500 ? err.message : "Failed to save the previews"),
  });
  const submit = () => (willRemove ? setConfirming(true) : save.mutate());

  const rebuilt = envServices.filter((s) => !!s.repository_id).map((s) => s.name);
  const reached = envServices.filter((s) => !s.repository_id).map((s) => s.name);
  const domain = cur.domain.trim().toLowerCase();
  // Unread, the list would be empty: Save would skip the dialog that names
  // what it removes.
  const err = (cur.on ? maxErr : "") || (removes && !open ? "Save waits until Toft knows which previews this removes." : "");

  if (settingsFailed) {
    return (
      <section id="s-previews" tabIndex={-1} aria-labelledby="pv-h" className={cn(card, "focus:outline-none")}>
        <div className="border-b border-subtle px-4 py-3">
          <h2 id="pv-h" className={h2}>Previews</h2>
        </div>
        <div className="px-4 py-3">
          <LoadFailed what="the preview settings" onRetry={() => void refetchSettings()} />
        </div>
      </section>
    );
  }

  return (
    <form id="s-previews" tabIndex={-1} aria-labelledby="pv-h" className={cn(card, "focus:outline-none")}
      onSubmit={(e) => { e.preventDefault(); if (changed && !err) submit(); }}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <span className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="pv-h" className={h2}>Previews</h2>
          {settings && <span className={sum}>{headSum}</span>}
        </span>
        <Link href={`/projects/${projectId}`} className="text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">
          See them on the Overview
        </Link>
      </div>

      <div className="flex flex-col gap-3.5 p-4">
        <div className="flex items-center justify-between gap-4">
          <div className="flex min-w-0 flex-col">
            <span className="text-[13px] font-medium text-fg">A preview for each open pull request</span>
            <span id="pv-d" className={help}>
              Removed when the pull request merges or closes.
              {settings && !cur.on && <> Off: pull requests in {projectName} open without one. {environments && own.length === 0 ? "Add an environment first." : isOwner ? "" : "An owner turns it on."}</>}
            </span>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={cur.on}
            aria-label="A preview for each open pull request"
            aria-describedby="pv-d"
            disabled={!isOwner || !settings || own.length === 0}
            onClick={() => setDraft({ ...cur, env, on: !cur.on })}
            className={cn(
              "relative h-5 w-9 flex-none rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60",
              "before:absolute before:-inset-x-1 before:-inset-y-3 before:content-[''] pointer-coarse:before:-inset-x-1",
              cur.on ? "bg-accent" : "bg-strong",
            )}
          >
            <span className={cn("absolute left-[3px] top-[3px] h-3.5 w-3.5 rounded-full transition-[translate]", cur.on ? "translate-x-4 bg-on-accent" : "bg-fg/70")} />
          </button>
        </div>

        {cur.on && (
          <>
            <div className="grid gap-3.5 sm:grid-cols-2">
              <div className="flex min-w-0 flex-col gap-1.5">
                <label htmlFor="pv-env" className={label}>Copies</label>
                <select id="pv-env" value={env} disabled={!isOwner} onChange={(e) => setDraft({ ...cur, env: e.target.value })} className={cn(field, "font-mono text-[13px]")}>
                  {own.map((e) => <option key={e.id} value={e.id}>{serverLabel(e)}</option>)}
                </select>
              </div>
              <div className="flex min-w-0 flex-col gap-1.5">
                <label htmlFor="pv-max" className={label}>At once</label>
                <input
                  id="pv-max"
                  inputMode="numeric"
                  value={cur.max}
                  disabled={!isOwner}
                  aria-invalid={!!maxErr || undefined}
                  aria-describedby="pv-max-help"
                  onChange={(e) => setDraft({ ...cur, env, max: e.target.value })}
                  className={cn(field, "font-mono text-[13px]", maxErr && "border-status-failed")}
                />
                <p id="pv-max-help" className={cn(help, maxErr && "text-status-failed-fg")}>
                  {maxErr || `Stopped ones count. Past ${max}, a new one waits until one is removed; one waiting for approval doesn’t count.`}
                </p>
              </div>
            </div>
            {copied && (
              rebuilt.length === 0 ? (
                <p role="status" className="flex items-start gap-2 text-[13px] leading-[19px] text-status-pending-fg">
                  <AlertTriangle className="mt-[3px] h-[13px] w-[13px] flex-none" aria-hidden="true" />
                  <span>
                    {copiesLine(copied.name, rebuilt, reached)}
                    {isOwner && own.length > 1 && " Copy an environment whose services build from it."}
                  </span>
                </p>
              ) : (
                <p className="text-[13px] leading-[19px] text-fg/70">{copiesLine(copied.name, rebuilt, reached)}</p>
              )
            )}
            <div className="flex min-w-0 flex-col gap-1.5">
              <label htmlFor="pv-dom" className={label}>
                Domain <span className="font-normal text-muted">optional</span>
              </label>
              <input
                id="pv-dom"
                value={cur.domain}
                disabled={!isOwner}
                placeholder="preview.example.com"
                aria-describedby="pv-dom-help"
                onChange={(e) => setDraft({ ...cur, env, domain: e.target.value })}
                className={cn(field, "font-mono text-[13px]")}
              />
              <p id="pv-dom-help" className={help}>
                Addresses read <span className="font-mono text-fg/85">{addressExample(domain, rebuilt[0] ?? "web", projectName, server?.ip_address)}</span>.{" "}
                {!domain
                  ? "Without a domain, each address is a name under sslip.io, which answers with the server’s address. They work at once; their certificates come from a pool of Let’s Encrypt’s that every sslip.io user shares, so one may take a while."
                  : saved.on && domain === saved.domain && check.data
                    ? (
                      <span role="status">
                        Needs a DNS record: <span className="font-mono text-fg/85">{check.data.record}</span>{" "}
                        {check.data.ok
                          ? <span className="text-status-running-fg">· found</span>
                          : <span className="text-status-pending-fg">· not found yet: until it is, previews start under sslip.io names</span>}
                        {". "}
                      </span>
                    )
                    : "Needs a wildcard DNS record to the server, checked once saved. "}
                {domain && `Each address gets its own Let’s Encrypt certificate; Let’s Encrypt issues 50 a week for ${registeredDomain(domain)}, production’s included.`}
              </p>
              {saved.on && saved.domain && domain === saved.domain && check.data && (
                <button type="button" className={cn(ghostSm, "-ml-2.5 self-start")} onClick={() => check.refetch()} disabled={check.isFetching}>
                  {check.isFetching ? "Checking…" : "Check the record again"}
                </button>
              )}
            </div>
            <dl className="flex flex-col text-[13px]">
              <div className="grid gap-x-3 gap-y-0.5 py-2 sm:grid-cols-[110px_minmax(0,1fr)]">
                <dt className="text-muted">Who starts one</dt>
                <dd className="text-fg">Owners and developers, at once. An API key: an owner or developer approves each commit first; it then runs Sandboxed, alone, with no secrets.</dd>
              </div>
              <div className="grid gap-x-3 gap-y-0.5 border-t border-subtle py-2 sm:grid-cols-[110px_minmax(0,1fr)]">
                <dt className="text-muted">Room</dt>
                <dd className="text-fg">When a deploy on {server?.name ?? "its server"} needs the memory, the oldest preview stops first. Production and staging never stop for a preview.</dd>
              </div>
            </dl>
          </>
        )}
        {removes && openFailed && <LoadFailed what="the open previews" onRetry={() => void refetchOpen()} />}
      </div>

      {!isOwner && settings && (
        <p className={saveBar}>Owners change previews.</p>
      )}

      {isOwner && changed && (
        <div className={saveBar}>
          <span className={cn(err && "text-status-failed-fg")}>
            {err || (cur.on && !saved.on
              ? "Previews start at the next pull request opened or pushed to."
              : `Unsaved ${changes.length === 1 ? "change" : "changes"} to ${joinNames(changes)}`)}
          </span>
          <span className="flex items-center gap-2">
            <button type="button" className={ghostSm} onClick={() => setDraft(null)} disabled={save.isPending}>Cancel</button>
            <button type="submit" className={primarySm} disabled={!!err || save.isPending}>{save.isPending ? "Saving…" : "Save"}</button>
          </span>
        </div>
      )}

      <Dialog open={confirming} onOpenChange={(o) => !save.isPending && setConfirming(o)}>
        <DialogContent className={cn(dialogContent, "max-w-[560px]")}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>
              {cur.on ? `Copy ${copied?.name ?? "another environment"} in ${projectName}?` : `Turn previews off in ${projectName}?`}
            </DialogTitle>
            <DialogDescription className={dialogText}>
              Removes the {joinNames([started.length > 0 ? `${started.length} preview${started.length === 1 ? "" : "s"}` : "", waiting > 0 ? `the ${waiting === 1 ? "one" : waiting} waiting` : ""])} now, with their containers and addresses
              {waiting > 0 ? ", and drops the approvals they wait for" : ""}. Nothing of {own.find((e) => e.id === saved.env)?.name ?? "the environment"} changes. Pull requests stay open.
              {cur.on ? " Each open pull request gets a new preview at its next push, or from Start preview." : ""}
            </DialogDescription>
          </DialogHeader>
          <ul className="mx-5 mt-3.5 flex flex-col gap-1 rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-xs text-fg/85">
            {removable.map((p) => <li key={`${p.repository_id}-${p.pull_number}`}>{previewRowText(p)}</li>)}
          </ul>
          <div className={dialogFooter}>
            <button type="button" className={outline} onClick={() => setConfirming(false)} disabled={save.isPending}>Cancel</button>
            <button type="button" className="inline-flex h-9 items-center rounded-lg bg-danger px-3.5 text-[13px] font-medium text-on-danger hover:bg-danger/90 disabled:opacity-50"
              onClick={() => save.mutate()} disabled={save.isPending}>
              {save.isPending ? "Removing…" : started.length > 0
                ? `Remove ${started.length} preview${started.length === 1 ? "" : "s"} and ${cur.on ? `copy ${copied?.name ?? "it"}` : "turn off"}`
                : cur.on ? `Copy ${copied?.name ?? "it"}` : "Turn off"}
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </form>
  );
}
