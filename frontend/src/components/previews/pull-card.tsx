"use client";

import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ExternalLink, Loader2 } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import type { PullRequestDetail, TimelineItem } from "@/lib/api/code.service";
import { previewService, type Preview, type PreviewService } from "@/lib/api/preview.service";
import { ISOLATION_NAME } from "@/lib/isolation";
import { useCanWrite } from "@/components/code/pulls";
import { LoadFailed } from "@/components/shell/unreachable";
import { ago, pullHref, shortSha, useCode } from "@/components/code/repo";
import { deploymentHref } from "@/components/home/home";
import { formatDuration } from "@/lib/time";
import { cn } from "@/lib/utils";
import { card, primary, small } from "@/app/projects/_lib/ui";
import { commitWaits } from "@/app/projects/[id]/code/[repo]/pulls/[number]/view";
import { joinNames, STATUS, TONE_CHIP } from "./view";

/** Untrusted previews run strict where their server can (D-115): the level
 *  its copies run at once there are any, else where it may run. */
function untrustedRuns(services: PreviewService[]): string {
  const levels = new Set(services.map((s) => s.isolation));
  const [only] = levels;
  return levels.size === 1 ? ISOLATION_NAME[only] : "Sandboxed, strict network where the server can";
}

/** A pull request's preview, if any; null for 404 (none). */
export function usePullPreview(projectId: string, repositoryId: string, number: number) {
  return useQuery({
    queryKey: ["pull-preview", repositoryId, number],
    queryFn: () =>
      previewService.of(projectId, repositoryId, number).get().catch((err) => {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }),
    refetchInterval: (q) => (["starting", "updating", "removing"].includes(q.state.data?.status ?? "") ? 4000 : false),
  });
}

const fact = "text-muted";
const btn = cn(small, "h-[30px] px-2.5 rounded-md");

/** The Preview card at the top of a pull request's side column (boards
 *  ReworkPull, PullRequestPreview, PreviewApproval; D-97). A commit waiting
 *  for its approval is WaitingCard's, for whoever may approve. removed is the
 *  conversation's newest preview_removed event, for a pull request whose
 *  preview is gone. */
