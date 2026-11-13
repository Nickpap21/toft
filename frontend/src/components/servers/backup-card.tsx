"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { backupPath, backupService, type BackupDestination, type BackupInput } from "@/lib/api/server.service";
import { formatBytes } from "@/lib/api/sharedvolume.service";
import { sinceShort } from "@/lib/time";
import { cn, formatDateTime, formatDateTimeZone } from "@/lib/utils";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  primaryButton,
} from "@/components/settings/settings-frame";
import { btnSm } from "@/app/projects/_lib/ui";
import { Row, settingsFoot, settingsHead, titleRow } from "@/app/servers/[id]/aside";
import { backupState, hostOf } from "@/app/settings/backups/backups-view";

const EMPTY: BackupInput = { endpoint: "", bucket: "", access_key: "", secret_key: "", schedule: "03:00" };

const message = (err: unknown, fallback: string) => (err instanceof ApiError && err.status < 500 ? err.message : fallback);

const DOT: Record<string, string> = {
  ok: "text-status-running-fg",
  fail: "text-status-failed-fg",
  wait: "text-status-pending-fg",
  off: "text-muted",
};

/** A server's backup destination (D-43; board: Server, Backups). Owners set
 *  it; every member sees where backups go and how the last one went, never
 *  the secret key. */
