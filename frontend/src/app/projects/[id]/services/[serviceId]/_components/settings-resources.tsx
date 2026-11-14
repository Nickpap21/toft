"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  formatDay,
  limitMB,
  resourcesService,
  type ComposeLimit,
  type ResourceRow,
  type ResourceSuggestion,
} from "@/lib/api/resources.service";
import { panelHostQuery, panelRoom } from "@/lib/api/instance.service";
import { serverService, type Server } from "@/lib/api/server.service";
import type { Service } from "@/lib/api/service.service";
import { ApiError, changedSinceRead } from "@/lib/api/client";
import { LoadFailed, ReadFailed, readFailed } from "@/components/shell/unreachable";
import { useAuth } from "@/context/AuthContext";
import { focusAfterReread } from "@/lib/focus-after-reread";
import { cn } from "@/lib/utils";
import { memoryText } from "./service-view";
import {
  DEFAULT_DEPLOY_TIMEOUT,
  DEFAULT_ROLLBACK_KEPT,
  deployTimeoutProblem,
  rollbackKeptProblem,
  type ConfigForm,
} from "./settings-form";

// Settings → Resources (board: ReworkServiceSettings.dc.html, D-41, D-42,
// D-155): the server's reserved room, the limits and what was measured. A
// suggestion fills the field; the card's Save saves it. The deploy time
// limit and rollback images kept are Settings → Deploy's.

const mono = "font-mono text-xs";
const input =
  "h-9 w-full rounded-md border border-strong/70 bg-ground px-2.5 font-mono text-[13px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60";
const mib = (mb: number) =>
  mb >= 1024 ? `${+(mb / 1024).toFixed(1)} GiB` : `${Math.round(mb)} MiB`;
const cores = (n: number) => `${+n.toFixed(2)}`;

/** "Lower to 320 MiB", "Raise to 768 MiB", "Set 512 MiB". */
export function suggestionText(
  s: ResourceSuggestion,
  field: "memory" | "cpu",
): string {
  const v =
    field === "memory"
      ? memoryText(s.value)
      : `${s.value} ${Number(s.value) === 1 ? "core" : "cores"}`;
  return `${s.direction === "down" ? "Lower to" : s.direction === "up" ? "Raise to" : "Set"} ${v}`;
}

/** The room on the panel's server for a service that runs there, else null. */
function usePanelRoom(service: Service) {
  const { data } = useQuery({
    ...panelHostQuery,
    enabled: !service.server_id,
    meta: { quietFailure: "Without the panel host's room the bars are left out; the limits below still read and save." },
  });
  return service.server_id ? null : panelRoom(data);
}

/** A suggestion to raise memory is left out while the panel's server is
 *  over-committed: the kill came from the server running short, and a higher
 *  limit takes more from every service there. */
const fitting = (s: ResourceSuggestion | null | undefined, hostOver: boolean) =>
  hostOver && s?.direction === "up" ? null : (s ?? null);