export function PullPreviewCard({ pull, removed }: { pull: PullRequestDetail; removed?: TimelineItem }) {
  const { projectId, repo } = useCode();
  const canWrite = useCanWrite();
  const queryClient = useQueryClient();
  const { data: pv, isLoading, error, refetch, dataUpdatedAt: now } = usePullPreview(projectId, repo.id, pull.number);
  const { data: settings } = useQuery({ queryKey: ["preview-settings", projectId], queryFn: () => previewService.settings(projectId) });
  const api = previewService.of(projectId, repo.id, pull.number);
  const done = (v: Preview) => {
    queryClient.setQueryData(["pull-preview", repo.id, pull.number], v);
    queryClient.invalidateQueries({ queryKey: ["pull-timeline"] });
    queryClient.invalidateQueries({ queryKey: ["project-previews", projectId] });
  };
  const fail = (what: string) => (err: unknown) => toast.error(err instanceof ApiError && err.status < 500 ? err.message : `Couldn’t ${what} the preview`);
  const start = useMutation({ mutationFn: api.start, onSuccess: done, onError: fail("start") });
  const stop = useMutation({ mutationFn: api.stop, onSuccess: done, onError: fail("stop") });
  const redeploy = useMutation({ mutationFn: api.redeploy, onSuccess: done, onError: fail("redeploy") });

  if (isLoading) return null;
  if (error && !pv) {
    return (
      <Shell chip={null}>
        <LoadFailed what="the preview" onRetry={() => void refetch()} />
      </Shell>
    );
  }
  if (!pv) {
    if (removed && pull.state !== "open") return <RemovedCard pull={pull} removed={removed} now={now} />;
    if (pull.state !== "open" || !settings?.environment_id || !canWrite) return null;
    return (
      <Shell chip={null}>
        <p className="text-[13px] leading-[19px] text-fg/80">No preview yet. One starts at the next push, or now.</p>
        <div><button type="button" className={btn} onClick={() => start.mutate()} disabled={start.isPending}>{start.isPending ? "Starting…" : "Start preview"}</button></div>
      </Shell>
    );
  }

  // While WaitingCard asks for approval, the card shows what keeps running.
  const waits = commitWaits(pv, canWrite);
  const status = STATUS[pv.status];
  const runs = pv.untrusted && pv.approved_sha ? pv.approved_sha : pv.head_sha;
  // What is deployed, which isn't the approved commit when that was refused;
  // "" when the services differ, and Addresses names each one's.
  const live = new Set(pv.services.map((s) => s.sha).filter(Boolean));
  const deployed = live.size === 1 ? [...live][0] : "";
  const refused = pv.services.filter((s) => s.status === "running" && s.error);
  const refusal = refused.length > 0 && (
    <div role="alert" className="flex flex-col gap-1.5 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-failed-fg">
      <span>Toft refused to build {joinNames(refused.map((s) => s.name))} from {shortSha(runs)}. The previous commit keeps running.</span>
      <code className="break-words font-mono text-xs leading-[17px]">{refused[0].error}</code>
    </div>
  );
  const first = pv.services[0];
  const logs = first && <Link href={`/projects/${projectId}/services/${first.id}?tab=logs`} className={btn}>Logs</Link>;
  const shell = first && <Link href={`/projects/${projectId}/services/${first.id}?shell=1`} className={btn}>Shell</Link>;
  const stopBtn = canWrite && (
    <button type="button" className={cn(btn, "text-fg/70")} onClick={() => stop.mutate()} disabled={stop.isPending}>{stop.isPending ? "Stopping…" : "Stop"}</button>
  );
  const redeployBtn = canWrite && (
    <button type="button" className={btn} onClick={() => redeploy.mutate()} disabled={redeploy.isPending}>{redeploy.isPending ? "Redeploying…" : "Redeploy"}</button>
  );
  const chip = (
    <span className={cn("inline-flex h-[22px] items-center gap-1.5 whitespace-nowrap rounded-full px-2 text-xs font-medium", TONE_CHIP[status.tone])}>
      {pv.status === "starting" || pv.status === "updating" || pv.status === "removing" ? (
        <Loader2 className="h-[11px] w-[11px] motion-safe:animate-spin" aria-hidden="true" />
      ) : (
        <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />
      )}
      {status.tone === "running" && deployed ? `${status.label} ${shortSha(deployed)}` : status.label}
    </span>
  );
  const reachedLine = pv.reached.length ? ` · ${joinNames(pv.reached)} in ${pv.copies}` : "";
  const serverName = first?.server || "the panel's server";

  // An untrusted preview with a commit to approve, for a viewer who can't
  // approve it, or that can't start yet.
  if (!waits && (pv.status === "needs_approval" || pv.status === "blocked" || (pv.needs_approval && pv.untrusted))) {
    const blocked = pv.status === "blocked";
    // An agent's pull request takes the API key's path (D-102): it names the
    // agent, not a key.
    const agent = pv.opened_with_key?.agent;
    const lead = !canWrite
      ? pv.approved_sha
        ? `A newer commit waits for an owner or developer to approve it.${pv.status === "running" ? ` Until then the preview keeps running ${shortSha(pv.approved_sha)}.` : ""}`
        : `${agent ? `Opened by ${agent}, an agent` : "Opened with an API key"}, so an owner or developer approves the preview first. It then runs Sandboxed, with a strict network where its server can, and none of ${pv.copies}’s secrets or services.`
      : pv.untrusted ? `A preview ${agent ? "an agent opened" : "opened with an API key"} runs Sandboxed, and can’t start here yet.` : "This preview can’t start yet.";
    return (
      <Shell chip={chip}>
        <p className="text-[13px] leading-[19px] text-fg/85">{lead}</p>
        {blocked && pv.note && canWrite && (
          <p role="note" className="rounded-lg bg-status-pending-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-pending-fg">{pv.note}</p>
        )}
        {!canWrite && pv.approved_sha && (pv.status === "running" || pv.status === "updating") && <Addresses pv={pv} />}
      </Shell>
    );
  }

  if (pv.status === "waiting_room") {
    return (
      <Shell chip={chip}>
        <p className="text-[13px] leading-[19px] text-fg/85">
          {settings?.max_per_project ?? 5} previews run in this project already. This one starts when one of them is removed.
        </p>
      </Shell>
    );
  }

  if (pv.status === "stopped") {
    return (
      <Shell chip={chip}>
        <p className="text-[13px] leading-[19px] text-fg/85">
          {pv.stopped_reason === "room"
            ? `Stopped ${ago(pv.updated_at, now)}: ${pv.note}. A server stops its oldest preview first, and never stops production or staging.`
            : `Stopped ${ago(pv.updated_at, now)}.`}
        </p>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-[13px]">
          <dt className={fact}>Commit</dt><dd className="font-mono text-xs">{shortSha(deployed || runs)}</dd>
          <dt className={fact}>Removed</dt><dd>when !{pull.number} merges or closes</dd>
        </dl>
        <div className="flex flex-wrap gap-2">
          {canWrite && <button type="button" className={btn} onClick={() => start.mutate()} disabled={start.isPending}>{start.isPending ? "Starting…" : "Start again"}</button>}
          {logs}
        </div>
        {pv.stopped_reason === "room" && <p className="text-xs leading-[17px] text-muted">A push starts it again too, if it fits; if it doesn’t, the push’s preview check fails with the numbers.</p>}
      </Shell>
    );
  }

  const building = pv.services.filter((s) => s.status === "starting");
  const failed = pv.services.find((s) => s.status === "failed");

  if (pv.status === "starting" || pv.status === "updating") {
    return (
      <Shell chip={chip}>
        <p className="text-[13px] leading-[19px] text-fg/85">
          {pv.status === "starting"
            ? `Building ${joinNames(pv.services.map((s) => s.name))} from ${shortSha(runs)}.${pv.reached.length ? ` ${joinNames(pv.reached)} ${pv.reached.length === 1 ? "is" : "are"} ${pv.copies}’s.` : ""}`
            : `Updating to ${shortSha(runs)}. Each service switches when its new container is healthy.`}
        </p>
        {refusal}
        <MigrationsNote pv={pv} number={pull.number} />
        {pv.status === "updating" && <Addresses pv={pv} />}
        <ol aria-label="Services" className="flex flex-col rounded-lg border border-subtle">
          {pv.services.map((s, i) => (
            <li key={s.id} className={cn("flex items-center gap-2.5 px-2.5 py-2 text-[13px]", i > 0 && "border-t border-subtle")}>
              <span className={cn("h-2 w-2 flex-none rounded-full", s.status === "running" ? "bg-status-running" : s.status === "failed" ? "bg-status-failed" : "bg-status-building")} aria-hidden="true" />
              <span className="font-medium">{s.name}</span>
              <span className="text-fg/70">{s.status === "running" ? `Live on ${shortSha(s.sha ?? "")}` : s.status === "failed" ? "Failed" : "Building"}</span>
              <span className="flex-1" />
              <span className="font-mono text-xs text-muted">{s.started_at ? formatDuration(Math.round(((s.finished_at ? Date.parse(s.finished_at) : now) - Date.parse(s.started_at)) / 1000)) : ""}</span>
            </li>
          ))}
        </ol>
        <dl className="flex flex-col text-[13px]">
          <Fact name="Server">{serverName}</Fact>
          <Fact name="Isolation">{pv.untrusted ? `${untrustedRuns(pv.services)}; a network of its own, no secrets` : "Standard"}</Fact>
          <Fact name="Copies">{pv.untrusted ? `${pv.copies}’s plain variables only` : `${pv.copies}’s variables and secrets`}</Fact>
        </dl>
        <div className="flex flex-wrap gap-2">{building[0] ? <Link href={deploymentHref(projectId, building[0].id, building[0].deployment_id ?? "")} className={btn}>Logs</Link> : logs}</div>
      </Shell>
    );
  }

  return (
    <Shell chip={chip}>
      {failed && (
        <div role="alert" className="flex flex-col gap-1.5 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-failed-fg">
          <span>{failed.deployment_id ? `${failed.name}’s deploy${failed.sha ? ` on ${shortSha(failed.sha)}` : ""} failed.` : `Toft refused to build ${failed.name}.`}</span>
          {failed.error && <code className="break-words font-mono text-xs leading-[17px]">{failed.error}</code>}
        </div>
      )}
      {refusal}
      <MigrationsNote pv={pv} number={pull.number} />
      {pv.untrusted && pv.approved_by && !waits && <p className="text-[13px] text-fg/85">{pv.approved_by} approved {shortSha(pv.approved_sha ?? "")}{pv.approved_at ? ` ${ago(pv.approved_at, now)}` : ""}.</p>}
      {(!waits || pv.approved_sha) && <Addresses pv={pv} />}
      <dl className="flex flex-col text-[13px]">
        {first && <Fact name="Runs">{pv.services.map((s) => s.name).join(", ")} from !{pull.number} on {serverName}<span className="text-muted">{reachedLine}</span></Fact>}
        <Fact name="Isolation">{pv.untrusted ? `${untrustedRuns(pv.services)}; a network of its own, no secrets` : "Standard"}</Fact>
        <Fact name="Copies">{pv.untrusted ? `${pv.copies}’s plain variables only` : `${pv.copies}’s variables and secrets`}</Fact>
        {waits && <Fact name="Next">{shortSha(pv.head_sha)}, once approved</Fact>}
      </dl>
      <div className="flex flex-wrap gap-2 pt-0.5">
        {failed ? (failed.deployment_id && <Link href={deploymentHref(projectId, failed.id, failed.deployment_id)} className={btn}>Build log</Link>) : logs}
        {!failed && shell}
        {!pv.untrusted || failed ? redeployBtn : null}
        {first && stopBtn}
      </div>
    </Shell>
  );
}