export function BackupCard({ serverId, serverName, canManage }: { serverId: string; serverName: string; canManage: boolean }) {
  const queryClient = useQueryClient();
  const path = backupPath(serverId);
  const queryKey = ["backup", path];
  const [form, setForm] = useState<BackupInput | null>(null);
  const [password, setPassword] = useState<string | null>(null);
  const [stopping, setStopping] = useState(false);

  const { data, error, isLoading, dataUpdatedAt, errorUpdatedAt } = useQuery({
    queryKey,
    queryFn: () => backupService.get(path),
    retry: false,
  });
  const dest = data ?? null;
  const now = Math.max(dataUpdatedAt, errorUpdatedAt);

  // Links to #backup (the Servers page, Home) arrive before the page has
  // rendered the card, so the browser's own jump misses it.
  useEffect(() => {
    if (!isLoading && window.location.hash === "#backup") {
      document.getElementById("backup")?.scrollIntoView({ block: "start" });
    }
  }, [isLoading]);

  const save = useMutation({
    mutationFn: (input: BackupInput) => backupService.put(path, input),
    onSuccess: (saved) => {
      const { repository_password, ...rest } = saved;
      queryClient.setQueryData(queryKey, rest);
      queryClient.invalidateQueries({ queryKey: ["backups"] });
      queryClient.invalidateQueries({ queryKey: ["inbox"] });
      setForm(null);
      if (repository_password) setPassword(repository_password);
      if (saved.apply_error) {
        toast.warning(`Saved, but not written to ${serverName} yet: ${saved.apply_error}. It gets the settings at its next provisioning run.`, { duration: 15_000 });
      } else if (!repository_password) {
        toast.success("Backup destination saved");
      }
    },
  });
  const set = (patch: Partial<BackupInput>) => setForm((f) => (f ? { ...f, ...patch } : f));
  const state = backupState(dest, now);
  const waiting = !!dest && !dest.last_run_at;

  return (
    <section id="backup" tabIndex={-1} aria-labelledby="bk-h" className={cn(card, "overflow-hidden")}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="bk-h" className="text-[15px] font-semibold leading-[22px] text-fg">Backups</h2>
          {dest && !form && (
            <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", DOT[waiting ? "wait" : state.tone])}>
              <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
              {waiting ? "Waiting for the first backup" : state.label}
              {dest.last_run_at && (
                <span title={formatDateTimeZone(dest.last_run_at)}>{sinceShort(dest.last_run_at, now)}</span>
              )}
            </span>
          )}
        </div>
        {dest && !form && canManage && (
          <button
            type="button"
            aria-label={`Edit the backup destination of ${serverName}`}
            className={btnSm}
            onClick={() => {
              save.reset();
              setForm({ endpoint: dest.endpoint, bucket: dest.bucket, access_key: dest.access_key, secret_key: "", schedule: dest.schedule });
            }}
          >
            Edit
          </button>
        )}
      </div>

      {isLoading ? (
        <p className="px-4 py-3.5 text-[13px] text-muted">Loading…</p>
      ) : error && data !== null ? (
        <p role="alert" className="px-4 py-3.5 text-[13px] text-status-failed-fg">The backup destination could not be loaded.</p>
      ) : form ? (
        <form
          className="flex flex-col gap-3.5 px-4 pb-4 pt-3.5"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate(form);
          }}
        >
          <div className="grid grid-cols-1 gap-x-4 gap-y-3.5 sm:grid-cols-2">
            <Field id="bk-endpoint" label="Endpoint" help="https:// and the host only, no path.">
              <input id="bk-endpoint" type="url" required value={form.endpoint} onChange={(e) => set({ endpoint: e.target.value })} placeholder="https://fsn1.your-objectstorage.com" aria-describedby="bk-endpoint-help" className={cn(fieldInput, "font-mono")} />
            </Field>
            <Field id="bk-bucket" label="Bucket" help="Backups go under toft/ in it, one folder per server.">
              <input id="bk-bucket" required value={form.bucket} onChange={(e) => set({ bucket: e.target.value.toLowerCase() })} placeholder="founders-backups" aria-describedby="bk-bucket-help" className={cn(fieldInput, "font-mono")} />
            </Field>
            <Field id="bk-access" label="Access key">
              <input id="bk-access" required autoComplete="off" value={form.access_key} onChange={(e) => set({ access_key: e.target.value })} className={cn(fieldInput, "font-mono")} />
            </Field>
            <Field id="bk-secret" label="Secret key" help={dest ? "Leave empty to keep the stored one." : "Never shown again."}>
              <input id="bk-secret" type="password" autoComplete="new-password" required={!dest} placeholder={dest ? "Stored, not shown" : ""} value={form.secret_key} onChange={(e) => set({ secret_key: e.target.value })} aria-describedby="bk-secret-help" className={cn(fieldInput, "font-mono")} />
            </Field>
            <Field id="bk-at" label="Daily at" help="Server time. The run starts within 15 minutes of it.">
              <input id="bk-at" type="time" required value={form.schedule} onChange={(e) => set({ schedule: e.target.value })} aria-describedby="bk-at-help" className={cn(fieldInput, "w-32 font-mono")} />
            </Field>
          </div>
          <p className="text-xs leading-[17px] text-muted">
            {dest
              ? `Saving writes the new settings to ${serverName} over SSH; if it can’t be reached, it gets them at its next provisioning run. The repository password stays the same.`
              : `Saving writes the settings to ${serverName} over SSH; if it can’t be reached, it gets them at its next provisioning run. Then the panel shows the repository password, once.`}
          </p>
          {save.error && (
            <p role="alert" className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] leading-[19px] text-status-failed-fg">
              {message(save.error, "The backup destination was not saved.")}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <button type="submit" className={cn(primaryButton, "h-8")} disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</button>
            <button type="button" className={cn(cancelButton, "h-8")} onClick={() => setForm(null)}>Cancel</button>
          </div>
        </form>
      ) : !dest ? (
        <div className="flex flex-wrap items-start justify-between gap-3 px-4 py-3.5">
          <div className="flex flex-[1_1_360px] flex-col gap-1.5 text-[13px] leading-[19px]">
            <span className="inline-flex items-center gap-1.5 font-medium text-muted">
              <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
              Not set up
            </span>
            <p className="text-fg/85">Nothing on {serverName} is backed up: not its volumes, its mounted folders or its databases.</p>
            <p className="text-fg/70">Backups run every night on the server, encrypted, to an S3-compatible bucket, so they keep working if the panel is down.</p>
            <p className="text-fg/70">First create a bucket and an S3 access key at your provider, such as Hetzner Object Storage in the same region. Toft can’t create them.</p>
          </div>
          {canManage && (
            <button type="button" className={primaryButton} onClick={() => setForm(EMPTY)}>Set up backups</button>
          )}
        </div>
      ) : (
        <>
          <dl>
            <Row k="Last backup" first>
              {waiting ? (
                <span>
                  The first starts at <span className="font-mono text-xs">{dest.schedule}</span> server time, or within 15 minutes if that has passed today. It
                  uploads everything, so it takes longer than the ones after it.
                </span>
              ) : state.failing ? (
                <>
                  <span className="text-status-failed-fg">
                    The last run backed up nothing.{" "}
                    {dest.last_ok_at ? <>Last success <span className="font-mono text-xs">{formatDateTimeZone(dest.last_ok_at)}</span></> : "No backup has succeeded yet."}
                  </span>
                  <span className="mt-1.5 flex flex-col gap-2 rounded-lg border border-danger/40 bg-ground px-3.5 py-2.5">
                    <span className="break-words font-mono text-xs leading-5 text-fg">
                      <span className="sr-only">Error from the last run: </span>
                      {dest.last_error || `see /var/lib/toft/backup/last.json on ${serverName}`}
                    </span>
                    <span className="text-xs text-fg/70">Nothing was backed up. The Backup failing alert resolves at the next success.</span>
                  </span>
                </>
              ) : (
                <span>{dest.last_bytes != null ? `${formatBytes(dest.last_bytes)}, no errors` : "No errors"}</span>
              )}
            </Row>
            <Row k="Next run">
              <span>
                <span className="font-mono text-[13px]">{dest.schedule}</span> server time, daily
              </span>
            </Row>
            <Row k="Destination">
              <span className="min-w-0" title={dest.repository.replace(/^s3:https?:\/\//, "")}>
                <span className="font-mono text-[13px] [overflow-wrap:anywhere]">{dest.bucket}</span>{" "}
                <span className="text-muted [overflow-wrap:anywhere]">· {hostOf(dest.endpoint)}</span>
              </span>
            </Row>
            <Row k="Keeps">
              <span>7 daily · 4 weekly · 6 monthly</span>
              <span className="text-xs leading-[18px] text-muted">
                Encrypted with the repository password, shown once on <span className="font-mono">{formatDateTime(dest.created_at).slice(0, 10)}</span>
              </span>
            </Row>
          </dl>
          <div className={settingsFoot}>
            <span className="flex flex-wrap gap-x-4 gap-y-1">
              <Link href="/settings/backups" className="touch-target inline-flex items-center text-link underline-offset-4 hover:underline">What is and isn’t backed up</Link>
              <Link href="/settings/backups#restore" className="touch-target inline-flex items-center text-link underline-offset-4 hover:underline">Run one now or restore</Link>
            </span>
            {canManage && (
              <button type="button" className={btnSm} onClick={() => setStopping(true)}>
                Stop backups…
              </button>
            )}
          </div>
        </>
      )}

      <PasswordDialog serverName={serverName} password={password} onClose={() => setPassword(null)} />
      <StopDialog serverName={serverName} path={path} dest={dest} open={stopping} onClose={() => setStopping(false)} />
    </section>
  );
}

function Field({ id, label, help, children }: { id: string; label: string; help?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className={fieldLabel}>{label}</label>
      {children}
      {help && <span id={`${id}-help`} className="text-xs leading-[17px] text-muted">{help}</span>}
    </div>
  );
}

function PasswordDialog({ serverName, password, onClose }: { serverName: string; password: string | null; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <Dialog open={password !== null} onOpenChange={(o) => !o && (onClose(), setCopied(false))}>
      <DialogContent className={cn(dialogContent, "max-w-[560px]")} showClose={false} onInteractOutside={(e) => e.preventDefault()}>
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Store the repository password of <span className="font-mono font-medium">{serverName}</span>
          </DialogTitle>
          <DialogDescription className={dialogText}>
            It decrypts this server’s backups, and this is the only time it is shown. The panel keeps it encrypted and the server keeps it in a
            root-only file, but if both are lost, the copy in your password manager is the only way to read the backups.
          </DialogDescription>
        </DialogHeader>
        <div className="mx-5 mt-4 flex flex-col gap-1.5">
          <label htmlFor="bp-value" className={fieldLabel}>Repository password</label>
          <div className="flex items-start gap-2">
            <textarea
              id="bp-value"
              readOnly
              rows={2}
              value={password ?? ""}
              onFocus={(e) => e.target.select()}
              className="min-w-0 flex-1 resize-none rounded-lg border border-strong bg-ground px-2.5 py-2 font-mono text-xs leading-5 text-fg [overflow-wrap:anywhere]"
            />
            <button
              type="button"
              className={cancelButton}
              onClick={() =>
                password &&
                navigator.clipboard.writeText(password).then(
                  () => setCopied(true),
                  () => toast.error("Couldn’t copy: select it and copy it"),
                )
              }
            >
              {copied ? <Check className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />}
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
        </div>
        <div className="flex justify-end gap-2 px-5 pb-5 pt-[18px]">
          <button type="button" className={primaryButton} onClick={() => (onClose(), setCopied(false))}>Done, I stored it</button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function StopDialog({
  serverName,
  path,
  dest,
  open,
  onClose,
}: {
  serverName: string;
  path: string;
  dest: BackupDestination | null;
  open: boolean;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const stop = useMutation({
    mutationFn: () => backupService.delete(path),
    onSuccess: () => {
      queryClient.setQueryData(["backup", path], null);
      queryClient.invalidateQueries({ queryKey: ["backups"] });
      queryClient.invalidateQueries({ queryKey: ["inbox"] });
      toast.success("Backups stopped. The copies already in the bucket stay.");
      onClose();
      stop.reset();
    },
  });
  return (
    <Dialog open={open} onOpenChange={(o) => !o && !stop.isPending && (onClose(), stop.reset())}>
      <DialogContent role="alertdialog" className={dialogContent} showClose={false}>
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Stop backing up <span className="font-mono font-medium">{serverName}</span>?
          </DialogTitle>
          <DialogDescription className={dialogText}>
            From tonight, nothing backs up its volumes, its mounted folders or its databases, nor the services of workspaces it is shared with, and
            no alert says so.
          </DialogDescription>
        </DialogHeader>
        <ul className="mx-5 mt-3 flex list-disc flex-col gap-1 pl-[18px] text-[13px] leading-[19px] text-fg/70">
          <li>
            The copies already in <span className="font-mono text-xs text-fg">{dest?.bucket}</span> stay. Delete them at your provider if you want them gone.
          </li>
          <li>Setting it up again makes a new repository password, which can’t open those copies. Use another bucket then, or delete them first.</li>
          <li>If {serverName} can’t be reached now, it keeps backing up until its next provisioning run.</li>
        </ul>
        {stop.error && (
          <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
            {message(stop.error, "Backups were not stopped.")}
          </p>
        )}
        <div className={dialogFooter}>
          <button type="button" className={cancelButton} disabled={stop.isPending} onClick={onClose}>Cancel</button>
          <button type="button" className={dangerButton} disabled={stop.isPending} onClick={() => stop.mutate()}>
            {stop.isPending ? "Stopping…" : "Stop backups"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