/** The server's reserved room, and this workspace's free share of it. */
export function ReservedBars({
  service,
  server,
}: {
  service: Service;
  server?: Server;
}) {
  const { user } = useAuth();
  const { data: capacity } = useQuery({
    queryKey: ["server-capacity", service.server_id],
    queryFn: () => serverService.getCapacitySummary(service.server_id!),
    enabled: !!service.server_id,
    staleTime: 30_000,
  });
  const { data: room } = useQuery({
    queryKey: ["server-fit", service.server_id, 0, 0],
    queryFn: () => serverService.fit(service.server_id!),
    enabled: !!service.server_id,
    staleTime: 30_000,
  });
  const host = usePanelRoom(service);
  if (!service.server_id) {
    // The panel's server: every workspace's services share what it leaves
    // its apps, in memory; CPU isn't counted there.
    if (!host) return null;
    return (
      <div className="flex flex-col gap-2.5">
        <div className="flex flex-col gap-1.5">
          <div className="flex justify-between gap-2 text-xs text-muted">
            <span>Memory reserved on the panel’s server</span>
            <span className="shrink-0 whitespace-nowrap font-mono text-fg/85">
              {mib(host.reserved)} of {mib(host.total)}
            </span>
          </div>
          <span aria-hidden="true" className="block h-1 rounded-sm bg-subtle">
            <span
              className={cn("block h-1 rounded-sm", host.over ? "bg-status-pending" : "bg-muted")}
              style={{ width: `${Math.min(100, (host.reserved / host.total) * 100)}%` }}
            />
          </span>
        </div>
        {host.over ? (
          <p className="flex items-start gap-2 text-xs leading-[17px] text-status-pending-fg">
            <AlertTriangle role="img" aria-label="Warning" className="mt-0.5 h-3.5 w-3.5 flex-none text-status-pending" />
            The services on the panel’s server reserve more memory than the {mib(host.total)} it has for apps, so any
            of them can be killed for memory before it reaches its own limit. Lower this service’s limit below or move
            it to another server; an instance operator can also give the server more memory.
          </p>
        ) : (
          <p className="text-xs leading-[17px] text-muted">
            Free for apps: <span className="font-mono text-fg/85">{mib(host.left)}</span>, shared by every workspace’s
            services on this server. The panel keeps the rest; CPU isn’t counted here.
          </p>
        )}
      </div>
    );
  }
  if (!capacity?.capacity_discovered) return null;
  const name = server?.name ?? "the server";
  return (
    <div className="flex flex-col gap-2.5">
      <div className="grid gap-4 sm:grid-cols-2">
        {[
          {
            label: `CPU reserved on ${name}`,
            used: capacity.allocated_cpu_cores,
            total: capacity.effective_cpu_cores,
            text: `${cores(capacity.allocated_cpu_cores)} of ${cores(capacity.effective_cpu_cores)} vCPU`,
          },
          {
            label: "Memory reserved",
            used: capacity.allocated_memory_mb,
            total: capacity.effective_memory_mb,
            text: `${mib(capacity.allocated_memory_mb)} of ${mib(capacity.effective_memory_mb)}`,
          },
        ].map((b) => (
          <div key={b.label} className="flex flex-col gap-1.5">
            <div className="flex justify-between gap-2 text-xs text-muted">
              <span>{b.label}</span>
              <span className="font-mono text-fg/85">{b.text}</span>
            </div>
            <span aria-hidden="true" className="block h-1 rounded-sm bg-subtle">
              <span
                className="block h-1 rounded-sm bg-muted"
                style={{
                  width: `${b.total > 0 ? Math.min(100, (b.used / b.total) * 100) : 0}%`,
                }}
              />
            </span>
          </div>
        ))}
      </div>
      {room?.capacity_known && (
        <p className="text-xs leading-[17px] text-muted">
          Free for {user?.orgName ?? "this workspace"}:{" "}
          <span className="font-mono text-fg/85">
            {cores(
              Math.max(0, room.effective_cpu_cores - room.allocated_cpu_cores),
            )}{" "}
            vCPU ·{" "}
            {mib(
              Math.max(0, room.effective_memory_mb - room.allocated_memory_mb),
            )}
          </span>
          .
          {capacity.share && server?.shared_by
            ? ` This is its share of ${name}, which ${server.shared_by} owns.`
            : ""}
        </p>
      )}
    </div>
  );
}

/** Resources for a single container: limits and what was measured. Fields
 *  are the form's; the card saves them. */