/** The migrations the pull request touches, which its preview didn't run (D-144, board PullRequestPreview, state 7),
 *  or that its diff was too large to check for them (D-147). */
function MigrationsNote({ pv, number }: { pv: Preview; number: number }) {
  const files = pv.migration_files;
  if (!files.length && !pv.migrations_unchecked) return null;
  const count = files.length === 1 ? "1 migration file" : `${files.length} migration files`;
  return (
    <div role="note" className="flex flex-col gap-1.5 rounded-lg bg-status-pending-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-pending-fg">
      {pv.migrations_unchecked ? (
        <span>
          <b className="font-semibold">Diff too large to check for migrations.</b> Toft didn’t run the migrate commands, and can’t tell whether this
          diff changes migrations. Post-deploy commands and each image’s own entrypoint still run, and may migrate {pv.copies}’s database.
          {files.length ? ` Its first 3,000 files include ${count}:` : null}
        </span>
      ) : (
        <span>
          <b className="font-semibold">Toft didn’t run the migrate commands.</b> !{number} changes {count}. A preview never runs its services’ migrate or
          pre-deploy commands, so {joinNames(pv.services.map((s) => s.name))} {pv.services.length === 1 ? "runs" : "run"} on {pv.copies}’s schema as it is: code
          that needs these changes may fail here. Post-deploy commands and each image’s own entrypoint still run, and may migrate {pv.copies}’s database.
        </span>
      )}
      {files.length > 0 && (
        <>
          <code className="whitespace-pre-line break-all font-mono text-xs leading-[18px]">{files.join("\n")}</code>
          <span className="text-xs">They run when the change deploys to an environment.</span>
        </>
      )}
    </div>
  );
}

