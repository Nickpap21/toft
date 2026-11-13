"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Check, Loader2, Undo2, X } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { upgradeQuery, upgradeService, type UpgradeRun, type UpgradeStatus } from "@/lib/api/instance.service";
import { cn } from "@/lib/utils";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton,
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
import { UPGRADE_STEPS, stepStates, upgradeCard, type StepState } from "./instance-view";

/** The board's confirmation (InstanceUpgrade, 1): what happens, and the
 *  operator's password. */
export function UpgradeDialog({ status, open, onClose }: { status: UpgradeStatus; open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const start = useMutation({
    mutationFn: () => upgradeService.start(status.latest, password),
    onSuccess: (st) => {
      queryClient.setQueryData(upgradeQuery.queryKey, st);
      close();
    },
  });
  const close = () => {
    setPassword("");
    start.reset();
    onClose();
  };
  const code = start.error instanceof ApiError ? (start.error.body as { data?: { code?: string } })?.data?.code : undefined;
  return (
    <Dialog open={open} onOpenChange={(o) => !o && !start.isPending && close()}>
      <DialogContent className={dialogContent}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            start.mutate();
          }}
        >
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>Upgrade Toft to {status.latest}?</DialogTitle>
            <DialogDescription className={dialogText}>
              From {status.current}.{" "}
              {status.notes_url && (
                <a href={status.notes_url} target="_blank" rel="noreferrer" className="inline-link">
                  Release notes ↗
                </a>
              )}
            </DialogDescription>
          </DialogHeader>
          <ol className="mx-5 mt-3.5 flex list-decimal flex-col gap-1.5 pl-5 text-[13px] leading-[19px] text-fg/85">
            <li>Pulls {status.latest} while {status.current} keeps serving.</li>
            <li>Saves the database, then moves it to {status.latest}.</li>
            <li>Restarts the panel on {status.latest}. The panel is away for about 30 seconds; apps keep running.</li>
            <li>If {status.latest} isn’t healthy within 2 minutes, {status.current} starts again by itself.</li>
          </ol>
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="up-pw" className={fieldLabel}>
              Your password
            </label>
            <input
              id="up-pw"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-invalid={code === "wrong_password"}
              aria-describedby={start.error ? "up-err" : undefined}
              className={fieldInput}
            />
            {start.error && (
              <p id="up-err" role="alert" className="text-xs text-status-failed-fg">
                {code === "wrong_password" ? "Your password is incorrect." : start.error.message}
              </p>
            )}
          </div>
          <div className={dialogFooter}>
            <button type="button" onClick={close} disabled={start.isPending} className={cancelButton}>
              Cancel
            </button>
            <button type="submit" disabled={start.isPending || !password} className={primaryButton}>
              {start.isPending && <Loader2 className="h-3.5 w-3.5 motion-safe:animate-spin" aria-hidden="true" />}
              Upgrade to {status.latest}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const DISMISSED = "toft.upgrade.dismissed";

/** The run's card dismissed by this viewer: a per-browser convenience. */
function dismissed(run: UpgradeRun): boolean {
  try {
    return localStorage.getItem(DISMISSED) === run.started_at;
  } catch {
    return false;
  }
}

const STEP_LOOK: Record<StepState, { bar: string; fg: string; icon: React.ReactNode }> = {
  done: { bar: "bg-status-running", fg: "text-fg", icon: <Check className="h-3 w-3" aria-hidden="true" /> },
  running: { bar: "bg-status-building", fg: "text-fg", icon: <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-status-building motion-safe:animate-pulse" /> },
  failed: { bar: "bg-status-failed", fg: "text-fg", icon: <X className="h-3 w-3" aria-hidden="true" /> },
  rolled_back: { bar: "bg-status-pending", fg: "text-fg", icon: <Undo2 className="h-3 w-3" aria-hidden="true" /> },
  waiting: { bar: "bg-raised", fg: "text-muted", icon: <span aria-hidden="true" className="h-1 w-1 rounded-full bg-muted" /> },
};

const KIND_LOOK = {
  running: { border: "border-subtle", tone: "bg-status-building-soft text-status-building-fg" },
  restarting: { border: "border-subtle", tone: "bg-status-building-soft text-status-building-fg" },
  done: { border: "border-status-running/35", tone: "bg-status-running-soft text-status-running-fg" },
  rolled_back: { border: "border-status-pending/40", tone: "bg-status-pending-soft text-status-pending-fg" },
  failed: { border: "border-status-failed/35", tone: "bg-status-failed-soft text-status-failed-fg" },
};

/** The upgrade in progress, or one that ended in the last day until it is
 *  dismissed (board InstanceUpgrade, 2 to 6). unreachable: the panel isn't
 *  answering, as it restarts; opened: when this page loaded. */
export function UpgradeCard({
  status,
  unreachable,
  opened,
  onRetry,
}: {
  status: UpgradeStatus;
  unreachable: boolean;
  opened: number;
  onRetry: () => void;
}) {
  const run = status.run;
  const [hidden, setHidden] = useState(() => (run ? dismissed(run) : false));
  const [log, setLog] = useState(false);
  const ended = run?.finished_at ? new Date(run.finished_at).getTime() : null;
  if (!run || (run.outcome !== "running" && (hidden || (ended !== null && opened - ended > 86_400_000)))) return null;
  const card = upgradeCard(run, unreachable);
  const look = KIND_LOOK[card.kind];
  const states = stepStates(run);
  const finished = run.outcome !== "running";
  const dismiss = () => {
    try {
      localStorage.setItem(DISMISSED, run.started_at);
    } catch {
      /* private mode: hide for this visit only */
    }
    setHidden(true);
  };

  return (
    <section aria-labelledby="run-h" className={cn("flex flex-col rounded-[10px] border bg-surface", look.border)}>
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2.5 px-4 py-3.5">
        <span className={cn("flex h-8 w-8 flex-none items-center justify-center rounded-lg", look.tone)}>
          {card.kind === "running" || card.kind === "restarting" ? (
            <Loader2 className="h-4 w-4 motion-safe:animate-spin" aria-hidden="true" />
          ) : card.kind === "done" ? (
            <Check className="h-4 w-4" aria-hidden="true" />
          ) : card.kind === "rolled_back" ? (
            <Undo2 className="h-4 w-4" aria-hidden="true" />
          ) : (
            <X className="h-4 w-4" aria-hidden="true" />
          )}
        </span>
        <div aria-live="polite" className="flex min-w-0 flex-[1_1_420px] flex-col gap-0.5">
          <h2 id="run-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            {card.title}
          </h2>
          <p className="text-[13px] leading-[19px] text-fg/70">{card.text}</p>
        </div>
        {finished && (
          <div className="flex flex-wrap items-center gap-2">
            {/* The page still runs the old release's code. */}
            {card.kind === "done" && ended !== null && ended > opened && (
              <button type="button" onClick={() => window.location.reload()} className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}>
                Reload the page
              </button>
            )}
            {run.output.length > 0 && (
              <button type="button" onClick={() => setLog(true)} className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}>
                Full log
              </button>
            )}
            {card.kind !== "done" && status.available && status.install === "installer" && (
              <button type="button" onClick={onRetry} className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}>
                Try again…
              </button>
            )}
            <button type="button" onClick={dismiss} className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}>
              Dismiss
            </button>
          </div>
        )}
      </div>
      <ol aria-label="Steps" className="grid grid-cols-[repeat(auto-fit,minmax(110px,1fr))] gap-2.5 px-4 pb-3.5 sm:grid-cols-6">
        {UPGRADE_STEPS.map((s, i) => {
          const l = STEP_LOOK[states[i]];
          return (
            <li key={s.id} aria-current={states[i] === "running" ? "step" : undefined} className="flex flex-col gap-1.5">
              <span aria-hidden="true" className={cn("h-[3px] rounded-sm", l.bar)} />
              <span className={cn("flex items-center gap-1.5 text-[13px]", l.fg)}>
                <span className="flex h-3 w-3 items-center justify-center">{l.icon}</span>
                {s.label}
                <span className="sr-only">: {states[i].replace("_", " ")}</span>
              </span>
              {states[i] !== "waiting" && run.notes[s.id] && (
                <span className="font-mono text-[11px] leading-4 text-muted">{run.notes[s.id]}</span>
              )}
            </li>
          );
        })}
      </ol>
      {/* The last lines stay after a failed or rolled-back run: they say why (board). */}
      {card.kind !== "done" && run.output.length > 0 && (
        <pre role="group" tabIndex={0} aria-label="The last lines toft upgrade printed" className="mx-4 mb-4 max-h-[180px] overflow-auto whitespace-pre-wrap rounded-lg border border-subtle bg-ground px-3.5 py-3 font-mono text-xs leading-[19px] text-fg/85">
          {run.output.slice(-12).join("\n")}
          {unreachable && "\n\nReconnecting…"}
        </pre>
      )}
      <UpgradeLog run={run} open={log} onClose={() => setLog(false)} />
    </section>
  );
}