export function SingleResources({
  service,
  serviceId,
  server,
  form,
  set,
  canEdit,
}: {
  service: Service;
  serviceId: string;
  server?: Server;
  form: ConfigForm;
  set: (patch: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const read = useQuery({
    queryKey: ["service-resources", serviceId],
    queryFn: () => resourcesService.service(serviceId),
  });
  const { data, dataUpdatedAt } = read;
  const row = data?.rows[0];
  const memSuggestion = fitting(row?.memory.suggestion, !!usePanelRoom(service)?.over);
  // The grace (D-41) as of the last read.
  const grace =
    data?.grace_until && new Date(data.grace_until).getTime() > dataUpdatedAt
      ? data.grace_until
      : null;
  const kindDefault: Record<string, string> = {
    database: "1 GiB",
    cache: "256 MiB",
    app: "512 MiB",
  };
  // An admin tool's default is its own (D-150), from the panel.
  const { data: toolDefault } = useQuery({
    meta: { quietFailure: "One word in a sentence about defaults: without the figure it says “its own”, which is still true." },
    queryKey: ["default-limit", service.addon_slug ?? "", service.docker_image ?? ""],
    queryFn: () => resourcesService.defaultLimit(service.addon_slug ?? "", service.docker_image ?? ""),
    enabled: row?.kind === "tool",
    staleTime: Infinity,
  });
  const memErr = !form.memory_limit.trim()
    ? "Required on every deploy."
    : !/^\d+(\.\d+)?[mgMG]$/.test(form.memory_limit.trim())
      ? "Docker notation: 512m, 1g."
      : "";
  const cpuErr =
    form.cpu_limit.trim() && !(Number(form.cpu_limit) > 0)
      ? "Cores, such as 0.5 or 2."
      : "";

  return (
    <div className="flex flex-col gap-4">
      <ReservedBars service={service} server={server} />
      {!form.memory_limit && grace && (
        <p className="flex items-start gap-2 rounded-lg border border-status-pending/40 bg-status-pending-soft/40 px-3 py-2.5 text-[13px] text-fg">
          <AlertTriangle
            className="mt-0.5 h-4 w-4 flex-none text-status-pending-fg"
            aria-hidden="true"
          />
          A memory limit is required from {formatDay(grace)}: deploys without
          one are refused from then.
        </p>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="flex flex-col gap-1.5">
          <label htmlFor="set-memory" className="text-[13px] text-fg">
            Memory limit
          </label>
          <input
            id="set-memory"
            className={cn(
              input,
              memErr && form.memory_limit && "border-status-failed",
            )}
            value={form.memory_limit}
            disabled={!canEdit}
            onChange={(e) => set({ memory_limit: e.target.value })}
            aria-describedby="set-memory-help"
            aria-invalid={!!memErr && !!form.memory_limit}
          />
          <p
            id="set-memory-help"
            className={cn(
              "text-xs",
              memErr && form.memory_limit
                ? "text-status-failed-fg"
                : "text-muted",
            )}
          >
            {memErr && form.memory_limit
              ? memErr
              : "Required on every deploy: it can be changed, not emptied. Docker notation: 512m, 1g."}
          </p>
        </div>
        <div className="flex flex-col gap-1.5">
          <label htmlFor="set-cpu" className="text-[13px] text-fg">
            CPU limit
          </label>
          <input
            id="set-cpu"
            className={cn(input, cpuErr && "border-status-failed")}
            value={form.cpu_limit}
            disabled={!canEdit}
            onChange={(e) => set({ cpu_limit: e.target.value })}
            aria-describedby="set-cpu-help"
            aria-invalid={!!cpuErr}
          />
          <p
            id="set-cpu-help"
            className={cn(
              "text-xs",
              cpuErr ? "text-status-failed-fg" : "text-muted",
            )}
          >
            {cpuErr || "Cores, such as 0.5 or 2. Empty means no limit."}
          </p>
        </div>
      </div>
      {readFailed(read) && <LoadFailed what="the service’s limits" onRetry={() => void read.refetch()} error={read.error} />}
      {row && (
        <p className="text-xs leading-[17px] text-muted">
          A new service starts at its kind&apos;s default: 1 GiB for a database,
          256 MiB for a cache, about twice its idle use for an admin tool,
          512 MiB for an app
          {row.kind === "app" ? ` such as ${service.name}` : ""}. {service.name}{" "}
          is {row.kind === "app" ? "an app" : `a ${row.kind}`}, so{" "}
          {row.kind === "tool"
            ? toolDefault
              ? `${toolDefault.memory_mb} MiB`
              : "its own"
            : kindDefault[row.kind]}
          . The suggestions below correct it from
          measured use.
        </p>
      )}
      {row && (
        <dl className="overflow-hidden rounded-lg border border-subtle text-[13px]">
          <div className="grid grid-cols-[72px_1fr] gap-3 px-3 py-2.5">
            <dt className="text-muted">Memory</dt>
            <dd className="flex flex-col gap-2">
              <span className="text-fg/85">
                <span className={cn(mono, "text-fg")}>
                  {row.memory.limit
                    ? `${memoryText(row.memory.limit)} limit`
                    : "no limit"}
                </span>
                {row.memory.peak_14d_mb !== null && (
                  <>
                    {" "}
                    · peak {mib(row.memory.peak_14d_mb)} over {row.memory.days}{" "}
                    {row.memory.days === 1 ? "day" : "days"}
                  </>
                )}
                {" · "}
                {row.memory.oom_kills_7d === 0 ? (
                  "no memory kills in 7 days"
                ) : (
                  <span className="text-status-failed-fg">
                    killed for memory {row.memory.oom_kills_7d}{" "}
                    {row.memory.oom_kills_7d === 1 ? "time" : "times"} in 7 days
                  </span>
                )}
              </span>
              {memSuggestion ? (
                <Suggestion
                  s={memSuggestion}
                  field="memory"
                  canEdit={canEdit}
                  filled={form.memory_limit === memSuggestion.value}
                  onUse={() => set({ memory_limit: memSuggestion.value })}
                />
              ) : !row.memory.limit ? (
                <span className="text-xs text-muted">
                  Measuring: a suggestion from use appears after a day of
                  samples.
                </span>
              ) : null}
            </dd>
          </div>
          <div className="grid grid-cols-[72px_1fr] gap-3 border-t border-subtle px-3 py-2.5">
            <dt className="text-muted">CPU</dt>
            <dd className="flex flex-col gap-2">
              <span className="text-fg/85">
                <span className={cn(mono, "text-fg")}>
                  {row.cpu.limit
                    ? `${row.cpu.limit} ${Number(row.cpu.limit) === 1 ? "core" : "cores"} limit`
                    : "no limit"}
                </span>
                {row.cpu.p95_cores !== null && (
                  <> · 95th percentile {cores(row.cpu.p95_cores)} cores</>
                )}
                {row.cpu.throttled_max_pct !== null && row.cpu.limit && (
                  <>
                    {" "}
                    · throttled up to {Math.round(row.cpu.throttled_max_pct)}%
                  </>
                )}
                {row.cpu.wait_avg_pct !== null && (
                  <>
                    {" "}
                    · waited for CPU {Math.round(row.cpu.wait_avg_pct)}% of the
                    time yesterday
                  </>
                )}
              </span>
              {row.cpu.suggestion && (
                <Suggestion
                  s={row.cpu.suggestion}
                  field="cpu"
                  canEdit={canEdit}
                  filled={form.cpu_limit === row.cpu.suggestion.value}
                  onUse={() => set({ cpu_limit: row.cpu.suggestion!.value })}
                />
              )}
            </dd>
          </div>
        </dl>
      )}

    </div>
  );
}

/** How long a deploy may run (D-125), for every kind of service. Empty is
 *  the default. */
export function DeployTimeLimit({
  form,
  set,
  canEdit,
}: {
  form: ConfigForm;
  set: (patch: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const problem = deployTimeoutProblem(form.deploy_timeout_minutes);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <label htmlFor="set-timeout" className="text-[13px] text-fg">
        Deploy time limit
      </label>
      <span className="inline-flex items-center gap-2 self-start">
        <input
          id="set-timeout"
          inputMode="numeric"
          placeholder={String(DEFAULT_DEPLOY_TIMEOUT)}
          className={cn(
            input,
            "w-24 font-mono",
            problem && "border-status-failed",
          )}
          value={form.deploy_timeout_minutes}
          disabled={!canEdit}
          onChange={(e) => set({ deploy_timeout_minutes: e.target.value })}
          aria-describedby="set-timeout-help"
          aria-invalid={!!problem}
        />
        <span className="text-[13px] text-fg/70">minutes</span>
      </span>
      {problem ? (
        <p
          id="set-timeout-help"
          role="alert"
          className="text-xs leading-[17px] text-status-failed-fg"
        >
          {problem}
        </p>
      ) : (
        <p id="set-timeout-help" className="text-xs leading-[17px] text-muted">
          A deploy that runs longer than this fails, and the previous version
          keeps serving. 5 to 240 minutes; empty means the default,{" "}
          <span className={cn(mono, "text-fg")}>{DEFAULT_DEPLOY_TIMEOUT}</span>.
          Raise it for a large build. Time spent waiting for a build slot
          doesn’t count.
        </p>
      )}
    </div>
  );
}

function Suggestion({
  s,
  field,
  canEdit,
  filled,
  onUse,
}: {
  s: ResourceSuggestion;
  field: "memory" | "cpu";
  canEdit: boolean;
  filled: boolean;
  onUse: () => void;
}) {
  const value =
    field === "memory"
      ? memoryText(s.value)
      : `${s.value} ${Number(s.value) === 1 ? "core" : "cores"}`;
  return (
    <div
      role="group"
      aria-label={`${field === "memory" ? "Memory" : "CPU"} suggestion`}
      className="flex flex-wrap items-center gap-2"
    >
      <span
        className={cn(
          "rounded-sm px-[7px] font-mono text-xs leading-[22px]",
          s.direction === "down"
            ? "bg-raised text-fg"
            : "bg-status-pending-soft text-status-pending-fg",
        )}
      >
        {suggestionText(s, field)}
      </span>
      <span className="text-xs text-muted">{s.reason}</span>
      {canEdit && (
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="h-7"
          disabled={filled}
          onClick={onUse}
        >
          {filled ? "In the field" : `Use ${value}`}
        </Button>
      )}
    </div>
  );
}

/** The stack's panel limits as saved. A panel CPU limit goes with a panel
 *  memory limit (PUT /compose-limits takes them as one row). */
function panelLimits(
  rows: ResourceRow[],
): Record<string, { memory: string; cpu: string }> {
  const out: Record<string, { memory: string; cpu: string }> = {};
  for (const r of rows) {
    const panel = r.memory.source === "panel";
    out[r.compose_service] = {
      memory: panel ? r.memory.limit : "",
      cpu: panel ? r.cpu.limit : "",
    };
  }
  return out;
}

/** Resources for a compose stack: a limit per compose service, saved with
 *  their own Save (PUT /compose-limits replaces the panel's set). */
export function ComposeResources({
  service,
  serviceId,
  server,
  canEdit,
  onSaved,
}: {
  service: Service;
  serviceId: string;
  server?: Server;
  canEdit: boolean;
  onSaved: () => void;
}) {
  const queryClient = useQueryClient();
  const hostOver = !!usePanelRoom(service)?.over;
  const key = ["service-resources", serviceId];
  const { data, error, dataUpdatedAt, refetch } = useQuery({
    queryKey: key,
    queryFn: () => resourcesService.service(serviceId),
  });
  const saved = data ? panelLimits(data.rows) : {};
  // Edit a copy, with the read it started from: Save sends that read's
  // version, so a change someone made since is refused, not undone.
  const [draft, setDraft] = useState<{
    limits: Record<string, { memory: string; cpu: string }>;
    read: NonNullable<typeof data>;
  } | null>(null);
  // The Save refused because the limits changed since they were read.
  const [conflict, setConflict] = useState(false);
  const edited = draft?.limits ?? saved;
  const base = draft ? panelLimits(draft.read.rows) : saved;
  const changes = Object.keys(edited).filter(
    (k) =>
      edited[k].memory !== (base[k]?.memory ?? "") ||
      edited[k].cpu !== (base[k]?.cpu ?? ""),
  ).length;
  // A draft with nothing changed in it is no draft: the form follows the read.
  const current = changes > 0 ? edited : saved;
  const invalid = Object.entries(current).filter(
    ([, v]) =>
      (v.memory && !/^\d+(\.\d+)?[mg]$/i.test(v.memory)) ||
      (v.cpu && !(Number(v.cpu) > 0)) ||
      (v.cpu && !v.memory),
  );
  const save = useMutation({
    mutationFn: () => {
      const limits: Record<string, ComposeLimit> = {};
      for (const [name, v] of Object.entries(current)) {
        if (v.memory || v.cpu)
          limits[name] = {
            memory_mb: v.memory ? limitMB(v.memory.toLowerCase()) : 0,
            cpu_cores: v.cpu ? Number(v.cpu) : null,
          };
      }
      return resourcesService.setComposeLimits(serviceId, draft!.read.version, limits);
    },
    onSuccess: (fresh) => {
      setDraft(null);
      setConflict(false);
      queryClient.setQueryData(key, fresh);
      onSaved();
    },
    onError: (err) => {
      if (changedSinceRead(err)) {
        setConflict(true);
        return;
      }
      toast.error(
        err instanceof ApiError ? err.message : "Failed to save the limits",
      );
    },
  });
  // A new read with what was changed here on top: a limit changed here stays
  // so, the rest is what was read.
  const readAgain = async () => {
    const fresh = await refetch();
    if (!fresh.data || fresh.isError) {
      toast.error("Couldn’t read the limits. Try again.");
      return;
    }
    const rebased = panelLimits(fresh.data.rows);
    for (const [name, v] of Object.entries(current)) {
      if (
        v.memory !== (base[name]?.memory ?? "") ||
        v.cpu !== (base[name]?.cpu ?? "")
      )
        rebased[name] = v;
    }
    setDraft({ limits: rebased, read: fresh.data });
    setConflict(false);
    focusAfterReread("cl-save", "cl-h");
  };
  const setRow = (
    name: string,
    patch: Partial<{ memory: string; cpu: string }>,
  ) =>
    setDraft({
      limits: { ...current, [name]: { ...current[name], ...patch } },
      read: changes > 0 ? draft!.read : data!,
    });

  if (!data)
    return error ? (
      <ReadFailed heading="h2" error={error} onRetry={() => void refetch()} />
    ) : (
      <div className="h-24 rounded-lg bg-raised/40 motion-safe:animate-pulse" />
    );
  const missing = data.rows.filter(
    (r) => !r.memory.limit && !current[r.compose_service]?.memory,
  );
  const graceOpen =
    data.grace_until && new Date(data.grace_until).getTime() > dataUpdatedAt;

  return (
    <div className="flex flex-col gap-4">
      <ReservedBars service={service} server={server} />
      {missing.length > 0 && (
        <p className="flex items-start gap-2 rounded-lg border border-status-pending/40 bg-status-pending-soft/40 px-3 py-2.5 text-[13px] text-fg">
          <AlertTriangle
            className="mt-0.5 h-4 w-4 flex-none text-status-pending-fg"
            aria-hidden="true"
          />
          <span>
            A memory limit is required on every compose service
            {graceOpen ? (
              <>
                {" "}
                from{" "}
                <span className={mono}>{formatDay(data.grace_until!)}</span>
              </>
            ) : (
              ""
            )}
            .{" "}
            <span className={mono}>
              {missing.map((r) => r.compose_service).join(", ")}
            </span>{" "}
            {missing.length === 1 ? "has" : "have"} none, so deploys of{" "}
            {service.name} {graceOpen ? "are refused from then" : "are refused"}
            .
          </span>
        </p>
      )}
      <p id="cl-h" tabIndex={-1} className="text-[13px] text-fg/85">
        Limits per compose service. A limit set here overrides the compose
        file&apos;s.
      </p>
      <div className="relative overflow-x-auto rounded-lg border border-subtle">
        <table className="w-full min-w-[620px] border-collapse text-[13px]">
          <thead>
            <tr className="text-left text-xs text-muted">
              <th scope="col" className="py-2 pl-3 pr-2 font-medium">
                Compose service
              </th>
              <th scope="col" className="p-2 font-medium">
                Memory limit
              </th>
              <th scope="col" className="p-2 font-medium">
                Peak, 14 days
              </th>
              <th scope="col" className="p-2 font-medium">
                CPU limit
              </th>
              <th scope="col" className="py-2 pl-2 pr-3 font-medium">
                Suggestion
              </th>
            </tr>
          </thead>
          <tbody>
            {data.rows.map((r) => {
              const v = current[r.compose_service] ?? { memory: "", cpu: "" };
              const s = fitting(r.memory.suggestion, hostOver);
              return (
                <tr
                  key={r.compose_service}
                  className="border-t border-subtle align-top"
                >
                  <td className="py-2.5 pl-3 pr-2">
                    <span className={mono}>{r.compose_service}</span>{" "}
                    <span className="text-xs text-muted">
                      {r.kind}
                      {service.compose_route_service === r.compose_service
                        ? " · routed"
                        : ""}
                    </span>
                  </td>
                  <td className="p-2">
                    <label
                      className="sr-only"
                      htmlFor={`cl-mem-${r.compose_service}`}
                    >
                      Memory limit of {r.compose_service}
                    </label>
                    <input
                      id={`cl-mem-${r.compose_service}`}
                      className={cn(input, "h-8 w-28")}
                      value={v.memory}
                      disabled={!canEdit}
                      placeholder={
                        r.memory.file_limit ? r.memory.file_limit : "none"
                      }
                      onChange={(e) =>
                        setRow(r.compose_service, { memory: e.target.value })
                      }
                    />
                    <p className="mt-1 text-xs text-muted">
                      {v.memory ? (
                        "set in the panel"
                      ) : r.memory.file_limit ? (
                        `${memoryText(r.memory.file_limit)} from the compose file`
                      ) : (
                        <span className="text-status-pending-fg">none set</span>
                      )}
                    </p>
                  </td>
                  <td className="p-2 font-mono text-xs text-fg/85">
                    {r.memory.peak_14d_mb !== null
                      ? mib(r.memory.peak_14d_mb)
                      : "—"}
                  </td>
                  <td className="p-2">
                    <label
                      className="sr-only"
                      htmlFor={`cl-cpu-${r.compose_service}`}
                    >
                      CPU limit of {r.compose_service}
                    </label>
                    <input
                      id={`cl-cpu-${r.compose_service}`}
                      className={cn(input, "h-8 w-20")}
                      value={v.cpu}
                      disabled={!canEdit}
                      placeholder={
                        r.memory.source !== "panel" && r.cpu.limit
                          ? r.cpu.limit
                          : "none"
                      }
                      onChange={(e) =>
                        setRow(r.compose_service, { cpu: e.target.value })
                      }
                    />
                  </td>
                  <td className="py-2 pl-2 pr-3">
                    {s && canEdit ? (
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        className="h-7"
                        disabled={v.memory === s.value}
                        onClick={() =>
                          setRow(r.compose_service, { memory: s.value })
                        }
                      >
                        {v.memory === s.value
                          ? "In the field"
                          : `Use ${memoryText(s.value)}`}
                      </Button>
                    ) : (
                      <span className="text-xs text-muted">
                        {s ? suggestionText(s, "memory") : "—"}
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="text-xs leading-[17px] text-muted">
        {graceOpen
          ? `Until ${formatDay(data.grace_until!)} a limit is suggested only from measured use. After it, a compose service with no use measured is offered its kind's default: 1 GiB for a database, 256 MiB for a cache, 512 MiB for an app.`
          : "A compose service with no use measured is offered its kind's default: 1 GiB for a database, 256 MiB for a cache, 512 MiB for an app."}
      </p>
      {conflict && (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg"
        >
          <p className="min-w-0 flex-[1_1_320px]">
            These limits changed since you opened this page, so nothing was
            saved. What you typed is still here.
          </p>
          <Button variant="outline" size="sm" onClick={() => void readAgain()}>
            Read them again
          </Button>
        </div>
      )}
      {canEdit && changes > 0 && (
        <div className="-mx-4 -mb-4 flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-subtle bg-raised/40 px-4 py-2.5 text-[13px]">
          <span className="text-fg">
            {changes === 1 ? "1 unsaved change" : `${changes} unsaved changes`}
          </span>
          <span
            className={invalid.length ? "text-status-failed-fg" : "text-muted"}
          >
            {invalid.length
              ? `Fix ${invalid.map(([n]) => n).join(", ")}: memory as 512m or 1g, CPU as cores, and a CPU limit needs a memory limit.`
              : "Applies from the next deploy."}
          </span>
          <span className="flex-1" />
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setDraft(null);
              setConflict(false);
            }}
            disabled={save.isPending}
          >
            Discard
          </Button>
          <Button
            id="cl-save"
            variant="outline"
            size="sm"
            onClick={() => save.mutate()}
            disabled={invalid.length > 0 || save.isPending || !!conflict}
          >
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      )}
    </div>
  );
}

/** The Resources card's header summary for a compose stack. */
export function limitsSummary(rows: ResourceRow[] | undefined): string {
  if (!rows) return "";
  const none = rows
    .filter((r) => !r.memory.limit)
    .map((r) => r.compose_service);
  if (none.length)
    return `${none.join(", ")} ${none.length === 1 ? "has" : "have"} no memory limit`;
  return rows
    .map((r) => `${r.compose_service} ${memoryText(r.memory.limit)}`)
    .join(" · ");
}

/** How many of its last live deployments keep their image on the server
 *  (D-135; board ServerDiskActions, 9). Empty is the default. */
export function RollbackImagesKept({
  form,
  set,
  canEdit,
}: {
  form: ConfigForm;
  set: (patch: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const problem = rollbackKeptProblem(form.rollback_images_kept);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <label htmlFor="set-keep" className="text-[13px] text-fg">
        Rollback images kept
      </label>
      <span className="inline-flex items-center gap-2 self-start">
        <input
          id="set-keep"
          inputMode="numeric"
          placeholder={String(DEFAULT_ROLLBACK_KEPT)}
          className={cn(input, "w-16 font-mono", problem && "border-status-failed")}
          value={form.rollback_images_kept}
          disabled={!canEdit}
          onChange={(e) => set({ rollback_images_kept: e.target.value })}
          aria-describedby="set-keep-help"
          aria-invalid={!!problem}
        />
        <span className="text-[13px] text-fg/70">deploys</span>
        {!form.rollback_images_kept.trim() && <span className="text-xs text-muted">default</span>}
      </span>
      {problem ? (
        <p id="set-keep-help" role="alert" className="text-xs leading-[17px] text-status-failed-fg">
          {problem}
        </p>
      ) : (
        <p id="set-keep-help" className="text-xs leading-[17px] text-muted">
          The images of its last {form.rollback_images_kept.trim() || DEFAULT_ROLLBACK_KEPT} deploys
          stay on the server, so a rollback to any of them starts at once. Older
          images are removed when the server is cleaned. 1 to 100; empty means
          the default, <span className={cn(mono, "text-fg")}>{DEFAULT_ROLLBACK_KEPT}</span>.
        </p>
      )}
    </div>
  );
}