function Shell({ chip, children }: { chip: React.ReactNode; children: React.ReactNode }) {
  return (
    <section aria-labelledby="prev-h" className={card}>
      <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1.5 border-b border-subtle px-4 py-3">
        <h2 id="prev-h" className="whitespace-nowrap text-[15px] font-semibold leading-[22px] text-fg">Preview</h2>
        {chip}
      </div>
      <div className="flex flex-col gap-3 px-4 pb-4 pt-3.5">{children}</div>
    </section>
  );
}

/** A fact row: name beside value from md up, above it on a phone (board ReworkPull). */
function Fact({ name, children }: { name: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1 border-t border-subtle py-2.5 first:border-t-0 first:pt-0 last:pb-0 md:grid md:grid-cols-[96px_minmax(0,1fr)] md:gap-3">
      <dt className="text-muted">{name}</dt>
      <dd className="text-fg">{children}</dd>
    </div>
  );
}

/** A commit waits for a person (board ReworkPull): an untrusted preview's
 *  next commit, leading the conversation for whoever may approve it (D-97).
 *  items is the conversation loaded so far, for who pushed it. */
export function WaitingCard({ pull, items }: { pull: PullRequestDetail; items: TimelineItem[] }) {
  const { projectId, repo, base, api: code } = useCode();
  const canWrite = useCanWrite();
  const queryClient = useQueryClient();
  const { data: pv, dataUpdatedAt: now } = usePullPreview(projectId, repo.id, pull.number);
  const waits = commitWaits(pv, canWrite);
  const sha = pv?.head_sha ?? "";
  const commit = useQuery({ queryKey: ["code-commit", code.id, sha], queryFn: () => code.commit(sha), enabled: waits && !!sha });
  const approve = useMutation({
    mutationFn: () => previewService.of(projectId, repo.id, pull.number).approve(sha),
    onSuccess: (v: Preview) => {
      queryClient.setQueryData(["pull-preview", repo.id, pull.number], v);
      queryClient.invalidateQueries({ queryKey: ["pull-timeline"] });
      queryClient.invalidateQueries({ queryKey: ["project-previews", projectId] });
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) {
        queryClient.invalidateQueries({ queryKey: ["pull", code.id, pull.number] });
        queryClient.invalidateQueries({ queryKey: ["pull-preview", repo.id, pull.number] });
      }
      toast.error(err instanceof ApiError && err.status < 500 ? err.message : "Couldn’t approve the preview");
    },
  });
  if (!pv || !waits) return null;
  const push = items.findLast((i) => {
    const d = (i.data ?? {}) as { after?: string; commits?: { sha: string }[] };
    return i.kind === "pushed" && (d.after === sha || !!d.commits?.some((c) => c.sha === sha));
  });
  const c = commit.data;
  return (
    <section aria-labelledby="wait-h" className="overflow-hidden rounded-[10px] border border-status-pending/45 bg-surface">
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 border-b border-subtle px-4 py-3">
        <h2 id="wait-h" className="text-[15px] font-semibold leading-[22px] text-fg">A commit waits for a person</h2>
        <span className="inline-flex h-[22px] items-center whitespace-nowrap rounded-full bg-status-pending-soft px-2 text-xs font-medium text-status-pending-fg">Waiting for you</span>
      </div>
      <div className="flex flex-col gap-3 px-4 py-3.5">
        <p className="text-[13px] leading-[19px] text-fg/85">
          {push ? (
            <>
              {push.actor?.agent ?? push.actor?.name ?? "Someone"} pushed <code className="font-mono text-xs text-fg">{shortSha(sha)}</code> {ago(push.created_at, now)}.
            </>
          ) : (
            // Its push is on a page of the conversation not loaded yet.
            <>
              <code className="font-mono text-xs text-fg">{shortSha(sha)}</code> was committed{c ? ` ${ago(c.committed_at, now)}` : ""}.
            </>
          )} Its preview runs a commit only after an owner or developer approves it, and even then Sandboxed, with no secrets.{" "}
          {pv.status === "stopped" ? (
            pv.stopped_reason === "room" ? "The preview is stopped for room, so nothing runs now. Approving starts it again if it fits." : "The preview is stopped, so nothing runs now. Approving won’t start it: use Start again."
          ) : pv.status === "failed" ? (
            "Its last deploy failed, so it stays as it is until then."
          ) : pv.approved_sha ? (
            <>
              Until then the preview {pv.status === "running" ? "keeps running" : "stays on"} <code className="font-mono text-xs text-fg">{shortSha(pv.approved_sha)}</code>, which {pv.approved_by || "an owner or developer"} approved.
            </>
          ) : (
            "Nothing runs until then."
          )}
        </p>
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 rounded-lg border border-subtle px-3 py-2.5 text-[13px]">
          <code className="font-mono text-xs text-fg">{shortSha(sha)}</code>
          <span className="min-w-0 flex-[1_1_200px] text-fg">{c?.subject}</span>
          {c && (
            <span className="ml-auto whitespace-nowrap text-xs text-muted">
              +{c.stats.additions} −{c.stats.deletions} · {c.stats.files === 1 ? "1 file" : `${c.stats.files} files`}
            </span>
          )}
        </div>
        <p className="text-xs leading-[17px] text-muted">
          Approving lets the preview run this commit. It doesn’t approve the pull request: merging still needs a person. Its images build outside the sandbox, as every build does: read its Dockerfile changes first.
        </p>
      </div>
      <div className="flex flex-wrap justify-end gap-2 border-t border-subtle px-4 py-3">
        <Link href={pullHref(base, pull.number, "files")} className="touch-target inline-flex h-9 items-center rounded-lg px-3 text-[13px] font-medium text-fg/85 hover:bg-raised hover:text-fg">
          Review the diff
        </Link>
        <button type="button" className={primary} disabled={approve.isPending} onClick={() => approve.mutate()}>
          {approve.isPending ? "Approving…" : `Approve ${shortSha(sha)}`}
        </button>
      </div>
    </section>
  );
}

