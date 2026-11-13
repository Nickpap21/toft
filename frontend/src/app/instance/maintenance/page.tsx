"use client";

import { Suspense, useState } from "react";
import { useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RotateCw } from "lucide-react";
import { useIsOperator } from "@/context/AuthContext";
import { upgradeQuery, upgradeService, type UpgradeStatus } from "@/lib/api/instance.service";
import { diskService, updatesService } from "@/lib/api/server.service";
import { sinceShort } from "@/lib/time";
import { cn, formatDateTime } from "@/lib/utils";
import { btnSm, card, h2, sum } from "@/app/projects/_lib/ui";
import { SectionsLayout } from "@/app/servers/[id]/sections";
import { maintenanceCount } from "@/app/servers/[id]/server-view";
import { InstanceFrame } from "@/components/instance/instance-frame";
import { panelMaintenance } from "@/components/instance/instance-view";
import { UpgradeDialog, UpgradeLog } from "@/components/instance/upgrade";
import { UpdatesCard } from "@/components/servers/updates-card";
import { DiskCard } from "@/components/servers/disk-card";

/** Instance → Maintenance (D-139, board ReworkInstanceMaintenance): Toft's
 *  own upgrade, the OS and Docker updates, and the disk, in one scroll as
 *  a server's Maintenance tab. Operators only. */
export default function InstanceMaintenancePage() {
  const isOperator = useIsOperator();
  const updates = useQuery({ queryKey: ["updates", null], queryFn: () => updatesService.get(null), enabled: isOperator }).data;
  const disk = useQuery({ queryKey: ["disk", null], queryFn: () => diskService.get(null), enabled: isOperator, retry: false }).data;
  const items = panelMaintenance(updates ?? undefined, disk);
  const up = useQuery({ ...upgradeQuery, enabled: isOperator }).data;
  // One accent per screen (D-139): the header's Upgrade, when there is one.
  const upgradeShown = up?.install === "installer" && up.available && up.run?.outcome !== "running";
  return (
    <InstanceFrame title="Maintenance">
      <SectionsLayout
        label="Jump to a maintenance section"
        sections={[
          { id: "toft", label: "Toft" },
          { id: "updates", label: "Updates", count: maintenanceCount(items.filter((i) => i.anchor === "updates")) },
          { id: "disk", label: "Disk", count: maintenanceCount(items.filter((i) => i.anchor === "disk")) },
        ]}
      >
        <Suspense>
          <ToftCard />
        </Suspense>
        <div id="updates" tabIndex={-1}>
          <UpdatesCard serverId={null} canCheck canRun accent={!upgradeShown} />
        </div>
        <DiskCard serverId={null} canWrite />
      </SectionsLayout>
    </InstanceFrame>
  );
}

const took = (secs: number) => (secs >= 60 ? `${Math.floor(secs / 60)} min ${secs % 60} s` : `${secs} s`);

/** The release the panel runs, the newest one with what an upgrade does,
 *  and the last upgrade with its log. Was Overview's Version card. */
