"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Loader2, RotateCw, X } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton, card, dangerButton, dialogContent, dialogFooter, dialogHeader, dialogText, dialogTitle, fieldInput, fieldLabel, help, primaryButton,
} from "@/components/settings/settings-frame";
import { btnSm, h2, sum } from "@/app/projects/_lib/ui";
import { reauthError } from "@/components/reauth";
import { ApiError, reauthProof } from "@/lib/api/client";
import { diskService, serverService, type DiskClean, type DiskKind, type DiskReport, type DiskVolume } from "@/lib/api/server.service";
import { sinceShort } from "@/lib/time";
import { cn } from "@/lib/utils";
import { bar, cleanRow, CLEAN_ORDER, gb, KIND_LABEL, keptRows, lastAutoNote, policyLine, POLICY_ORDER, safeRows, volumeNote } from "./disk-view";

const running = (c: DiskClean | null | undefined) => c?.outcome === "running";
const codeOf = (err: unknown) => (err instanceof ApiError ? (err.body as { data?: { code?: unknown } } | null)?.data?.code : undefined);
const shortName = (v: DiskVolume) => (v.kind === "anonymous" ? v.name.slice(0, 12) : v.name);

// A table at 640 px and up, a card per row below (the board's .lt).
const th = "border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted";
const tr = "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0";
const td = "border-t border-subtle px-4 py-2.5 align-middle [tr:first-child>&]:border-t-0 max-sm:block max-sm:border-0 max-sm:p-0";

const TONE = {
  running: { dot: "bg-status-building-soft text-status-building", state: "text-status-building-fg", row: "bg-status-building-soft/25" },
  ok: { dot: "bg-status-running-soft text-status-running", state: "text-status-running-fg", row: "" },
  bad: { dot: "bg-status-failed-soft text-status-failed", state: "text-status-failed-fg", row: "" },
};

/** What fills a host's disk, what is safe to delete and what Toft keeps
 *  for rollback (board: ServerDisk; D-135), with freeing space, deleting a
 *  volume and the disk policy (board: ServerDiskActions). serverId null is
 *  the panel's own server, on Instance → Server; canWrite: owners and
 *  operators on a server, operators on the panel's. */
