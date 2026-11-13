"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api/client";
import { panelHostQuery, setupQuery, setupService, type PanelHost, type ServerRole, type Setup } from "@/lib/api/instance.service";
import { Notice } from "@/components/shell/auth-shell";
import { SetupPage } from "@/components/setup/setup-page";
import {
  nextStep,
  previousStep,
  setupBack,
  setupBody,
  setupCard,
  setupFoot,
  setupLead,
  setupOption,
  setupPrimary,
} from "@/components/setup/setup-frame";

// Step 3 (board ReworkSetup, D-26, D-62): run apps here too, or only Toft.
// Worker for an existing panel waits for the node agent.

const GiB = 1 << 30;
const round1 = (n: number) => Math.round(n * 10) / 10;

export default function RoleStep() {
  const host = useQuery(panelHostQuery);
  return (
    <SetupPage step="role" title="What runs on this server">
      {(setup) =>
        host.data ? (
          <RoleForm setup={setup} host={host.data} />
        ) : host.isError ? (
          <div className={`${setupCard} ${setupBody} items-start`}>
            <Notice kind="error">Couldn’t read this server’s CPU and memory.</Notice>
            <button type="button" onClick={() => void host.refetch()} className={setupBack}>
              Try again
            </button>
          </div>
        ) : (
          <div className={`${setupCard} h-64 motion-safe:animate-pulse`}>
            <span className="sr-only" role="status">Loading…</span>
          </div>
        )
      }
    </SetupPage>
  );
}

function RoleForm({ setup, host }: { setup: Setup; host: PanelHost }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const r = host.reservation;
  // Below the floor the apps would get almost nothing (D-62, D-132).
  const small = host.cpu_cores < r.min_cpu || host.memory_bytes < r.min_memory_bytes;
  const [role, setRole] = useState<ServerRole>(setup.role_set ? setup.role : small ? "control_plane" : "apps");
  const save = useMutation({
    mutationFn: () => setupService.setRole(role),
    onSuccess: (st) => {
      queryClient.setQueryData(setupQuery.queryKey, st);
      void queryClient.invalidateQueries({ queryKey: panelHostQuery.queryKey });
      router.push(nextStep("role"));
    },
  });
  const mem = round1(host.memory_bytes / GiB);
  const keeps = round1((r.system_memory_bytes + r.panel_memory_bytes) / GiB);
  const options: { id: ServerRole; title: string; desc: string; note?: string }[] = [
    {
      id: "apps",
      title: "Run apps here too",
      desc: `Best with one server. ${host.cpu_cores} vCPU and ${mem} GiB: Toft keeps ${keeps} GiB, about ${round1(r.apps_memory_bytes / GiB)} GiB is left for apps. You can add servers later.`,
      // The host's figure rounds down, so one just under the floor never reads as the floor itself.
      note: small
        ? `Needs at least ${r.min_cpu} vCPU and ${round1(r.min_memory_bytes / GiB)} GiB of memory, which a server sold as 4 GB passes; this server has ${host.cpu_cores} vCPU and ${Math.floor((host.memory_bytes / GiB) * 10) / 10} GiB.`
        : undefined,
    },
    {
      id: "control_plane",
      title: "Only Toft",
      desc: "Apps run on other servers: you add the first one next, on Home. Safer for a team: an app can’t take Toft’s memory.",
    },
  ];
  const inUse = save.error instanceof ApiError && (save.error.body as { data?: { code?: string } } | null)?.data?.code === "panel_host_in_use";

  return (
    <form
      className={setupCard}
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className={setupBody}>
        <p className={setupLead}>This server runs Toft. It can run your apps too, or you can keep it for Toft alone and add servers for apps.</p>
        <fieldset className="flex flex-col gap-3.5">
          <legend className="sr-only">What runs on this server</legend>
          {options.map((o) => (
            <label key={o.id} className={`${setupOption(role === o.id)} ${o.note ? "cursor-not-allowed" : ""}`}>
              <input
                type="radio"
                name="role"
                checked={role === o.id}
                disabled={!!o.note}
                onChange={() => setRole(o.id)}
                aria-labelledby={`role-${o.id}-t`}
                aria-describedby={`role-${o.id}`}
                className="mt-[3px] h-4 w-4 flex-none accent-[var(--link)] disabled:opacity-60"
              />
              <span className="flex min-w-0 flex-1 flex-col gap-1">
                <span id={`role-${o.id}-t`} className={o.note ? "font-medium opacity-60" : "font-medium"}>{o.title}</span>
                <span id={`role-${o.id}`} className="text-xs leading-[18px] text-muted">
                  <span className={o.note ? "opacity-60" : undefined}>{o.desc}</span>
                  {o.note && <span className="mt-1 block text-status-pending-fg">{o.note}</span>}
                </span>
              </span>
            </label>
          ))}
        </fieldset>
        {save.error && (
          <Notice kind="error">
            {inUse || (save.error instanceof ApiError && save.error.status < 500) ? save.error.message : "Couldn’t reach Toft. Try again."}
          </Notice>
        )}
      </div>
      <div className={setupFoot}>
        <button type="button" onClick={() => router.push(previousStep("role"))} className={setupBack}>
          Back
        </button>
        <button type="submit" disabled={save.isPending} className={setupPrimary}>
          {save.isPending ? "Saving…" : "Continue"}
        </button>
      </div>
    </form>
  );
}
