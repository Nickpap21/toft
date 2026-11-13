"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { orgService, type OrgIsolation } from "@/lib/api/org.service";
import { btnSm, ghostSm, primarySm, saveBar } from "@/app/projects/_lib/ui";
import { CardHead, Segmented } from "@/components/settings/settings-frame";
import { Read } from "@/components/shell/unreachable";
import { cn } from "@/lib/utils";

type Level = OrgIsolation["default_isolation"];
type IsolationServer = OrgIsolation["servers"][number];

const LEVELS: { key: Level; name: string; runtime: string; detail: string }[] = [
  { key: "standard", name: "Standard", runtime: "runc", detail: "Containers share the server’s kernel, behind seccomp, AppArmor and the hardened defaults." },
  {
    key: "sandboxed",
    name: "Sandboxed",
    runtime: "gVisor runsc",
    detail: "Each container gets its own kernel in user space. Slower for databases and small requests; needs gVisor on the server.",
  },
  {
    key: "sandboxed_strict",
    name: "Sandboxed, strict network",
    runtime: "gVisor runsc-strict",
    detail: "gVisor’s own network stack too: network calls never reach the server’s. Slower again; needs gVisor and Toft’s agent on the server.",
  },
];
const LEVEL = Object.fromEntries(LEVELS.map((l) => [l.key, l])) as Record<Level, (typeof LEVELS)[number]>;

/** What a server's row says about Sandboxed and the strict network there
 *  (D-115); the panel's own server (no id) has its card on Instance →
 *  Server (D-97). */
function serverState(s: IsolationServer): string {
  const page = s.id ? "the Server page" : "Instance → Server";
  const gvisor = s.gvisor_version ? `gVisor ${s.gvisor_version}` : "gVisor";
  switch (s.sandboxed) {
    case "available":
      switch (s.sandboxed_strict) {
        case "available":
          return `Both: ${gvisor}, and Toft’s agent answers names`;
        case "unavailable":
          return s.id
            ? "Sandboxed only: the strict network needs Toft’s agent, and it connects over SSH"
            : "Sandboxed only: the strict network needs Toft’s agent, and the panel’s server runs without one";
        case "not_installed":
          return `Sandboxed only: an instance operator installs gVisor again on ${page} to add the strict network`;
        case "failing":
          return `Sandboxed only: the strict network fails its check, see ${page}`;
      }
      return `${gvisor} installed`;
    case "not_installed":
      return `No gVisor: an instance operator installs it on ${page}`;
    case "failing":
      return `gVisor fails its check: see ${page}`;
    case "unknown":
      return s.id ? "Not checked yet: Check again on the Server page" : "Not checked yet: an instance operator checks it on Instance → Server";
  }
  return s.reason;
}

/** The level a workspace's services run at unless one chooses its own
 *  (board: ReworkSettings; D-95). Owners change it; a level that would get
 *  services refused asks first (D-138). */