export function DiskCard({ serverId, serverName, canWrite }: { serverId: string | null; serverName?: string; canWrite: boolean }) {
  const queryClient = useQueryClient();
  const panel = serverId === null;
  const where = panel ? "the panel’s server" : (serverName ?? "this server");
  const queryKey = ["disk", serverId];
  const { data, error, isLoading, dataUpdatedAt } = useQuery({ queryKey, queryFn: () => diskService.get(serverId), retry: false });
  const read = useMutation({
    mutationFn: () => diskService.get(serverId, true),
    onSuccess: (r) => queryClient.setQueryData(queryKey, r),
    onError: (err) => toast.error(err instanceof ApiError && err.status < 500 ? err.message : "Failed to read the disk"),
  });
  const cleanKey = ["disk-clean", serverId];
  const clean = useQuery({
    queryKey: cleanKey,
    queryFn: () => diskService.clean(serverId),
    initialData: data?.clean ?? undefined,
    enabled: running(data?.clean) || running(queryClient.getQueryData<DiskClean | null>(cleanKey)),
    refetchInterval: (q) => (running(q.state.data) ? 2000 : false),
  });
  const cleanData = clean.data;
  // When a clean ends the backend drops its read: read again.
  const wasRunning = useRef(false);
  useEffect(() => {
    const now = running(cleanData);
    if (wasRunning.current && !now) queryClient.invalidateQueries({ queryKey: ["disk", serverId] });
    wasRunning.current = now;
  }, [cleanData, queryClient, serverId]);
  // A finished clean seen here wins over a report read while it ran.
  const busy = cleanData ? running(cleanData) : running(data?.clean);
  // Links to #disk (the inbox, Home) arrive before the card renders.
  useEffect(() => {
    if (!isLoading && window.location.hash === "#disk") document.getElementById("disk")?.scrollIntoView({ block: "start" });
  }, [isLoading]);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(t);
  }, []);
  const [freeing, setFreeing] = useState(false);
  const [deleting, setDeleting] = useState<DiskVolume | null>(null);

  return (
    <section id="disk" tabIndex={-1} aria-labelledby="disk-h" className={card}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="disk-h" className={h2}>Disk</h2>
          {data && (
            <span className={sum}>{[`read ${sinceShort(data.read_at, Math.max(now, dataUpdatedAt))}`, lastAutoNote(data)].filter(Boolean).join(" · ")}</span>
          )}
        </div>
        <button type="button" className={btnSm} disabled={read.isPending || isLoading} onClick={() => read.mutate()}>
          <RotateCw className={cn("h-[13px] w-[13px]", read.isPending && "motion-safe:animate-spin")} aria-hidden="true" />
          {read.isPending ? "Reading…" : "Read again"}
        </button>
      </div>
      {isLoading ? (
        <p className="px-4 py-3 text-[13px] text-muted">Reading the disk…</p>
      ) : !data ? (
        <p role="alert" className="px-4 py-3 text-[13px] text-status-failed-fg">
          {error instanceof ApiError && error.status < 500
            ? `Toft couldn’t read the disk: ${error.message}.`
            : `Toft couldn’t read the disk on ${where}. Read again once the server answers.`}
        </p>
      ) : (
        <Report
          data={data}
          clean={cleanData ?? data.clean ?? data.last_clean}
          panel={panel}
          where={where}
          canWrite={canWrite}
          busy={busy}
          onFree={() => setFreeing(true)}
          onDelete={setDeleting}
          serverId={serverId}
        />
      )}
      {data && freeing && (
        <CleanDialog
          report={data}
          serverId={serverId}
          where={where}
          onClose={() => setFreeing(false)}
          onStarted={(c) => {
            setFreeing(false);
            queryClient.setQueryData(cleanKey, c);
            queryClient.setQueryData<DiskReport>(queryKey, (r) => (r ? { ...r, clean: c } : r));
          }}
        />
      )}
      {deleting && (
        <VolumeDialog
          volume={deleting}
          serverId={serverId}
          where={where}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            toast.success(`Deleted the volume ${shortName(deleting)}`);
            // Its Delete… is gone with it: Radix has nothing to return focus to.
            requestAnimationFrame(() => document.getElementById("disk-vol-h")?.focus());
            queryClient.setQueryData<DiskReport>(queryKey, (r) => (r ? { ...r, volumes: r.volumes.filter((v) => v.name !== deleting.name) } : r));
          }}
        />
      )}
    </section>
  );
}