/** What toft upgrade printed, in full. */
export function UpgradeLog({ run, open, onClose }: { run: UpgradeRun; open: boolean; onClose: () => void }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-[min(960px,calc(100vw-32px))] gap-0 rounded-xl p-0">
        <DialogHeader className={dialogHeader}>
          <DialogTitle className={dialogTitle}>
            {run.from ? `${run.from} → ${run.to}` : `Install of ${run.to}`}
          </DialogTitle>
          <DialogDescription className={dialogText}>What toft upgrade printed on the panel’s server.</DialogDescription>
        </DialogHeader>
        <pre
          role="group"
          tabIndex={0}
          aria-label="Everything toft upgrade printed"
          className="mx-5 mb-5 mt-3 max-h-[60vh] overflow-auto whitespace-pre-wrap rounded-lg border border-subtle bg-ground px-3.5 py-3 font-mono text-xs leading-[19px] text-fg/85"
        >
          {run.output.join("\n")}
        </pre>
      </DialogContent>
    </Dialog>
  );
}

/** The header's button: "Upgrade to vX", while one is available. */
export function UpgradeButton({ status, onClick }: { status: UpgradeStatus; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={primaryButton}>
      <ArrowUp className="h-[15px] w-[15px]" aria-hidden="true" />
      Upgrade to {status.latest}
    </button>
  );
}
