"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@/lib/api/client";
import { serverService, type CapacitySummary, type ServerShare } from "@/lib/api/server.service";
import { useAuth } from "@/context/AuthContext";
import { cn, possessive } from "@/lib/utils";
import { card, fieldInput, fieldLabel, primaryButton, smallButton } from "@/components/settings/settings-frame";
import { btnSm, ghostSm } from "@/app/projects/_lib/ui";
import { settingsFoot, settingsHead, titleRow } from "@/app/servers/[id]/aside";
import { cores, gib } from "./capacity";

const message = (err: unknown, fallback: string) => (err instanceof ApiError && err.status < 500 ? err.message : fallback);

/**
 * Shares a server with another workspace the user also owns, within a CPU
 * and memory share, so one machine is one server entry (board: Server,
 * Sharing). The other workspace sees the server's name and its share,
 * never its address. Owners only; the server refuses sharing unless app
 * networks are isolated.
 */
export function SharingCard({
  serverId,
  canManage,
  colourOf = () => "bg-fg/75",
  summary,
}: {
  serverId: string;
  canManage: boolean;
  colourOf?: (orgId: string) => string;
  summary?: CapacitySummary;
}) {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const key = ["server-shares", serverId];
  const { data: shares, error } = useQuery({
    queryKey: key,
    queryFn: () => serverService.listShares(serverId),
    enabled: canManage,
    retry: false,
  });

  // Workspaces the user owns, other than this one.
  const targets = (user?.orgs ?? []).filter((o) => o.role === "owner" && o.org_id !== user?.orgId);
  const [orgId, setOrgId] = useState("");
  const [cpu, setCpu] = useState("1");
  const [memoryGiB, setMemoryGiB] = useState("1.5");
  const [changing, setChanging] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: (target: string) => serverService.putShare(serverId, target, Number(cpu), Math.round(Number(memoryGiB) * 1024)),
    onSuccess: (updated) => {
      queryClient.setQueryData(key, updated);
      queryClient.invalidateQueries({ queryKey: ["server-capacity", serverId] });
      setOrgId("");
      setChanging(null);
      toast.success("Share saved");
    },
    onError: (err) => toast.error(message(err, "Failed to share the server")),
  });
  const remove = useMutation({
    mutationFn: (target: string) => serverService.deleteShare(serverId, target),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: key });
      queryClient.invalidateQueries({ queryKey: ["server-capacity", serverId] });
      setRemoving(null);
      toast.success("Share removed");
    },
    onError: (err) => toast.error(message(err, "Failed to remove the share")),
  });

  if (!canManage) return null;
  const valid = Number(cpu) > 0 && Number(memoryGiB) > 0;
  const fields = (target: string) => (
    <form
      className="flex flex-wrap items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (target && valid) save.mutate(target);
      }}
    >
      {!changing && (
        <div className="flex flex-col gap-1">
          <label htmlFor="share-org" className={fieldLabel}>Workspace</label>
          <select id="share-org" value={orgId} onChange={(e) => setOrgId(e.target.value)} className={cn(fieldInput, "h-8 text-xs")}>
            <option value="">Choose…</option>
            {targets
              .filter((o) => !(shares ?? []).some((s) => s.org_id === o.org_id))
              .map((o) => (
                <option key={o.org_id} value={o.org_id}>{o.name}</option>
              ))}
          </select>
        </div>
      )}
      <div className="flex flex-col gap-1">
        <label htmlFor="share-cores" className={fieldLabel}>CPU, vCPU</label>
        <input id="share-cores" inputMode="decimal" value={cpu} onChange={(e) => setCpu(e.target.value)} className={cn(fieldInput, "h-8 w-24 font-mono text-xs")} />
      </div>
      <div className="flex flex-col gap-1">
        <label htmlFor="share-memory" className={fieldLabel}>Memory, GiB</label>
        <input id="share-memory" inputMode="decimal" value={memoryGiB} onChange={(e) => setMemoryGiB(e.target.value)} className={cn(fieldInput, "h-8 w-24 font-mono text-xs")} />
      </div>
      <button type="submit" className={cn(primaryButton, "h-8")} disabled={save.isPending || !target || !valid}>
        {save.isPending ? "Saving…" : changing ? "Save share" : "Share"}
      </button>
      {changing && (
        <button type="button" className={smallButton} onClick={() => setChanging(null)}>Cancel</button>
      )}
    </form>
  );

  const list = shares ?? [];
  const canAdd = targets.some((o) => !list.some((s) => s.org_id === o.org_id));
  return (
    <section aria-labelledby="sh-h" className={cn(card, "overflow-hidden")}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="sh-h" className="text-[15px] font-semibold leading-[22px] text-fg">Sharing</h2>
          {shares && <span className="text-xs text-muted">{list.length === 0 ? "not shared" : `${list.length} ${list.length === 1 ? "workspace" : "workspaces"}`}</span>}
        </div>
      </div>
      {error ? (
        <p role="alert" className="px-4 py-3 text-[13px] text-status-failed-fg">The shares could not be loaded: {error instanceof Error ? error.message : ""}</p>
      ) : !shares ? null : list.length > 0 ? (
        <table className="w-full border-collapse text-[13px] max-sm:block">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className="border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted">Workspace</th>
              <th scope="col" className="border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted">Share · reserved</th>
              <th scope="col" className="border-b border-subtle px-4 py-2"><span className="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {list.map((sh: ServerShare, i) => {
              const used = summary?.workspaces?.find((w) => w.org_id === sh.org_id);
              return (
                <tr key={sh.org_id} className={cn("max-sm:flex max-sm:flex-wrap max-sm:items-center max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3", i > 0 && "border-t border-subtle")}>
                  <td className="px-4 py-2.5 align-middle max-sm:p-0">
                    <span className="inline-flex items-center gap-2 font-medium text-fg">
                      <span aria-hidden="true" className={cn("h-2 w-2 flex-none rounded-xs", colourOf(sh.org_id))} />
                      {sh.org_name}
                    </span>
                  </td>
                  <td className="px-4 py-2.5 align-middle font-mono text-xs max-sm:order-last max-sm:w-full max-sm:p-0">
                    {cores(sh.cpu_cores)} vCPU · {gib(sh.memory_mb)} GiB{" "}
                    <span className="text-muted">
                      · {cores(used?.cpu_cores ?? 0)} vCPU · {gib(used?.memory_mb ?? 0)} GiB reserved
                    </span>
                    {changing === sh.org_id && <div className="mt-2.5 font-sans">{fields(sh.org_id)}</div>}
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5 text-right align-middle max-sm:ml-auto max-sm:p-0">
                    {removing === sh.org_id ? (
                      <span className="inline-flex flex-wrap items-center justify-end gap-2 whitespace-normal text-xs text-fg/85">
                        Remove {possessive(sh.org_name)} share?
                        <button
                          type="button"
                          className="touch-target inline-flex h-[30px] items-center rounded-md bg-danger px-2.5 font-semibold text-on-danger disabled:opacity-50"
                          disabled={remove.isPending}
                          onClick={() => remove.mutate(sh.org_id)}
                        >
                          {remove.isPending ? "Removing…" : "Remove"}
                        </button>
                        <button type="button" className={btnSm} onClick={() => setRemoving(null)}>Cancel</button>
                      </span>
                    ) : changing !== sh.org_id && (
                      <span className="inline-flex gap-1.5">
                        <button
                          type="button"
                          aria-label={`Change ${possessive(sh.org_name)} share`}
                          className={btnSm}
                          onClick={() => {
                            setChanging(sh.org_id);
                            setCpu(String(sh.cpu_cores));
                            setMemoryGiB(String(Math.round((sh.memory_mb / 1024) * 10) / 10));
                          }}
                        >
                          Change
                        </button>
                        <button type="button" aria-label={`Remove share for ${sh.org_name}…`} className={ghostSm} onClick={() => setRemoving(sh.org_id)}>
                          Remove share…
                        </button>
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : (
        <p className="px-4 py-3 text-[13px] text-fg/70">
          Not shared with any workspace.{!canAdd && " You own no other workspace to share it with."}
        </p>
      )}
      {canAdd && !changing && (
        <div className="flex flex-col gap-2 border-t border-subtle px-4 py-3">
          <span className="text-xs text-muted">Share with another workspace you own. It can deploy here within a CPU and memory share. Needs isolated app networks.</span>
          {fields(orgId)}
        </div>
      )}
      <div className={settingsFoot}>
        <span className="text-xs">It sees this server’s name and its share, never its address.</span>
      </div>
    </section>
  );
}
