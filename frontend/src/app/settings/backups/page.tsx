"use client";

import { gitInfoQuery } from "@/lib/api/code.service";
import { useState } from "react";
import Link from "next/link";
import { useQueries, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Copy, Minus } from "lucide-react";
import { useIsOperator } from "@/context/AuthContext";
import { alertService } from "@/lib/api/alert.service";
import {
  backupPath,
  backupService,
  serverService,
  type BackupDestination,
  type Server,
} from "@/lib/api/server.service";
import { serversQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import {
  SectionHead,
  SettingsFrame,
  card,
  phoneLabel,
  phoneNote,
  phoneRow,
  phoneTd,
  smallButton,
  th,
} from "@/components/settings/settings-frame";
import {
  backupState,
  failureNote,
  hostOf,
  summary,
  type BackupState,
  type BackupTone,
} from "./backups-view";
import { PANEL_KEY, PanelHost } from "./panel-host";

const TONE: Record<BackupTone, string> = {
  ok: "text-status-running-fg",
  fail: "text-status-failed-fg",
  wait: "text-status-pending-fg",
  off: "text-fg/70",
};

interface Row {
  key: string;
  name: string;
  role: string;
  href?: string;
  /** Null: not set up; undefined: not ours to see, or not read. */
  dest: BackupDestination | null | undefined;
  state: BackupState | null;
  /** Its read failed: what wasn't read, and how to read it again. */
  unread?: { what: string; onRetry: () => void };
  action?: { label: string; href?: string; onClick?: () => void; aria: string };
  fix?: { label: string; href?: string; onClick?: () => void };
  failingSince: string | null;
  guestOf?: string;
}

/** Settings → Backups (board: Backup): every server's nightly backup, the
 *  panel host's for operators, what a backup holds and how to restore. */
export default function BackupsPage() {
  const isOperator = useIsOperator();
  const [now] = useState(() => Date.now());
  const [editingPanel, setEditingPanel] = useState(false);
  const servers = useQuery(serversQuery);
  const owned = (servers.data ?? []).filter((s) => !s.shared);
  const guests = (servers.data ?? []).filter((s) => s.shared);
  const dests = useQueries({
    queries: owned.map((s) => ({
      queryKey: ["backups", s.id],
      queryFn: () => backupService.get(backupPath(s.id)),
      retry: false,
    })),
  });
  const shares = useQueries({
    queries: owned.map((s) => ({
      queryKey: ["server-shares", s.id],
      queryFn: () => serverService.listShares(s.id),
      meta: { quietFailure: "Only the row's “With …’s share” label; nothing here says a server isn't shared." },
    })),
  });
  const panel = useQuery({
    queryKey: PANEL_KEY,
    queryFn: () => backupService.get(backupPath(null)),
    enabled: isOperator,
    retry: false,
  });
  const alerts = useQuery({
    queryKey: ["alerts-recent"],
    queryFn: () => alertService.recent(),
  });
  const failingSince = (subject: string) =>
    alerts.data?.find(
      (a) =>
        a.kind === "backup_failed" && a.subject === subject && !a.resolved_at,
    )?.fired_at ?? null;

  const editPanel = () => {
    setEditingPanel(true);
    document
      .getElementById("panel-host")
      ?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  const rows: Row[] = [];
  if (isOperator) {
    const d = panel.data;
    const failed = readFailed(panel);
    rows.push({
      key: "panel",
      name: "panel",
      role: "Panel host",
      href: "#panel-host",
      dest: panel.isLoading || failed ? undefined : (d ?? null),
      state: panel.isLoading || failed ? null : backupState(d ?? null, now),
      unread: failed ? { what: "the panel host’s backup", onRetry: () => void panel.refetch() } : undefined,
      action: failed ? undefined : {
        label: d ? "Edit" : "Set up",
        onClick: editPanel,
        aria: d
          ? "Edit the panel host’s backup"
          : "Set up the panel host’s backup",
      },
      fix: { label: "Edit the panel host’s backup", onClick: editPanel },
      failingSince: failingSince("control-plane"),
    });
  } else {
    rows.push({
      key: "panel",
      name: "panel",
      role: "Panel host",
      dest: undefined,
      state: null,
      failingSince: null,
    });
  }
  owned.forEach((s: Server, i) => {
    const d = dests[i]?.data;
    const failed = !!dests[i] && readFailed(dests[i]);
    const sharedWith = (shares[i]?.data ?? []).map((x) => x.org_name);
    rows.push({
      key: s.id,
      name: s.name,
      role: sharedWith.length ? `With ${sharedWith.join(", ")}’s share` : "",
      href: `/servers/${s.id}`,
      dest: dests[i]?.isLoading || failed ? undefined : (d ?? null),
      state: dests[i]?.isLoading || failed ? null : backupState(d ?? null, now),
      unread: failed ? { what: "this server’s backup", onRetry: () => void dests[i].refetch() } : undefined,
      action: {
        label: "Backup settings",
        href: `/servers/${s.id}#backup`,
        aria: `${s.name}’s backup settings`,
      },
      fix: {
        label: `Fix it on ${s.name}’s page`,
        href: `/servers/${s.id}#backup`,
      },
      failingSince: failingSince(s.id),
    });
  });
  guests.forEach((s) => {
    rows.push({
      key: s.id,
      name: s.name,
      role: `Shared by ${s.shared_by}`,
      dest: undefined,
      state: null,
      failingSince: null,
      guestOf: s.shared_by,
    });
  });
  const known = rows
    .map((r) => r.state)
    .filter((x): x is BackupState => x !== null);

  return (
    <SettingsFrame title="Backups" gap="gap-6">
      <p className="max-w-[900px] text-[13px] leading-[19px] text-fg/70">
        Each server backs itself up once a night with restic, encrypted, to an
        S3-compatible bucket. The backup runs on the server, so it keeps working
        when the panel is down, and your data never passes through the panel. A
        server’s owners set its destination on{" "}
        <Link href="/servers" className="inline-link">
          its page under Servers
        </Link>
        ; an instance operator sets the panel host’s
        {isOperator ? " here" : ""}.
      </p>

      {servers.error ? (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-danger/35 bg-surface px-4 py-3.5"
        >
          <span className="flex items-center gap-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
            Could not load the backups:{" "}
            {servers.error instanceof Error
              ? servers.error.message
              : "the panel did not answer"}
            . The servers keep backing up on their own schedule.
          </span>
          <button
            type="button"
            className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
            onClick={() => servers.refetch()}
          >
            Retry
          </button>
        </div>
      ) : (
        <>
          <div className="flex flex-wrap items-start gap-6">
            <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-6">
              <section aria-labelledby="srv-h" className="flex flex-col gap-3">
                <SectionHead
                  id="srv-h"
                  title="Servers"
                  count={servers.isLoading ? "–" : rows.length}
                  note={known.length ? summary(known) : undefined}
                />
                <div className={cn(card, "overflow-x-auto")}>
                  <table className="w-full border-collapse text-[13px] max-sm:block sm:min-w-[700px]">
                    <thead className="max-sm:hidden">
                      <tr>
                        <th scope="col" className={th}>
                          Server
                        </th>
                        <th scope="col" className={th}>
                          Backs up to
                        </th>
                        <th scope="col" className={cn(th, "whitespace-nowrap")}>
                          Daily, server time
                        </th>
                        <th scope="col" className={th}>
                          Last backup
                        </th>
                        <th scope="col" className={th}>
                          <span className="sr-only">Actions</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody className="max-sm:block">
                      {servers.isLoading ? (
                        <tr className="max-sm:block">
                          <td colSpan={5} className={cn(phoneNote, "py-3.5")}>
                            <span
                              className="block h-2.5 w-[60%] rounded bg-raised motion-safe:animate-pulse"
                              aria-label="Loading"
                            />
                          </td>
                        </tr>
                      ) : (
                        rows.map((r) => (
                          <ServerRows
                            key={r.key}
                            r={r}
                            isOperator={isOperator}
                          />
                        ))
                      )}
                    </tbody>
                  </table>
                </div>
                <p className="text-xs leading-[17px] text-muted">
                  The panel reads each server’s last run every 5 minutes and
                  keeps only that one. The run’s full output stays on the
                  server, in{" "}
                  <span className="font-mono text-fg/70">
                    /var/lib/toft/backup/
                  </span>{" "}
                  (<span className="font-mono text-fg/70">last.json</span>,{" "}
                  <span className="font-mono text-fg/70">restic.err</span>). A
                  failed run, or none that succeeded in 26 hours, raises{" "}
                  <Link href="/settings/alerts" className="inline-link">
                    Backup failing
                  </Link>
                  , which resolves at the next success.
                </p>
              </section>

              {isOperator ? (
                panel.isLoading ? (
                  <div
                    className={cn(card, "h-64 motion-safe:animate-pulse")}
                    aria-label="Loading the panel host’s backup"
                  />
                ) : panel.error ? (
                  <p
                    role="alert"
                    className={cn(
                      card,
                      "p-4 text-[13px] text-status-failed-fg",
                    )}
                  >
                    Could not load the panel host’s backup:{" "}
                    {panel.error instanceof Error
                      ? panel.error.message
                      : "unknown error"}
                  </p>
                ) : (
                  <PanelHost
                    dest={panel.data ?? null}
                    now={now}
                    editing={editingPanel}
                    onEdit={setEditingPanel}
                  />
                )
              ) : (
                <div
                  className={cn(
                    card,
                    "px-4 py-3.5 text-[13px] leading-[19px] text-fg/70",
                  )}
                >
                  The panel host,{" "}
                  <span className="font-mono text-xs text-fg">panel</span>, is
                  backed up by an instance operator. Its destination and last
                  run are shown only to operators.
                </div>
              )}
            </div>
            <Holds />
          </div>
          <HowItRuns />
          <Restore />
        </>
      )}
    </SettingsFrame>
  );
}

function ServerRows({ r, isOperator }: { r: Row; isOperator: boolean }) {
  const name = "whitespace-nowrap font-mono font-medium text-fg max-sm:whitespace-normal";
  const failing = r.state?.failing && r.dest;
  return (
    <>
      <tr className={phoneRow}>
        <td className={cn(phoneTd, "align-top max-sm:col-start-1 max-sm:row-start-1")}>
          <span className="flex flex-col">
            {r.href ? (
              r.href.startsWith("#") ? (
                <a href={r.href} className={cn(name, "hover:underline")}>
                  {r.name}
                </a>
              ) : (
                <Link href={r.href} className={cn(name, "hover:underline")}>
                  {r.name}
                </Link>
              )
            ) : (
              <span className={name}>{r.name}</span>
            )}
            {r.role && (
              <span className="whitespace-nowrap text-xs leading-5 text-muted">
                {r.role}
              </span>
            )}
          </span>
        </td>
        <td className={cn(phoneTd, "align-top max-sm:col-span-2")}>
          {r.unread ? (
            <LoadFailed what={r.unread.what} onRetry={r.unread.onRetry} />
          ) : r.guestOf ? (
            <span className="text-fg/70">Set by {r.guestOf}’s owners</span>
          ) : r.dest === undefined && r.key === "panel" && !isOperator ? (
            <span className="text-fg/70">Set by an instance operator</span>
          ) : r.dest ? (
            <span className="flex flex-col">
              <span className="whitespace-nowrap font-mono text-xs leading-5 text-fg max-sm:whitespace-normal">
                <span className={cn(phoneLabel, "font-sans")}>Backs up to </span>
                {r.dest.bucket}
              </span>
              <span className="whitespace-nowrap text-xs leading-5 text-muted">
                {hostOf(r.dest.endpoint)}
              </span>
            </span>
          ) : r.dest === null ? (
            <span className="text-muted">No destination</span>
          ) : null}
        </td>
        <td
          className={cn(
            phoneTd,
            "align-top font-mono text-xs leading-5 max-sm:col-span-2",
            r.dest ? "text-fg" : "text-muted",
          )}
        >
          <span className={cn(phoneLabel, "font-sans")}>Daily, server time </span>
          {r.dest?.schedule ?? "–"}
        </td>
        <td className={cn(phoneTd, "align-top max-sm:col-span-2")}>
          {r.guestOf ? (
            <span className="flex flex-col">
              <span className="font-medium text-fg/70">Their backup</span>
              <span className="text-xs leading-5 text-muted">
                holds your share too; only they see it
              </span>
            </span>
          ) : r.key === "panel" && !isOperator ? (
            <span className="flex flex-col">
              <span className="font-medium text-muted">Operators only</span>
            </span>
          ) : r.state ? (
            <span className="flex flex-col">
              <span
                className={cn(
                  "inline-flex items-center gap-1.5 font-medium",
                  TONE[r.state.tone],
                )}
              >
                <span className={cn(phoneLabel, "font-normal")}>Last backup </span>
                <span
                  className="h-1.5 w-1.5 rounded-full bg-current"
                  aria-hidden="true"
                />
                {r.state.label}
              </span>
              {r.state.note && (
                <span className="text-xs leading-5 text-muted">
                  {r.state.note}
                </span>
              )}
            </span>
          ) : null}
        </td>
        <td className={cn(phoneTd, "whitespace-nowrap text-right align-top max-sm:col-start-2 max-sm:row-start-1")}>
          {r.action &&
            (r.action.href ? (
              <Link
                href={r.action.href}
                aria-label={r.action.aria}
                className={smallButton}
              >
                {r.action.label}
              </Link>
            ) : (
              <button
                type="button"
                onClick={r.action.onClick}
                aria-label={r.action.aria}
                className={smallButton}
              >
                {r.action.label}
              </button>
            ))}
        </td>
      </tr>
      {failing && r.dest && (
        <tr className="max-sm:block">
          <td colSpan={5} className="px-4 pb-3.5 max-sm:block">
            <div className="flex flex-col gap-2 rounded-lg border border-danger/40 bg-ground px-3 py-2.5">
              <span className="flex items-start gap-2 break-words font-mono text-xs leading-[18px] text-fg">
                <AlertTriangle
                  className="mt-0.5 h-3.5 w-3.5 flex-none text-status-failed"
                  aria-hidden="true"
                />
                <span>
                  <span className="sr-only">Error from the last run: </span>
                  {r.dest.last_error ||
                    "The run stopped without an error message; see last.json on the server."}
                </span>
              </span>
              <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-fg/70">
                <span>{failureNote(r.dest, r.failingSince)}</span>
                {r.fix &&
                  (r.fix.href ? (
                    <Link href={r.fix.href} className="inline-link">
                      {r.fix.label}
                    </Link>
                  ) : (
                    <button
                      type="button"
                      onClick={r.fix.onClick}
                      className="inline-link"
                    >
                      {r.fix.label}
                    </button>
                  ))}
              </span>
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

const COVERED = [
  {
    name: "Volumes",
    text: "Every Docker volume Toft made (paas-…): services’ volumes and shared volumes.",
  },
  {
    name: "Mounted folders and files",
    text: "Whatever a service bind-mounts from the server, such as its config files.",
  },
  {
    name: "Databases, as dumps",
    text: "Each running MySQL, MariaDB, Postgres and MongoDB, dumped with its own tool just before the copy. Its volume is copied too.",
  },
  {
    name: "The panel’s database",
    text: "On the panel host: accounts, workspaces, members, settings and encrypted secrets.",
  },
  {
    name: "Git repositories, as bundles",
    text: "On the panel host: every repository with its branches, tags and pull requests, as a checked git bundle written right after the panel’s database, so the two always match.",
  },
  {
    name: "Everything on a shared server",
    text: "A server’s backup holds other workspaces’ services in their share too.",
  },
];
const EXCLUDED = [
  {
    name: "Other databases, as dumps",
    text: "Redis, SQLite and search indexes are copied as files while they run, and such a copy may not restore cleanly.",
  },
  {
    name: "Stopped databases",
    text: "A stopped database container is not dumped. Its volume is still copied.",
  },
  {
    name: "Images and container files",
    text: "Anything a container writes outside its volumes. Images come back with the next deploy.",
  },
  {
    name: "Volumes made outside Toft",
    text: "Docker volumes whose names don’t start with paas-.",
  },
  {
    name: "Paths with “:” or “,”",
    text: "A mounted folder with one in its path is skipped, and the run fails saying which.",
  },
  {
    name: "The panel’s .env",
    text: "ENCRYPTION_KEY and the panel’s other settings. The recovery kit holds the key: keep it in your password manager.",
  },
];

function Holds() {
  // Without git hosting (TOFT_GIT=off) there are no repositories to name.
  const git = useQuery(gitInfoQuery);
  const covered = git.data?.enabled === false ? COVERED.filter((k) => !k.name.startsWith("Git")) : COVERED;
  const list = (
    id: string,
    title: string,
    items: typeof COVERED,
    yes: boolean,
  ) => (
    <section aria-labelledby={id} className={card}>
      <div className="border-b border-subtle px-4 py-3">
        <h2
          id={id}
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          {title}
        </h2>
      </div>
      <dl className="flex flex-col py-1">
        {items.map((k, i) => (
          <div
            key={k.name}
            className={cn(
              "flex flex-col gap-0.5 px-4 py-[9px]",
              i > 0 && "border-t border-subtle",
            )}
          >
            <dt className="flex items-center gap-2 text-[13px] font-medium text-fg">
              {yes ? (
                <Check
                  className="h-3.5 w-3.5 flex-none text-status-running"
                  aria-hidden="true"
                />
              ) : (
                <Minus
                  className="h-3.5 w-3.5 flex-none text-fg/70"
                  aria-hidden="true"
                />
              )}
              {k.name}
            </dt>
            <dd className="ml-[22px] text-xs leading-[17px] text-fg/70">
              {k.text}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
  return (
    <aside
      aria-label="What a backup holds"
      className="flex min-w-0 flex-[1_1_340px] flex-col gap-6"
    >
      {list("in-h", "What is backed up", covered, true)}
      {list("out-h", "Not backed up", EXCLUDED, false)}
    </aside>
  );
}

const HOW = [
  {
    k: "When",
    v: "Once a day from the set time, 03:00 server time unless changed. A timer checks every 15 minutes, so a run missed while the server was off starts when it is back, the same day.",
  },
  {
    k: "Where",
    v: "toft/<server ID> in the bucket. The panel host’s is toft/control-plane.",
  },
  {
    k: "Encryption",
    v: "restic encrypts everything on the server before it is sent, with the repository password. Without that password the copies can’t be read, by you or the provider.",
  },
  {
    k: "Keeps",
    v: "7 daily, 4 weekly and 6 monthly copies, pruned after each run. Not changeable.",
  },
  {
    k: "Load",
    v: "restic runs in a container limited to 512 MiB of memory. The first run uploads everything; later runs only what changed.",
  },
  {
    k: "Recovery point",
    v: "Up to a day: what changed since the last run can be lost. A repository with a push mirror also has a copy within a minute of each push.",
  },
];

function HowItRuns() {
  return (
    <section aria-labelledby="how-h" className={card}>
      <div className="border-b border-subtle px-4 py-3">
        <h2
          id="how-h"
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          How it runs
        </h2>
      </div>
      <dl className="grid grid-cols-[repeat(auto-fit,minmax(160px,1fr))] py-1">
        {HOW.map((h, i) => (
          <div
            key={h.k}
            className={cn(
              "flex flex-col gap-1 px-4 pb-3 pt-2.5",
              i > 0 && "border-l border-subtle",
            )}
          >
            <dt className="text-[13px] font-medium text-fg">{h.k}</dt>
            <dd className="text-xs leading-[18px] text-fg/70">{h.v}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

const R =
  'R="docker run --rm --env-file /var/lib/toft/backup/backup.env restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510"';
const STEPS = [
  {
    title: "Run a backup now, outside the schedule",
    cmd: "/usr/local/lib/toft/backup.sh now",
    note: "Writes last.json when done; the panel shows the result within 5 minutes.",
  },
  {
    title: "Use the server’s own settings for the commands below",
    cmd: R,
    note: "The env file is root-only. restic is pinned to the version the backups use.",
  },
  {
    title: "See the copies",
    cmd: "$R snapshots",
    note: "One per night, tagged toft, newest last.",
  },
  {
    title: "See what one holds",
    cmd: "$R ls latest | head",
    note: "Dumps are under /data/dumps, volumes under /data/volumes/<volume>, mounted folders under /data/binds/<path>.",
  },
  {
    title: "Put a volume’s files back (here one file)",
    cmd: "docker run --rm --env-file /var/lib/toft/backup/backup.env -v <volume>:/data/volumes/<volume> restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510 restore latest --target / --include /data/volumes/<volume>/<path>",
    note: "Leave out <path> to restore the whole volume. Stop the service first if it writes there.",
  },
  {
    title: "Load a database from its dump (Postgres)",
    cmd: "$R dump latest /data/dumps/<container>.dump | docker exec -i <container> psql -U postgres",
    note: "MySQL and MariaDB: … | docker exec -i <container> mysql -uroot -p",
  },
  {
    title: "Get a repository back (panel host)",
    cmd: "$R dump latest /data/volumes/paas-git/@bundles/<repository ID>.bundle > repo.bundle && git clone --mirror repo.bundle",
    note: "The ID is in the repository’s URL in the panel. A restore onto a new server brings every repository back by itself.",
  },
];

function Restore() {
  const [copied, setCopied] = useState<number | null>(null);
  return (
    <section id="restore" aria-labelledby="rs-h" className={card}>
      <div className="flex flex-col gap-0.5 border-b border-subtle px-4 py-3">
        <h2
          id="rs-h"
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          Restoring, and running one now
        </h2>
        <span className="text-xs leading-[18px] text-muted">
          On the server, as root, with the settings the panel wrote there. The
          panel can’t restore yet. These commands were drilled on{" "}
          <span className="font-mono">2026-10-13</span> against a test volume
          and a test database.
        </span>
      </div>
      <ol className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,480px),1fr))] gap-x-6 gap-y-3.5 p-4">
        {STEPS.map((st, i) => (
          <li key={st.title} className="flex min-w-0 flex-col gap-1.5">
            <span className="flex items-baseline gap-2 text-[13px] text-fg">
              <span aria-hidden="true" className="font-mono text-xs text-muted">
                {i + 1}
              </span>
              {st.title}
            </span>
            <div className="flex items-start gap-2 rounded-md border border-subtle bg-ground py-1.5 pl-3 pr-1.5">
              <code className="min-w-0 flex-1 whitespace-pre-wrap py-1 font-mono text-xs leading-[18px] text-fg/90 [overflow-wrap:anywhere]">
                {st.cmd}
              </code>
              <button
                type="button"
                aria-label={`Copy: ${st.title.toLowerCase()}`}
                className={cn(smallButton, "h-7 flex-none rounded-md")}
                onClick={() =>
                  navigator.clipboard.writeText(st.cmd).then(
                    () => setCopied(i),
                    () => toast.error("Couldn't copy: select it and copy it"),
                  )
                }
              >
                {copied === i ? (
                  <Check className="h-3.5 w-3.5" aria-hidden="true" />
                ) : (
                  <Copy className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {copied === i ? "Copied" : "Copy"}
              </button>
            </div>
            <span className="text-xs leading-[17px] text-muted">{st.note}</span>
          </li>
        ))}
      </ol>
      <p className="border-t border-subtle px-4 pb-3.5 pt-3 text-xs leading-[18px] text-muted">
        From another machine, when the server is gone: put{" "}
        <span className="font-mono text-fg/70">RESTIC_REPOSITORY</span>,{" "}
        <span className="font-mono text-fg/70">RESTIC_PASSWORD</span>,{" "}
        <span className="font-mono text-fg/70">AWS_ACCESS_KEY_ID</span> and{" "}
        <span className="font-mono text-fg/70">AWS_SECRET_ACCESS_KEY</span> in
        an env file, with the password from your password manager and the keys
        from your provider, and run the same commands with it. The repository is
        the server’s destination above, for example{" "}
        <span className="font-mono text-fg/70">
          s3:https://fsn1.your-objectstorage.com/your-bucket/toft/control-plane
        </span>
        .
      </p>
    </section>
  );
}