function Report({ data, clean, panel, where, canWrite, busy, onFree, onDelete, serverId }: {
  data: DiskReport;
  clean: DiskClean | null;
  panel: boolean;
  where: string;
  canWrite: boolean;
  busy: boolean;
  onFree: () => void;
  onDelete: (v: DiskVolume) => void;
  serverId: string | null;
}) {
  const [allVolumes, setAllVolumes] = useState(false);
  const safe = safeRows(data);
  const kept = keptRows(data, panel);
  const b = bar(data);
  const used = data.size_bytes ? Math.round((data.used_bytes / data.size_bytes) * 100) : 0;
  const volBytes = data.volumes.reduce((n, v) => n + Math.max(0, v.bytes), 0);
  const volumes = allVolumes ? data.volumes : data.volumes.slice(0, 4);
  // A person's clean shows until the next; an automatic one only when it failed.
  const status = clean && (clean.outcome === "running" || !clean.automatic || clean.outcome === "failed") ? clean : null;
  return (
    <>
      <div className="flex flex-col gap-3.5 p-4">
        <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-[13px]">
          <span>
            <span className="font-mono font-medium">{gb(data.used_bytes)}</span> of {gb(data.size_bytes)} used · {used}%
          </span>
          <span className="text-muted">
            {gb(data.free_bytes)} free · alert at {data.policy.effective.alert_percent}%
          </span>
        </div>
        <div role="img" aria-label={`${gb(data.used_bytes)} of ${gb(data.size_bytes)} used: ${gb(b.safe)} safe to delete, ${gb(b.kept)} kept, ${gb(b.rest)} everything else`}
          className="flex h-2.5 overflow-hidden rounded-xs bg-subtle">
          <span className="bg-link" style={{ width: b.safePct }} />
          <span className="bg-fg/75" style={{ width: b.keptPct }} />
          <span className="bg-muted/60" style={{ width: b.restPct }} />
        </div>
        <div className="flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-fg/70">
          <Swatch className="bg-link">Safe to delete {gb(b.safe)}</Swatch>
          <Swatch className="bg-fg/75">Kept {gb(b.kept)}</Swatch>
          <Swatch className="bg-muted/60">Everything else {gb(b.rest)}</Swatch>
        </div>
        <div className="@container">
          <div className="grid grid-cols-1 gap-3.5 @min-[560px]:grid-cols-2">
            <div className="flex min-w-0 flex-col gap-1.5">
              <h3 className="text-[13px] font-medium">Safe to delete</h3>
              {safe.length > 0 ? (
                <>
                  <dl className="flex flex-col text-[13px]">
                    {safe.map((d) => (
                      <div key={d.kind} title={d.note} className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 border-t border-subtle py-1.5 first:border-t-0">
                        <dt className="text-muted">{d.k} · {d.count}</dt>
                        <dd className="whitespace-nowrap font-mono text-xs tabular-nums text-fg">{d.v}</dd>
                      </div>
                    ))}
                  </dl>
                  <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
                    {canWrite && (
                      <button type="button" aria-disabled={busy} aria-describedby="disk-free-note"
                        className={cn(btnSm, "aria-disabled:cursor-not-allowed aria-disabled:opacity-50")}
                        onClick={() => { if (!busy) onFree(); }}>
                        Free {gb(b.safe)}…
                      </button>
                    )}
                    <span id="disk-free-note" className={help}>{busy ? "A clean is running." : "Nothing that runs or a rollback needs is touched."}</span>
                  </div>
                </>
              ) : (
                <p className="text-[13px] text-muted">Nothing here is safe to delete right now.</p>
              )}
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <h3 className="text-[13px] font-medium">Kept</h3>
              <dl className="flex flex-col text-[13px]">
                {kept.map((d) => (
                  <div key={d.k} title={d.note} className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 border-t border-subtle py-1.5 first:border-t-0">
                    <dt className="text-muted">{d.k}{d.count && ` · ${d.count}`}</dt>
                    <dd className="whitespace-nowrap font-mono text-xs tabular-nums text-fg">{d.v}</dd>
                  </div>
                ))}
              </dl>
            </div>
          </div>
        </div>
      </div>

      {status && <StatusRow clean={status} where={where} />}

      {data.volumes.length > 0 && (
        <>
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 border-t border-subtle px-4 pb-2 pt-3">
            <h3 id="disk-vol-h" tabIndex={-1} className="text-[13px] font-semibold outline-none">Volumes no service uses</h3>
            <span className={sum}>{data.volumes.length} · {gb(volBytes)} · never deleted automatically</span>
          </div>
          <table aria-labelledby="disk-vol-h" className="w-full border-collapse text-[13px] max-sm:block">
            <thead className="max-sm:hidden">
              <tr>
                <th scope="col" className={th}>Volume</th>
                <th scope="col" className={th}>Size</th>
                <th scope="col" className={th}><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody className="max-sm:block">
              {volumes.map((v) => (
                <tr key={v.name} className={tr}>
                  <td className={cn(td, "max-sm:col-span-2 max-sm:row-start-1")}>
                    <span title={v.name} className="font-mono text-xs text-fg [overflow-wrap:anywhere]">{shortName(v)}</span>
                    {v.kind === "anonymous" && (
                      <span className="ml-2 inline-flex rounded bg-status-pending-soft px-1.5 text-[11px] font-semibold leading-[18px] text-status-pending-fg">may hold data</span>
                    )}
                    <div className={help}>{volumeNote(v)}</div>
                  </td>
                  <td className={cn(td, "whitespace-nowrap font-mono sm:w-[120px] text-xs max-sm:col-start-1 max-sm:row-start-2 max-sm:self-center")}>
                    {v.bytes < 0 ? "size unknown" : gb(v.bytes)}
                  </td>
                  <td className={cn(td, "text-right sm:w-px max-sm:col-start-2 max-sm:row-start-2")}>
                    {canWrite && (
                      <button type="button" className={btnSm} aria-label={`Delete the volume ${shortName(v)}…`} onClick={() => onDelete(v)}>
                        Delete…
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {data.volumes.length > 4 && (
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
              <span>{volumes.length} of {data.volumes.length} shown</span>
              <button type="button" className="touch-target text-link hover:underline" aria-expanded={allVolumes} onClick={() => setAllVolumes(!allVolumes)}>
                {allVolumes ? "Show fewer" : `Show all ${data.volumes.length}`}
              </button>
            </div>
          )}
        </>
      )}

      <Policy report={data} panel={panel} canWrite={canWrite} serverId={serverId} />
    </>
  );
}

function Swatch({ className, children }: { className: string; children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span aria-hidden="true" className={cn("h-2.5 w-2.5 rounded-xs", className)} />
      {children}
    </span>
  );
}

function StatusRow({ clean, where }: { clean: DiskClean; where: string }) {
  const [now, setNow] = useState(() => Date.now());
  const going = running(clean);
  useEffect(() => {
    if (!going) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [going]);
  const v = cleanRow(clean, now, where);
  const tone = TONE[v.tone];
  return (
    <div className={cn("flex items-start gap-3 border-t border-subtle px-4 py-3", tone.row)}>
      <span aria-hidden="true" className={cn("mt-px flex h-[18px] w-[18px] flex-none items-center justify-center rounded-full", tone.dot)}>
        {going ? <Loader2 className="h-[11px] w-[11px] motion-safe:animate-spin" strokeWidth={3} />
          : v.tone === "bad" ? <X className="h-[11px] w-[11px]" strokeWidth={3} />
          : <Check className="h-[11px] w-[11px]" strokeWidth={3} />}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span id="disk-clean" tabIndex={-1} className="flex flex-wrap items-baseline gap-x-2 gap-y-1 outline-none">
          <span className="text-[13px] font-semibold">{v.label}</span>
          <span className={cn("text-xs", tone.state)}>{v.state}</span>
        </span>
        <span role="status" className="sr-only">{going ? "" : `${v.label}, ${v.state}`}</span>
        <span className="text-[13px] text-fg/70">{v.detail}</span>
        <span className="font-mono text-xs text-muted">{v.meta}</span>
      </div>
    </div>
  );
}

/** Free space… where a disk alert is listed (the inbox, D-140): reads the
 *  disk, then opens the same dialog as the disk card. */
export function FreeSpaceButton({ serverId, where, className }: { serverId: string | null; where: string; className?: string }) {
  const queryClient = useQueryClient();
  const [report, setReport] = useState<DiskReport | null>(null);
  const read = useMutation({
    mutationFn: () => diskService.get(serverId),
    onSuccess: (r) => {
      queryClient.setQueryData(["disk", serverId], r);
      setReport(r);
    },
    onError: (err) => toast.error(err instanceof ApiError && err.status < 500 ? err.message : `Couldn’t read the disk on ${where}`),
  });
  return (
    <>
      {/* Not disabled while reading: the dialog gives focus back to this button. */}
      <button type="button" className={className} aria-busy={read.isPending} onClick={() => !read.isPending && read.mutate()}>
        {read.isPending ? "Reading the disk…" : "Free space…"}
      </button>
      {report && (
        <CleanDialog
          report={report}
          serverId={serverId}
          where={where}
          onClose={() => setReport(null)}
          onStarted={(c) => {
            setReport(null);
            queryClient.setQueryData(["disk-clean", serverId], c);
            queryClient.setQueryData<DiskReport>(["disk", serverId], (r) => (r ? { ...r, clean: c } : r));
            toast.success(`Freeing space on ${where}`);
          }}
        />
      )}
    </>
  );
}

/** Confirm a clean: what goes, what stays, and the password. */
function CleanDialog({ report, serverId, where, onClose, onStarted }: {
  report: DiskReport;
  serverId: string | null;
  where: string;
  onClose: () => void;
  onStarted: (c: DiskClean) => void;
}) {
  const rows = safeRows(report);
  const [picked, setPicked] = useState<Set<DiskKind>>(() => new Set(rows.map((r) => r.kind)));
  const [cacheAll, setCacheAll] = useState(false);
  const [password, setPassword] = useState("");
  const [needsPassword] = useState(() => !reauthProof());
  const started = useRef(false);
  const start = useMutation({
    mutationFn: async () => {
      if (needsPassword) await serverService.confirmPassword(password);
      return diskService.start(serverId, CLEAN_ORDER.filter((k) => picked.has(k)), cacheAll);
    },
    onSuccess: (c) => {
      started.current = true;
      onStarted(c);
    },
  });
  const total = rows.filter((r) => picked.has(r.kind)).reduce((n, r) => n + r.bytes, 0) + (cacheAll ? report.build_cache.reclaimable_bytes : 0);
  const code = codeOf(start.error);
  const blocked = code === "deploy_running" || code === "disk_clean_running";
  const inUse = report.classes.find((c) => c.kind === "in_use");
  const rb = report.classes.find((c) => c.kind === "rollback");
  const keptText = [
    inUse && inUse.count > 0 ? `the ${inUse.count} ${inUse.count === 1 ? "image" : "images"} in use` : "",
    serverId === null ? "the running release and the one before, for toft rollback" : "",
    rb && rb.count > 0 ? `each service’s last deploys kept for rollback (${gb(rb.bytes)})` : "",
    "every volume",
    cacheAll ? "" : `the build cache up to its ${gb(report.build_cache.limit_bytes)} limit`,
  ].filter(Boolean);
  const kept = keptText.slice(0, -1).join(", ") + (keptText.length > 1 ? " and " : "") + keptText[keptText.length - 1];
  const toggle = (k: DiskKind, on: boolean) => setPicked((s) => { const n = new Set(s); if (on) n.add(k); else n.delete(k); return n; });
  return (
    <Dialog open onOpenChange={(o) => { if (!o && !start.isPending) onClose(); }}>
      <DialogContent className={cn(dialogContent, "max-w-[560px]")}
        onCloseAutoFocus={(e) => {
          if (!started.current) return;
          e.preventDefault();
          requestAnimationFrame(() => document.getElementById("disk-clean")?.focus());
        }}>
        <form onSubmit={(e) => { e.preventDefault(); if (picked.size > 0 || cacheAll) start.mutate(); }}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>Free {gb(total)} on {where}?</DialogTitle>
            <DialogDescription className={dialogText}>
              Removes what’s ticked from Docker on {serverId === null ? "this server. The panel keeps running." : "the server. An image a container starts from in the meantime is left where it is."}
            </DialogDescription>
          </DialogHeader>
          <fieldset className="mx-5 mt-3.5 flex flex-col gap-2.5">
            <legend className="sr-only">What to delete</legend>
            {rows.map((r) => (
              <Tick key={r.kind} checked={picked.has(r.kind)} onChange={(on) => toggle(r.kind, on)} label={r.k} count={r.count} size={r.v} note={r.note} />
            ))}
            <Tick checked={cacheAll} onChange={setCacheAll} label="Clear all build cache" size={gb(report.build_cache.reclaimable_bytes)} warn
              note="All of it, not only what’s over the limit. The next build of each service starts from nothing and takes longer." />
          </fieldset>
          <div className="mx-5 mt-3.5 flex flex-col gap-1 rounded-lg border border-subtle bg-ground px-3 py-2.5">
            <span className="text-xs font-semibold text-status-running-fg">Kept</span>
            <span className="text-[13px] leading-[19px] text-fg/80">{kept.charAt(0).toUpperCase() + kept.slice(1)}.</span>
          </div>
          {needsPassword && !blocked && (
            <div className="mx-5 mt-4 flex flex-col gap-1.5">
              <label htmlFor="disk-pw" className={fieldLabel}>Your password</label>
              <input id="disk-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)}
                aria-describedby="disk-pw-help" className={fieldInput} required />
              <span id="disk-pw-help" className={help}>Good for 10 minutes in this tab.</span>
            </div>
          )}
          {start.error && (
            <p role="alert" className={cn("mx-5 mt-3 rounded-lg px-3 py-2.5 text-[13px] leading-[19px]",
              blocked ? "border border-status-pending/35 bg-status-pending-soft text-fg" : "bg-status-failed-soft text-status-failed-fg")}>
              {code === "deploy_running"
                ? "A deploy runs on this server and may be about to start an image on this list, so this waits until it’s done. Try again then."
                : reauthError(start.error)}
            </p>
          )}
          <div className={cn(dialogFooter, "items-center")}>
            <span className="mr-auto text-xs text-muted">Nothing restarts.{serverId === null ? " Instance operators only." : ""}</span>
            <button type="button" className={cancelButton} onClick={onClose} disabled={start.isPending}>{blocked ? "Close" : "Cancel"}</button>
            {!blocked && (
              <button type="submit" className={dangerButton} disabled={start.isPending || (picked.size === 0 && !cacheAll) || (needsPassword && !password)}>
                {start.isPending ? "Starting…" : `Free ${gb(total)}`}
              </button>
            )}
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Tick({ checked, onChange, label, count, size, note, warn }: {
  checked: boolean; onChange: (on: boolean) => void; label: string; count?: string; size: string; note: string; warn?: boolean;
}) {
  return (
    <label className="flex cursor-pointer items-start gap-2.5">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="mt-[3px] h-4 w-4 flex-none accent-[var(--link)]" />
      <span className="flex min-w-0 flex-1 flex-col gap-px">
        <span className="flex justify-between gap-2">
          <span className="text-[13px] text-fg">{label} {count && <span className="text-xs text-muted">{count}</span>}</span>
          <span className="whitespace-nowrap font-mono text-xs text-fg">{size}</span>
        </span>
        <span className={cn("text-xs leading-[17px]", warn ? "text-status-pending-fg" : "text-muted")}>{note}</span>
      </span>
    </label>
  );
}

/** Delete one volume: type its name, and the password. */
function VolumeDialog({ volume, serverId, where, onClose, onDeleted }: {
  volume: DiskVolume; serverId: string | null; where: string; onClose: () => void; onDeleted: () => void;
}) {
  const name = shortName(volume);
  const [typed, setTyped] = useState("");
  const [password, setPassword] = useState("");
  const [needsPassword] = useState(() => !reauthProof());
  const remove = useMutation({
    mutationFn: async () => {
      if (needsPassword) await serverService.confirmPassword(password);
      await diskService.removeVolume(serverId, volume.name);
    },
    onSuccess: onDeleted,
  });
  const anon = volume.kind === "anonymous";
  const code = codeOf(remove.error);
  const size = volume.bytes < 0 ? "Size unknown" : gb(volume.bytes);
  return (
    <Dialog open onOpenChange={(o) => { if (!o && !remove.isPending) onClose(); }}>
      <DialogContent role="alertdialog" className={cn(dialogContent, "max-w-[560px]")}
        onCloseAutoFocus={(e) => { if (remove.isSuccess) e.preventDefault(); }}>
        <form onSubmit={(e) => { e.preventDefault(); if (typed === name) remove.mutate(); }}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={cn(dialogTitle, "[overflow-wrap:anywhere]")}>Delete the volume {name}?</DialogTitle>
            <DialogDescription className={dialogText}>
              {size} on {where}. {volumeNote(volume)}
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-3.5 flex flex-col gap-1 rounded-lg border border-status-failed/35 bg-status-failed-soft px-3 py-2.5">
            <span className="text-xs font-semibold text-status-failed-fg">{anon ? "It may hold a database" : "Its data is gone for good"}</span>
            <span className="text-[13px] leading-[19px] text-fg/80">
              {anon
                ? "An image’s declared volume keeps its data after its container is gone. If this was the only copy, deleting it loses it."
                : "Docker deletes the volume and everything in it. Toft can’t bring it back."}
            </span>
          </div>
          {anon && (
            <div className="mx-5 mt-3 flex flex-col gap-1.5">
              <span className="text-xs text-fg/70">To look inside it first, on the server:</span>
              <code className="block rounded-lg border border-subtle bg-ground px-2.5 py-2 font-mono text-xs leading-[18px] text-fg/85 [overflow-wrap:anywhere]">
                docker run --rm -v {volume.name}:/v alpine ls -la /v
              </code>
            </div>
          )}
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="vol-name" className={fieldLabel}>Type <span className="font-mono text-fg">{name}</span> to confirm</label>
            <input id="vol-name" autoComplete="off" spellCheck={false} value={typed} onChange={(e) => setTyped(e.target.value)} className={cn(fieldInput, "font-mono text-xs")} />
          </div>
          {needsPassword && (
            <div className="mx-5 mt-3 flex flex-col gap-1.5">
              <label htmlFor="vol-pw" className={fieldLabel}>Your password</label>
              <input id="vol-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} className={fieldInput} required />
            </div>
          )}
          {remove.error && (
            <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-failed-fg">
              {code === "volume_in_use" ? "A container mounts it now, so Docker kept it." : code === "volume_not_removable" ? "A service owns it now, so Toft kept it." : reauthError(remove.error)}
            </p>
          )}
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={onClose} disabled={remove.isPending}>Cancel</button>
            <button type="submit" className={dangerButton} disabled={remove.isPending || typed !== name || (needsPassword && !password)}>
              {remove.isPending ? "Deleting…" : "Delete volume"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** The disk policy: closed, each value and whether it's the default; open, the form. */
function Policy({ report, panel, canWrite, serverId }: { report: DiskReport; panel: boolean; canWrite: boolean; serverId: string | null }) {
  const [open, setOpen] = useState(false);
  // Each button goes as the other part opens: move focus along.
  const opened = useRef(false);
  useEffect(() => {
    if (!opened.current) { opened.current = true; return; }
    document.getElementById(open ? "bc-limit" : "disk-policy-change")?.focus();
  }, [open]);
  return (
    <div className="border-t border-subtle">
      {open ? (
        <>
          <h3 id="disk-policy-h" className="px-4 pb-1 pt-3 text-[13px] font-semibold">Disk policy</h3>
          <PolicyForm report={report} panel={panel} serverId={serverId} onDone={() => setOpen(false)} />
        </>
      ) : (
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 px-4 py-2.5 text-[13px] text-muted">
          <span id="disk-policy-h">{policyLine(report.policy)}</span>
          {canWrite && (
            <button id="disk-policy-change" type="button" aria-describedby="disk-policy-h" className={btnSm} onClick={() => setOpen(true)}>Change</button>
          )}
        </div>
      )}
    </div>
  );
}

const AUTO_NOTE: Record<DiskKind, string> = {
  untagged: "No tag, and no container uses them.",
  old_builds: "Builds older than each service’s rollback images kept, and those of deleted services.",
  old_panel_releases: "All but the running release and the one before it.",
  unused_pulled: "The next deploy that needs one pulls it again, so it takes longer.",
};

function PolicyForm({ report, panel, serverId, onDone }: { report: DiskReport; panel: boolean; serverId: string | null; onDone: () => void }) {
  const queryClient = useQueryClient();
  const { effective: e, defaults: d } = report.policy;
  const [limit, setLimit] = useState(String(Math.round(e.build_cache_limit_bytes / 1e9)));
  const [auto, setAuto] = useState<Set<DiskKind>>(() => new Set(e.auto_clean));
  const [alert, setAlert] = useState(String(e.alert_percent));
  const kinds = POLICY_ORDER.filter((k) => panel || k !== "old_panel_releases");
  const defLimit = Math.round(d.build_cache_limit_bytes / 1e9);
  const maxLimit = Math.max(1, Math.floor(report.size_bytes / 1e9));
  const save = useMutation({
    mutationFn: () => {
      const l = Number(limit), a = Number(alert);
      const sameAuto = auto.size === d.auto_clean.length && d.auto_clean.every((k) => auto.has(k));
      // A value equal to its default follows the default.
      return diskService.setPolicy(serverId, {
        build_cache_limit_bytes: l === defLimit ? null : l * 1e9,
        auto_clean: sameAuto ? null : kinds.filter((k) => auto.has(k)),
        alert_percent: a === d.alert_percent ? null : a,
      });
    },
    onSuccess: () => {
      toast.success("Saved the disk policy");
      queryClient.invalidateQueries({ queryKey: ["disk", serverId] });
      onDone();
    },
  });
  const a = Number(alert);
  return (
    <form aria-labelledby="disk-policy-h" className="flex flex-col gap-4 px-4 pb-3.5 pt-2" onSubmit={(ev) => { ev.preventDefault(); save.mutate(); }}>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="bc-limit" className={fieldLabel}>Build cache limit</label>
        <span className="flex flex-wrap items-center gap-2">
          <input id="bc-limit" type="number" inputMode="numeric" min={1} max={maxLimit} step={1} required value={limit} onChange={(ev) => setLimit(ev.target.value)}
            aria-describedby="bc-limit-help" className={cn(fieldInput, "w-[88px] tabular-nums")} />
          <span className="text-[13px] text-fg/70">GB</span>
          {Number(limit) !== defLimit && <Reset onClick={() => setLimit(String(defLimit))}>Reset to {defLimit} GB</Reset>}
        </span>
        <span id="bc-limit-help" className="text-xs leading-[17px] text-muted">
          Default {defLimit} GB, 10% of this disk. Toft trims the cache to this after each build, keeping what builds used last. Docker’s own clean-up starts only when the disk is about 80% full.
        </span>
      </div>
      <fieldset className="flex flex-col gap-2">
        <legend className={cn(fieldLabel, "mb-1.5")}>Clean automatically</legend>
        {kinds.map((k) => (
          <label key={k} className="flex cursor-pointer items-start gap-2">
            <input type="checkbox" checked={auto.has(k)} className="mt-[3px] h-4 w-4 flex-none accent-[var(--link)]"
              onChange={(ev) => setAuto((s) => { const n = new Set(s); if (ev.target.checked) n.add(k); else n.delete(k); return n; })} />
            <span className="flex flex-col gap-px">
              <span className="text-[13px] text-fg">{KIND_LABEL[k]} {d.auto_clean.includes(k) && <span className="text-xs text-muted">default</span>}</span>
              <span className="text-xs leading-[17px] text-muted">{AUTO_NOTE[k]}</span>
            </span>
          </label>
        ))}
        <span className="text-xs leading-[17px] text-muted">Once a day, and at once when the disk alert fires. Volumes and the whole build cache are never cleaned automatically.</span>
      </fieldset>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="alert-at" className={fieldLabel}>Disk alert at</label>
        <span className="flex flex-wrap items-center gap-2">
          <input id="alert-at" type="number" inputMode="numeric" min={50} max={99} step={1} required value={alert} onChange={(ev) => setAlert(ev.target.value)}
            aria-describedby="alert-hint" className={cn(fieldInput, "w-16 tabular-nums")} />
          <span className="text-[13px] text-fg/70">% used</span>
          {a !== d.alert_percent && <Reset onClick={() => setAlert(String(d.alert_percent))}>Reset to {d.alert_percent}%</Reset>}
        </span>
        <span id="alert-hint" className="text-xs leading-[17px] text-muted">
          Default {d.alert_percent}%.{a >= 50 && a <= 99 && a !== d.alert_percent ? ` Set to ${a}%, it resolves under ${a - 5}%, 5 points below.` : ""} Between 50 and 99%.
        </span>
      </div>
      {save.error && (
        <p role="alert" className="rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-failed-fg">{reauthError(save.error)}</p>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <button type="button" className={cn(cancelButton, "h-8")} onClick={onDone} disabled={save.isPending}>Cancel</button>
        <button type="submit" className={cn(primaryButton, "h-8")} disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</button>
      </div>
    </form>
  );
}

function Reset({ onClick, children }: { onClick: () => void; children: React.ReactNode }) {
  return <button type="button" className="text-[13px] text-link hover:underline" onClick={onClick}>{children}</button>;
}