/** Each service's address, with the commit it runs when they differ. */
function Addresses({ pv, sha }: { pv: Preview; sha?: string }) {
  const rows = pv.services.flatMap((s) => s.addresses.map((a) => ({ svc: s.name, a, sha: sha ?? s.sha ?? "", tone: s.status })));
  if (rows.length === 0) return null;
  const shas = new Set(rows.map((r) => r.sha));
  return (
    <ul aria-label="Addresses" className="flex flex-col gap-1.5">
      {rows.map((r) => (
        <li key={r.a} className="flex min-w-0 items-baseline gap-2">
          {rows.length > 1 && <span className="max-w-[96px] flex-none truncate text-xs text-muted">{r.svc}</span>}
          <a href={`https://${r.a}`} target="_blank" rel="noreferrer" title={r.a} className="inline-flex min-w-0 items-center gap-1.5 font-mono text-xs text-accent-text hover:underline">
            <span className="break-all">{r.a}</span>
            <ExternalLink className="h-3 w-3 flex-none" aria-hidden="true" />
          </a>
          {(shas.size > 1 || sha) && r.sha && (
            <span className={cn("flex-none text-xs", r.sha === pv.head_sha && !sha ? "text-status-running-fg" : "text-muted")}>{shortSha(r.sha)}</span>
          )}
        </li>
      ))}
    </ul>
  );
}

function RemovedCard({ pull, removed, now }: { pull: PullRequestDetail; removed: TimelineItem; now: number }) {
  const d = (removed.data ?? {}) as { reason?: string; services?: string[] };
  return (
    <Shell chip={<span className={cn("inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium", TONE_CHIP.muted)}><span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />Removed</span>}>
      <p className="text-[13px] leading-[19px] text-fg/85">
        Removed {ago(removed.created_at, now)}{d.reason === "merged" ? `, when !${pull.number} merged` : d.reason === "closed" ? `, when !${pull.number} closed` : ""}
        {d.services?.length ? `: ${joinNames(d.services)}’s containers, the preview’s network and its addresses.` : "."} Nothing of the environment it copied changed.
      </p>
      {pull.state === "closed" && pull.head_exists && <p className="text-xs leading-[17px] text-muted">Reopening the pull request starts a new preview from its head.</p>}
    </Shell>
  );
}