export function WorkspaceIsolationCard({ isOwner }: { isOwner: boolean }) {
  const qc = useQueryClient();
  const iso = useQuery({ queryKey: ["org-isolation"], queryFn: () => orgService.getIsolation() });
  const settings = useQuery({ queryKey: ["org-settings"], queryFn: () => orgService.getSettings() });
  const [edit, setEdit] = useState<Level | null>(null);
  const [showServers, setShowServers] = useState(false);
  const ref = useRef<HTMLElement>(null);
  // The warning or save bar goes away: focus back on the level picked.
  const refocus = () => requestAnimationFrame(() => ref.current?.querySelector<HTMLElement>("[aria-pressed=true]")?.focus());
  const saved = iso.data?.default_isolation ?? "standard";
  const value = edit ?? saved;
  const dirty = value !== saved;
  const save = useMutation({
    mutationFn: () =>
      orgService.updateSettings({ resource_bypass_min_role: settings.data!.resource_bypass_min_role, default_isolation: value }),
    onSuccess: () => {
      setEdit(null);
      refocus();
      qc.invalidateQueries({ queryKey: ["org-isolation"] });
      qc.invalidateQueries({ queryKey: ["org-settings"] });
      toast.success(`Services follow ${LEVEL[value].name} from their next deploy`);
    },
    onError: (e) => toast.error(e instanceof ApiError && e.status < 500 ? e.message : "Couldn’t save the isolation level"),
  });
  const servers = iso.data?.servers ?? [];
  // The level the availability line and the table speak of: the one picked,
  // or Sandboxed while Standard (which runs everywhere) is.
  const shown: Level = value === "standard" ? "sandboxed" : value;
  const runs = (s: IsolationServer) => (shown === "sandboxed_strict" ? s.sandboxed_strict : s.sandboxed) === "available";
  const running = servers.filter(runs);
  const cant = value !== "standard" ? servers.filter((s) => !runs(s)) : [];
  const refused = cant.filter((s) => s.following > 0);
  const refusedCount = refused.reduce((n, s) => n + s.following, 0);
  const warn = dirty && refusedCount > 0;
  const busy = save.isPending || !settings.data;

  return (
    <section ref={ref} aria-labelledby="iso-h" className="rounded-[10px] border border-subtle bg-surface">
      <CardHead id="iso-h" title="Isolation" sum="the level services run at unless they choose their own" />
      <div className="flex flex-col gap-3.5 p-4">
        <Read query={iso} what="the isolation level" loading={<div role="status" className="h-5 w-64 rounded bg-raised motion-safe:animate-pulse" aria-label="Loading the isolation level" />}>{() => (<>
        <div className="flex flex-col gap-1.5">
          <span className="text-[13px] font-medium text-fg">Default level</span>
          <Segmented
            label="Default isolation level"
            options={LEVELS.map((l) => ({ value: l.key, label: l.name }))}
            value={value}
            onChange={setEdit}
            disabled={!isOwner}
          />
          <p className="text-xs leading-[18px] text-muted">
            {LEVEL[value].name} <span className="font-mono">{LEVEL[value].runtime}</span>: {LEVEL[value].detail} A change applies to each service at its next
            deploy; nothing is redeployed.
          </p>
        </div>

        {warn && (
          <div role="alert" className="flex gap-2.5 rounded-lg border border-status-pending/45 bg-status-pending-soft p-3 text-[13px] leading-[19px] text-fg/85">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none text-status-pending" aria-hidden="true" />
            <div className="flex min-w-0 flex-col gap-0.5">
              <b className="font-medium text-fg">
                {cant.length} of {servers.length} {servers.length === 1 ? "server" : "servers"} can’t run {LEVEL[value].name}
              </b>
              <p>
                The {refusedCount} {refusedCount === 1 ? "service" : "services"} on them that follow the default would be refused at their next deploy, until
                {value === "sandboxed_strict" ? " those servers have gVisor and Toft’s agent" : " an instance operator installs gVisor there"}. Services that chose
                their own level aren’t affected.
              </p>
              <span className="mt-2 flex flex-wrap gap-1">
                <button type="button" disabled={busy} onClick={() => save.mutate()} className={btnSm}>
                  {save.isPending ? "Saving…" : `Change to ${LEVEL[value].name} anyway`}
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setEdit(null);
                    refocus();
                  }}
                  className={ghostSm}
                >
                  Keep {LEVEL[saved].name}
                </button>
              </span>
            </div>
          </div>
        )}

        {servers.length === 0 ? (
          <p className="text-[13px] text-fg/80">
            This workspace has no servers yet.{" "}
            <Link href="/servers" className="inline-link">
              Add one on Servers
            </Link>
            .
          </p>
        ) : (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-fg/80">
            <span>
              {value === "standard" && "Standard runs on every server; "}
              {value === "standard" ? "Sandboxed" : LEVEL[shown].name} {value === "standard" ? "on" : "runs on"} {running.length} of {servers.length}{" "}
              {servers.length === 1 ? "server" : "servers"}
              {running.length > 0 && running.length <= 3 && (
                <span className="font-mono text-xs text-muted"> ({running.map((s) => s.name).join(", ")})</span>
              )}
            </span>
            <button
              type="button"
              aria-expanded={showServers}
              aria-controls="iso-servers"
              onClick={() => setShowServers(!showServers)}
              className="touch-target rounded py-1 text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            >
              {showServers ? "Hide servers" : "Show servers"}
            </button>
          </div>
        )}
        </>)}</Read>
      </div>

      {showServers && servers.length > 0 && (
        <table id="iso-servers" className="w-full border-collapse border-t border-subtle text-[13px] max-sm:block">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className="px-4 py-2 text-left text-xs font-medium text-muted">Server</th>
              <th scope="col" className="px-4 py-2 text-left text-xs font-medium text-muted">Offers</th>
              <th scope="col" className="px-4 py-2 text-left text-xs font-medium text-muted">Following the default</th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {servers.map((s) => {
              const bad = refused.includes(s);
              return (
                <tr key={s.id || "panel"} className="border-t border-subtle max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3">
                  <td className="px-4 py-2.5 align-middle max-sm:p-0">
                    {s.id ? (
                      <Link href={`/servers/${s.id}`} className="font-mono text-xs text-fg hover:text-link">
                        {s.name}
                      </Link>
                    ) : (
                      <span className="font-mono text-xs text-fg">{s.name}</span>
                    )}
                  </td>
                  <td className={cn("px-4 py-2.5 align-middle max-sm:col-span-2 max-sm:row-start-2 max-sm:p-0", bad ? "text-status-pending-fg" : runs(s) ? "text-status-running-fg" : "text-fg/80")}>
                    {serverState(s)}
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5 align-middle text-muted max-sm:col-start-2 max-sm:row-start-1 max-sm:p-0 max-sm:text-right">
                    {s.following} {s.following === 1 ? "service" : "services"}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      {isOwner
        ? dirty &&
          !warn && (
            <div className={saveBar}>
              <span>Unsaved change to the default level</span>
              <span className="flex gap-1">
                <button
                  type="button"
                  onClick={() => {
                    setEdit(null);
                    refocus();
                  }}
                  className={ghostSm}
                >
                  Cancel
                </button>
                <button type="button" disabled={busy} onClick={() => save.mutate()} className={primarySm}>
                  {save.isPending ? "Saving…" : "Save"}
                </button>
              </span>
            </div>
          )
        : <p className={saveBar}>Owners change the default level.</p>}
    </section>
  );
}