function ToftCard() {
  const queryClient = useQueryClient();
  const up = useQuery(upgradeQuery);
  const st: UpgradeStatus | undefined = up.data;
  // Needs you's Full log links here (?log=upgrade); the log shows once the run has loaded.
  const params = useSearchParams();
  const [log, setLog] = useState(() => params.get("log") === "upgrade");
  const [confirm, setConfirm] = useState(false);
  const check = useMutation({
    mutationFn: upgradeService.check,
    onSuccess: (s) => queryClient.setQueryData(upgradeQuery.queryKey, s),
  });
  const installer = st?.install === "installer";
  const last = st?.run && st.run.outcome !== "running" ? st.run : null;
  const failed = !!last && last.outcome !== "succeeded";
  const canUpgrade = installer && !!st?.available && st.run?.outcome !== "running";

  const rows: [string, React.ReactNode, string?][] = [];
  if (st) {
    rows.push(["Running", `${st.current}${st.current === "dev" ? " · a build from source" : st.since ? ` since ${formatDateTime(st.since).slice(0, 10)}` : ""}`]);
    if (last) {
      const secs = last.finished_at ? Math.round((new Date(last.finished_at).getTime() - new Date(last.started_at).getTime()) / 1000) : 0;
      const what = last.from ? `${last.from} → ${last.to}` : `${last.to} installed`;
      rows.push([
        "Last upgrade",
        failed ? `${what} ${last.outcome === "rolled_back" ? "failed, rolled back" : "stopped"} · ${formatDateTime(last.started_at).slice(0, 16)}` : `${what} · ${took(secs)}`,
        failed ? "text-status-pending-fg" : undefined,
      ]);
    }
    rows.push(["Installed with", installer ? "install.sh" : "deploy.sh: upgrades from here are off"]);
  }

  return (
    <section id="toft" tabIndex={-1} aria-labelledby="toft-h" className={cn(card, "focus-visible:outline-none")}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2.5">
          <h2 id="toft-h" className={h2}>
            Toft
          </h2>
          {installer && <span className={sum}>{st?.checked_at ? `checked for releases ${sinceShort(st.checked_at, up.dataUpdatedAt)}, daily` : "not checked for releases yet"}</span>}
        </div>
        {installer && (
          <button type="button" className={btnSm} disabled={check.isPending} onClick={() => check.mutate()}>
            <RotateCw className={cn("h-[13px] w-[13px]", check.isPending && "motion-safe:animate-spin")} aria-hidden="true" />
            {check.isPending ? "Checking…" : "Check now"}
          </button>
        )}
      </div>

      {!st ? (
        <p aria-busy="true" role={up.isError ? "alert" : undefined} className="px-4 py-3.5 text-[13px] text-muted">
          {up.isError ? `Could not read the release: ${up.error.message}` : "Reading the release…"}
        </p>
      ) : (
        <>
          {canUpgrade && (
            <div className="flex flex-col gap-3 border-b border-subtle px-4 py-3.5">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="flex min-w-0 flex-col gap-0.5">
                  <h3 className="text-[15px] font-semibold leading-[22px] text-fg">{st.latest} is available</h3>
                  <p className="text-[13px] text-muted">
                    You run {st.current}.{" "}
                    {st.notes_url && (
                      <a href={st.notes_url} target="_blank" rel="noreferrer" className="inline-link">
                        Release notes ↗
                      </a>
                    )}
                  </p>
                </div>
                <button type="button" onClick={() => setConfirm(true)} className={cn(btnSm, "h-9 px-3 max-sm:w-full max-sm:justify-center pointer-coarse:h-11")}>
                  {failed && last?.to === st.latest ? "Try again…" : `Upgrade to ${st.latest}…`}
                </button>
              </div>
              <ol className="flex list-decimal flex-col gap-1 pl-5 text-[13px] leading-[19px] text-fg/85">
                <li>Pulls {st.latest} while {st.current} keeps serving.</li>
                <li>Saves the database, then moves it to {st.latest}.</li>
                <li>Restarts the panel on {st.latest}: it’s away for about 30 seconds; apps keep running.</li>
                <li>
                  If {st.latest} isn’t healthy within 2 minutes, {st.current} starts again by itself.
                </li>
              </ol>
            </div>
          )}
          <dl className="flex flex-col px-4 py-1.5 text-[13px]">
            {rows.map(([dt, dd, tint], i) => (
              <div key={dt} className={cn("flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-2", i > 0 && "border-t border-subtle")}>
                <dt className="text-muted">{dt}</dt>
                <dd className={cn("min-w-0 text-right font-mono text-xs", tint ?? "text-fg")}>{dd}</dd>
              </div>
            ))}
          </dl>
          {st.check_error && installer && (
            <p role="status" className="border-t border-subtle px-4 py-2.5 text-xs leading-[17px] text-status-pending-fg">
              The last check failed: {st.check_error}
            </p>
          )}
          {installer && (
            <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5 border-t border-subtle px-4 py-3 text-xs text-muted">
              <span>
                Runs <span className="font-mono text-fg/85">toft upgrade</span> on the server, outside the panel, so it carries on while the panel restarts.
              </span>
              {last && (
                <button type="button" onClick={() => setLog(true)} className="touch-target font-medium text-link hover:underline">
                  {failed ? "Full log of the failed upgrade" : "Last upgrade’s log"}
                </button>
              )}
            </div>
          )}
          {last && <UpgradeLog run={last} open={log} onClose={() => setLog(false)} />}
          <UpgradeDialog status={st} open={confirm} onClose={() => setConfirm(false)} />
        </>
      )}
    </section>
  );
}
