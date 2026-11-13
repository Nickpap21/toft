"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Copy, Info, Loader2, RotateCw, X } from "lucide-react";
import { ApiError, apiOrigin } from "@/lib/api/client";
import { serverService, type AgentJoin, type ConnectionResult, type ProvisionRun, type Requirements, type Server } from "@/lib/api/server.service";
import { useOrgRole } from "@/context/AuthContext";
import { cn } from "@/lib/utils";
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
  primaryButton,
  smallButton,
} from "@/components/settings/settings-frame";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { hostKeyFile } from "@/components/servers/host-key-dialog";
import { cores, diskGB, gib } from "@/components/servers/capacity";
import { clock, waitingForAgent } from "@/components/servers/agent";
import { addState, leftOnServer, logTail, namedFromHost, osOf, progressOf, shortOS, stoppedAt, validHost, validServerName, type AddState } from "./add-server";

// Add a server (board: ReworkAddServer, D-153). Opening the page makes the
// person's waiting server, server-<n>, or reuses theirs with a new command
// (POST /servers); ?server=<id> keeps a reload on it, and a reload makes a
// new command too, as the old one is shown only once. The agent's first
// join provisions it on the panel, requirements first, and names it after
// its hostname; this page follows by polling the server and its recorded
// run. A server that can't run the command is reached over SSH as root
// with a key Toft makes, then checked here (host key, requirements) before
// it is provisioned.

const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const sum = "text-[13px] text-muted";
const helpText = "text-[13px] leading-[19px] text-muted";
const label = "text-[13px] font-medium text-fg";
const smallPrimary = "touch-target inline-flex h-[30px] pointer-coarse:px-3.5 flex-none items-center gap-1.5 rounded-md bg-accent px-2.5 text-xs font-medium text-on-accent hover:bg-accent/90 disabled:opacity-50";
const ghostButton = "touch-target inline-flex h-9 items-center rounded-lg px-3 text-[13px] font-medium text-fg/85 hover:bg-raised disabled:opacity-50";

export default function AddServerPage() {
  return (
    <Suspense>
      <AddServer />
    </Suspense>
  );
}

function AddServer() {
  const router = useRouter();
  const params = useSearchParams();
  const qc = useQueryClient();
  const canManage = useOrgRole() === "owner";
  const serverId = params.get("server") ?? "";
  // The command this page made, for the server it made it for.
  const [made, setMade] = useState<{ id: string; join: AgentJoin } | null>(null);
  const madeFor = useRef<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [sshOpen, setSSHOpen] = useState(false);

  useEffect(() => {
    document.title = "Add a server · Servers · Toft";
  }, []);

  const server = useQuery({
    queryKey: ["server", serverId],
    queryFn: () => serverService.get(serverId),
    enabled: !!serverId,
    // Until it is provisioned or stopped; Try again starts it once more.
    refetchInterval: (q) => (q.state.data?.provisioned_at ? false : 2_000),
  });
  const srv = server.data;
  const followRun = !!srv && !srv.provisioned_at && (srv.connection === "ssh" || !!srv.agent?.key_fingerprint);
  const runQ = useQuery({
    queryKey: ["provision-run", serverId],
    queryFn: () => serverService.lastProvision(serverId),
    enabled: followRun || !!srv?.provisioned_at,
    refetchInterval: (q) => (followRun && !q.state.data?.finished_at ? 2_000 : false),
  });
  const run = followRun || srv?.provisioned_at ? runQ.data : null;

  const create = useMutation({
    mutationFn: () => serverService.createAgent(),
    onSuccess: (s) => {
      setMade({ id: s.id, join: s.join });
      qc.setQueryData(["server", s.id], s);
      qc.invalidateQueries({ queryKey: ["servers"] });
      router.replace(`/servers/new?server=${s.id}`);
    },
  });
  const fresh = useMutation({
    mutationFn: (id: string) => serverService.joinToken(id),
    onSuccess: (join, id) => setMade({ id, join }),
  });

  // Arriving makes (or reuses) the waiting server; a reload, a new command.
  // madeFor is the URL's server last handled: "" until router.replace
  // lands, so a re-render before then doesn't make another.
  useEffect(() => {
    if (!canManage) return;
    if (!serverId) {
      if (madeFor.current !== "") {
        madeFor.current = "";
        create.mutate();
      }
      return;
    }
    if (made?.id === serverId) madeFor.current = serverId;
    else if (srv && waitingForAgent(srv) && madeFor.current !== serverId) {
      madeFor.current = serverId;
      fresh.mutate(serverId);
    }
  }, [canManage, serverId, srv, made, create, fresh]);

  const cmd = made?.id === serverId ? made.join : null;
  const live = !!cmd && Date.parse(cmd.expires_at) > now;
  useEffect(() => {
    if (!cmd) return;
    const t = setInterval(() => setNow(Date.now()), 10_000);
    return () => clearInterval(t);
  }, [cmd]);

  if (!canManage) {
    return (
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold text-fg">Add a server</h1>
        <p className="text-[13px] text-muted">Only the workspace’s owners add servers.</p>
      </div>
    );
  }

  const failedToStart = create.error ?? fresh.error ?? server.error;
  const busy = !srv || (waitingForAgent(srv) && !cmd && (create.isPending || fresh.isPending || !fresh.isError));
  const state: AddState | null = srv && !busy ? addState(srv, run, live) : null;

  return (
    <div className="flex flex-col">
      <header className="mb-5 flex flex-wrap items-baseline gap-x-3 gap-y-1 md:-mt-3">
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Add a server</h1>
        <p className={sum}>for apps · it connects out to Toft</p>
      </header>
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
        <div className="flex min-w-0 flex-col gap-5">
          {failedToStart && !srv ? (
            <p role="alert" className="flex items-start gap-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
              <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
              {failedToStart instanceof ApiError && failedToStart.status < 500 ? failedToStart.message : "Toft couldn’t make the server’s command. Reload the page to try again."}
            </p>
          ) : !srv || !state ? (
            <section aria-label="Run this on the new server" className={cn(card, "flex items-center gap-2 px-4 py-6 text-[13px] text-muted")}>
              <Loader2 className="h-4 w-4 motion-safe:animate-spin" aria-hidden="true" />
              Making the command…
            </section>
          ) : state === "verify" ? (
            <Verify
              serverId={srv.id}
              name={srv.name}
              srv={srv}
              onRemove={() => router.push("/servers")}
              onBack={() => router.replace("/servers/new")}
            />
          ) : (
            <AddCard
              srv={srv}
              state={state}
              run={run}
              join={cmd}
              newCommand={() => fresh.mutate(srv.id)}
              making={fresh.isPending}
              makeError={fresh.error}
              sshOpen={sshOpen}
            />
          )}
          {srv && (state === "command" || state === "expired") && <SSHInstead serverId={srv.id} onToggle={setSSHOpen} />}
        </div>
        <aside>
          <section aria-labelledby="nd-h" className={card}>
            <h2 id="nd-h" className={cn(h2, "border-b border-subtle px-4 py-3")}>What it needs</h2>
            <dl className="flex flex-col text-[13px] leading-5">
              {[
                ["System", "Debian or Ubuntu, 64-bit (amd64 or arm64), with systemd and a 5.10 kernel or newer"],
                ["Network", `Out to ${panelHost(cmd?.command.match(/https?:\/\/[^/\s]+/)?.[0])}. In: 80 and 443 for your apps`],
                ["Size", "Any. Under 1 GiB of memory is small for apps; Toft shows what fits"],
              ].map(([k, v], i) => (
                <div key={k} className={cn("grid gap-x-3 gap-y-0.5 px-4 py-2.5 md:grid-cols-[110px_minmax(0,1fr)]", i > 0 && "border-t border-subtle")}>
                  <dt className="text-muted">{k}</dt>
                  <dd className="text-fg [overflow-wrap:anywhere]">{v}</dd>
                </div>
              ))}
            </dl>
          </section>
        </aside>
      </div>
    </div>
  );
}

/** "paas.example on 443": the address in the command, else this panel's. */
function panelHost(origin = apiOrigin()): string {
  try {
    const u = new URL(origin);
    return `${u.hostname} on ${u.port || (u.protocol === "http:" ? "80" : "443")}`;
  } catch {
    return "this panel on 443";
  }
}

/** The one card: the command, then what the server is doing. */
function AddCard({
  srv,
  state,
  run,
  join,
  newCommand,
  making,
  makeError,
  sshOpen,
}: {
  srv: Server;
  state: AddState;
  run: ProvisionRun | null | undefined;
  join: AgentJoin | null;
  newCommand: () => void;
  making: boolean;
  makeError: Error | null;
  sshOpen: boolean;
}) {
  const router = useRouter();
  const qc = useQueryClient();
  const [copiedCmd, setCopiedCmd] = useState("");
  const copied = !!join && copiedCmd === join.command;
  const [name, setName] = useState<string | null>(null);
  const viaSSH = srv.connection === "ssh";
  const joined = state === "joined";
  const stopped = state === "unmet" || state === "failed";
  const waitingCmd = state === "command" || state === "expired";
  // No command at all: making one failed (after a reload); nothing expired.
  const noCommand = state === "expired" && !join && !!makeError;

  const reqs = useQuery<Requirements>({
    queryKey: ["server-requirements", srv.id],
    queryFn: () => serverService.requirements(srv.id),
    enabled: state === "unmet",
    retry: false,
  });
  const unmet = reqs.data?.checks.filter((q) => !q.met) ?? [];

  const retry = useMutation({
    mutationFn: async () => {
      // The run is recorded as it goes; the polling shows it.
      qc.setQueryData(["provision-run", srv.id], { started_at: new Date().toISOString(), started_by: "", finished_at: null, ok: null, error: "", log: "" });
      await serverService.provision(srv.id, () => {});
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["provision-run", srv.id] });
      qc.invalidateQueries({ queryKey: ["server", srv.id] });
      qc.invalidateQueries({ queryKey: ["server-requirements", srv.id] });
      qc.invalidateQueries({ queryKey: ["servers"] });
    },
  });
  const value = name ?? srv.name;
  const nameBad = !validServerName(value);
  const use = useMutation({
    mutationFn: async () => {
      if (value !== srv.name) await serverService.rename(srv.id, value);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["servers"] });
      qc.invalidateQueries({ queryKey: ["server", srv.id] });
      router.push(`/servers/${srv.id}`);
    },
  });

  const os = osOf(run) || shortOS(reqs.data?.os ?? "");
  const conn = [
    viaSSH ? `Connected over SSH to ${srv.ip_address}` : `Connected from ${srv.agent?.from_ip || srv.ip_address}`,
    os,
    srv.total_cpu_cores ? `${cores(srv.total_cpu_cores)} vCPU` : "",
    srv.total_memory_mb ? `${gib(srv.total_memory_mb)} GiB` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  const usedAt = srv.agent?.key_pinned_at;
  const cmdSum = viaSSH
    ? `as root · port ${srv.port}`
    : waitingCmd
      ? noCommand
        ? "no command yet"
        : state === "expired"
          ? `expired${join ? ` at ${clock(join.expires_at)}` : ""}, unused`
          : `as root · works once, until ${join ? clock(join.expires_at) : ""}`
      : `used${usedAt ? ` at ${clock(usedAt)}` : ""}${srv.agent?.from_ip ? ` from ${srv.agent.from_ip}` : ""}`;
  const small = joined && !!srv.total_memory_mb && srv.total_memory_mb < 1024;

  return (
    <section aria-labelledby="rc-h" className={card}>
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 border-b border-subtle px-4 py-3">
        <h2 id="rc-h" className={cn(h2, "min-w-0 [overflow-wrap:anywhere]")}>{viaSSH ? `${srv.name} over SSH` : "Run this on the new server"}</h2>
        <span className={cn(sum, state === "expired" && !noCommand && "text-status-failed-fg")}>{cmdSum}</span>
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        {!viaSSH && (
          <>
            {/* A command is shown only to the page that made it (it is kept hashed). */}
            {join && (
              <div className="flex items-start gap-2.5 rounded-lg border border-subtle bg-ground p-3">
                <code className={cn("min-w-0 flex-1 font-mono text-xs leading-[19px] [overflow-wrap:anywhere]", state === "expired" ? "text-muted line-through" : "text-fg")}>
                  {join.command}
                </code>
                {state === "expired" ? (
                  <button type="button" className={smallPrimary} disabled={making} onClick={newCommand}>
                    {making ? "Making…" : "New command"}
                  </button>
                ) : waitingCmd ? (
                  <button
                    type="button"
                    className={sshOpen ? cn(smallButton, "flex-none pointer-coarse:px-3.5") : smallPrimary}
                    onClick={() =>
                      navigator.clipboard?.writeText(join.command).then(
                        () => setCopiedCmd(join.command),
                        () => toast.error("Couldn’t copy: select it and copy it"),
                      )
                    }
                  >
                    {copied && <Check className="h-3 w-3" aria-hidden="true" />}
                    {copied ? "Copied" : "Copy"}
                  </button>
                ) : null}
              </div>
            )}
            {!join && state === "expired" && (
              <button type="button" className={cn(smallPrimary, "self-start")} disabled={making} onClick={newCommand}>
                {making ? "Making…" : noCommand ? "Make a command" : "New command"}
              </button>
            )}
            {state === "command" && <p className={helpText}>Opening this page makes a new command. An earlier one stops working.</p>}
            <p className={helpText}>
              It installs a small agent that connects out to Toft over HTTPS, then Toft installs Docker, Traefik and the firewall through it. Nothing on the
              server needs to be opened to Toft, and Toft never holds an SSH key for it.
            </p>
          </>
        )}
        {makeError && (
          <p role="alert" className="text-[13px] text-status-failed-fg">
            {makeError instanceof ApiError && makeError.status < 500 ? makeError.message : "Toft couldn’t make a command. Try again."}
          </p>
        )}
        <div role="status" className="flex flex-col gap-3.5 empty:hidden">
        {state === "command" && (
          <p className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-subtle bg-ground px-3.5 py-3 text-sm text-fg">
            <Loader2 className="h-[15px] w-[15px] text-link motion-safe:animate-spin" aria-hidden="true" />
            Waiting for the server to connect. This page moves on by itself.
          </p>
        )}
        {state === "expired" && !noCommand && (
          <p className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-subtle bg-ground px-3.5 py-3 text-sm text-fg">
            <Info className="h-[15px] w-[15px] flex-none text-muted" aria-hidden="true" />
            Nothing ran this command, and it no longer works. Make a new one; the server keeps waiting for it.
          </p>
        )}
        {!waitingCmd && (
          <ul aria-label="Progress" className="flex flex-col gap-2 text-[13px] text-fg">
            <li className="flex items-start gap-2">
              <Check className="mt-[3px] h-3.5 w-3.5 flex-none text-status-running" aria-hidden="true" />
              {conn}
            </li>
            {progressOf(state, run, unmet.length).map((p) => (
              <li key={p.text} className={cn("flex items-start gap-2", p.tone === "bad" && "text-status-failed-fg", p.tone === "warn" && "text-status-pending-fg")}>
                {p.tone === "ok" ? (
                  <Check className="mt-[3px] h-3.5 w-3.5 flex-none text-status-running" aria-hidden="true" />
                ) : p.tone === "now" ? (
                  <Loader2 className="mt-[3px] h-3.5 w-3.5 flex-none text-link motion-safe:animate-spin" aria-hidden="true" />
                ) : p.tone === "warn" ? (
                  <AlertTriangle role="img" aria-label="Warning" className="mt-[3px] h-3.5 w-3.5 flex-none text-status-pending" />
                ) : (
                  <X className="mt-[3px] h-3.5 w-3.5 flex-none text-status-failed" aria-hidden="true" />
                )}
                {p.text}
              </li>
            ))}
          </ul>
        )}
        </div>
        {state === "installing" && (
          <p className={helpText}>This page moves on by itself. You can leave it: the server shows in Servers once it’s ready.</p>
        )}
        {stopped && run && (
          <div className="flex flex-col gap-2.5 rounded-lg border border-status-failed/35 bg-ground px-3.5 py-3">
            <p role="alert" className="text-[13px] font-semibold text-fg">
              {state === "unmet" ? "Toft can’t use this server yet. Nothing was installed." : stoppedAt(run)}
            </p>
            {state === "unmet" &&
              (reqs.isLoading ? (
                <p className={helpText}>Reading the server…</p>
              ) : unmet.length ? (
                unmet.map((q) => (
                  <div key={q.id} className="flex flex-col gap-0.5 text-[13px]">
                    <p>
                      <span className="font-medium text-fg">{q.label}</span>
                      <span className="font-mono text-xs text-fg/70"> · {q.found}</span>
                    </p>
                    {q.fix && <p className={helpText}>{q.fix}</p>}
                  </div>
                ))
              ) : (
                <p className={cn(helpText, "[overflow-wrap:anywhere]")}>{run.error || reqs.error?.message}</p>
              ))}
            {state === "failed" && (
              <pre className="m-0 whitespace-pre-wrap rounded-md border border-subtle px-3 py-2.5 font-mono text-xs leading-[18px] text-fg/70 [overflow-wrap:anywhere]">{logTail(run)}</pre>
            )}
            <p className={helpText}>
              {state === "unmet"
                ? `Check again reads the server once more.${viaSSH ? "" : " The agent stays connected."}`
                : "Try again runs the install from where it stopped; every step is safe to repeat."}
            </p>
          </div>
        )}
        {joined && (
          <>
            {small && (
              <p className="flex items-start gap-2 text-[13px] text-fg">
                <AlertTriangle className="mt-[3px] h-3.5 w-3.5 flex-none text-status-pending" aria-hidden="true" />
                {gib(srv.total_memory_mb!)} GiB of memory is small for running apps. Toft shows what fits when you deploy.
              </p>
            )}
            <div className="flex flex-col gap-1.5">
              <label htmlFor="as-name" className={label}>Name</label>
              <input
                id="as-name"
                value={value}
                onChange={(e) => {
                  setName(e.target.value);
                  use.reset();
                }}
                aria-invalid={nameBad || use.isError}
                aria-describedby="as-name-help"
                spellCheck={false}
                autoCapitalize="none"
                className={cn(fieldInput, "font-mono", (nameBad || use.isError) && "border-danger")}
              />
              <p id="as-name-help" className={cn("text-xs leading-5", nameBad || use.isError ? "text-status-failed-fg" : "text-muted")}>
                {use.isError
                  ? use.error instanceof ApiError && use.error.status === 409
                    ? `Another server in this workspace is named ${value}. Pick another name.`
                    : use.error.message || "The name wasn’t saved. Try again."
                  : nameBad
                    ? "Lowercase letters, digits and dashes, starting and ending with a letter or digit, up to 63 characters."
                    : `How it’s shown everywhere. Lowercase letters, digits and dashes.${namedFromHost(run) ? " From the server’s hostname." : ""}`}
              </p>
            </div>
          </>
        )}
      </div>
      {joined && (
        <div className="flex justify-end border-t border-subtle px-4 py-3">
          <button type="button" className={primaryButton} disabled={nameBad || use.isPending} onClick={() => use.mutate()}>
            {use.isPending ? "Saving…" : "Use this server"}
          </button>
        </div>
      )}
      {stopped && (
        <div className="flex flex-wrap justify-end gap-2 border-t border-subtle px-4 py-3">
          <RemoveServer srv={srv} run={run} className={ghostButton} label="Remove server" disabled={retry.isPending} onRemoved={() => router.push("/servers")} />
          <button type="button" className={primaryButton} disabled={retry.isPending} onClick={() => retry.mutate()}>
            {retry.isPending ? "Checking…" : state === "unmet" ? "Check again" : "Try again"}
          </button>
        </div>
      )}
    </section>
  );
}

/** "Can't run a command there?": reach the waiting server over SSH as root,
 *  with the key Toft makes for it, or one the person has. */
function SSHInstead({ serverId, onToggle }: { serverId: string; onToggle: (open: boolean) => void }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [address, setAddress] = useState("");
  const [port, setPort] = useState("22");
  const [ownKey, setOwnKey] = useState("");
  const key = useQuery({
    queryKey: ["server-ssh-key", serverId],
    queryFn: () => serverService.sshKey(serverId),
    enabled: open,
    staleTime: Infinity,
    retry: false,
  });
  const portN = Number(port);
  const portOK = /^\d+$/.test(port) && portN >= 1 && portN <= 65535;
  const hostBad = !!address && !validHost(address);
  const ready = validHost(address) && portOK && (!!ownKey.trim() || key.isSuccess);
  const move = useMutation({
    mutationFn: () => serverService.useSSH(serverId, { ip_address: address.trim(), port: portN, ...(ownKey.trim() ? { ssh_key: ownKey } : {}) }),
    onSuccess: (s) => {
      qc.setQueryData(["server", serverId], s);
      qc.invalidateQueries({ queryKey: ["servers"] });
    },
  });
  const line = key.data ? `mkdir -p /root/.ssh && echo '${key.data.public_key}' >> /root/.ssh/authorized_keys` : "";

  return (
    <details
      className={card}
      onToggle={(e) => {
        setOpen(e.currentTarget.open);
        onToggle(e.currentTarget.open);
      }}
    >
      <summary className="cursor-pointer px-4 py-3 text-[13px] font-medium text-fg">Can’t run a command there? Connect over SSH instead</summary>
      <form
        className="flex flex-col gap-4 px-4 pb-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) move.mutate();
        }}
      >
        <p className={helpText}>For a server that can’t reach Toft. Toft signs in as root over SSH for every deploy, log and shell, and keeps the server’s host key.</p>
        <div className="flex flex-wrap gap-3">
          <div className="flex min-w-0 flex-[1_1_240px] flex-col gap-1.5">
            <label htmlFor="ss-ip" className={label}>Address</label>
            <input
              id="ss-ip"
              value={address}
              placeholder="198.51.100.7"
              onChange={(e) => setAddress(e.target.value)}
              aria-invalid={hostBad}
              aria-describedby="ss-ip-help"
              spellCheck={false}
              autoCapitalize="none"
              className={cn(fieldInput, "font-mono", hostBad && "border-danger")}
            />
            <p id="ss-ip-help" className={cn("text-xs leading-5", hostBad ? "text-status-failed-fg" : "text-muted")}>
              {hostBad ? "Enter an IP address or a hostname." : "IP address or hostname"}
            </p>
          </div>
          <div className="flex w-[120px] flex-col gap-1.5">
            <label htmlFor="ss-port" className={label}>Port</label>
            <input
              id="ss-port"
              inputMode="numeric"
              value={port}
              onChange={(e) => setPort(e.target.value.trim())}
              aria-invalid={!portOK}
              aria-describedby="ss-port-help"
              className={cn(fieldInput, "font-mono", !portOK && "border-danger")}
            />
            <p id="ss-port-help" className={cn("text-xs leading-5", portOK ? "text-muted" : "text-status-failed-fg")}>1 to 65535</p>
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <span className={label}>User</span>
          <span className="font-mono text-[13px] text-fg">root</span>
          <p className="text-xs leading-5 text-muted">Toft 1.0 signs in as root. A user with sudo isn’t supported yet: Toft doesn’t run its install through sudo.</p>
        </div>
        <div className="flex flex-col gap-1.5">
          <span className={label}>Add this key for root on the server</span>
          {key.isError ? (
            <p role="alert" className="text-[13px] text-status-failed-fg">{key.error.message || "Toft couldn’t make the key."}</p>
          ) : line ? (
            <CodeBox text={line} />
          ) : (
            <p className={helpText}>Making the key…</p>
          )}
          <p className="text-xs leading-5 text-muted">Toft made this key for this server. Its private half never leaves Toft.</p>
        </div>
        <details>
          <summary className="-my-2.5 cursor-pointer py-2.5 text-[13px] font-medium text-fg pointer-coarse:py-3">Use an existing key instead</summary>
          <div className="mt-2 flex flex-col gap-1.5">
            <label htmlFor="ss-key" className={label}>Private key</label>
            <textarea
              id="ss-key"
              value={ownKey}
              onChange={(e) => setOwnKey(e.target.value)}
              aria-describedby="ss-key-help"
              placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
              spellCheck={false}
              className="h-28 resize-y rounded-lg border border-strong bg-ground p-2.5 font-mono text-xs leading-[18px] text-fg placeholder:text-muted"
            />
            <p id="ss-key-help" className="text-xs leading-5 text-muted">Stored encrypted. Toft forgets the key it made.</p>
          </div>
        </details>
        {move.isError && (
          <p role="alert" className="flex items-start gap-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
            {/* 502: Toft couldn't sign in, and says why. */}
            {move.error instanceof ApiError && (move.error.status < 500 || move.error.status === 502) ? move.error.message : "Toft couldn’t switch the server to SSH. Try again."}
          </p>
        )}
        <div className="-mx-4 -mb-3 flex justify-end border-t border-subtle px-4 py-3">
          <button type="submit" className={primaryButton} disabled={!ready || move.isPending}>
            {move.isPending ? "Connecting…" : "Connect and check"}
          </button>
        </div>
      </form>
    </details>
  );
}

/** Remove server (D-153): a server that joined (its agent, or over SSH) is
 *  confirmed first, naming it and what Toft left on the machine; a waiting
 *  one goes at once. */
function RemoveServer({
  srv,
  run,
  className,
  label,
  disabled,
  onRemoved,
}: {
  srv: Server;
  run: ProvisionRun | null | undefined;
  className: string;
  label: string;
  disabled?: boolean;
  onRemoved: () => void;
}) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const joined = srv.connection === "ssh" || !!srv.agent?.key_fingerprint;
  const left = leftOnServer(srv, run);
  const remove = useMutation({
    mutationFn: () => serverService.delete(srv.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["servers"] });
      toast.success(`${srv.name} removed`);
      onRemoved();
    },
    onError: (err) => {
      if (!joined) toast.error(err.message || "The server could not be removed");
    },
  });
  const listed = left.length > 1 ? `${left.slice(0, -1).join(", ")} and ${left[left.length - 1]}` : left[0];
  return (
    <>
      <button type="button" className={className} disabled={disabled || remove.isPending} onClick={() => (joined ? setOpen(true) : remove.mutate())}>
        {joined ? `${label}…` : label}
      </button>
      <Dialog
        open={open}
        onOpenChange={(o) => {
          if (remove.isPending) return;
          setOpen(o);
          if (!o) remove.reset();
        }}
      >
        <DialogContent className={dialogContent}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={cn(dialogTitle, "[overflow-wrap:anywhere]")}>
              Remove <span className="font-mono font-medium">{srv.name}</span>?
            </DialogTitle>
            <DialogDescription className={dialogText}>
              Toft forgets this server. Nothing on the machine is stopped or deleted{listed ? `: ${listed} ${left.length > 1 ? "stay" : "stays"} there.` : "."}
            </DialogDescription>
          </DialogHeader>
          {remove.error && (
            <p role="alert" className="mx-5 mt-3.5 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
              {remove.error instanceof ApiError && remove.error.status < 500 ? remove.error.message : "The server was not removed."}
            </p>
          )}
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} disabled={remove.isPending} onClick={() => setOpen(false)}>
              Cancel
            </button>
            <button type="button" className={dangerButton} disabled={remove.isPending} onClick={() => remove.mutate()}>
              {remove.isPending ? "Removing…" : "Remove server"}
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** A command that wraps, with its Copy. */
function CodeBox({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-start gap-2.5 rounded-lg border border-subtle bg-ground p-3">
      <code className="min-w-0 flex-1 font-mono text-xs leading-[19px] text-fg [overflow-wrap:anywhere]">{text}</code>
      <button
        type="button"
        className={cn(smallButton, "flex-none pointer-coarse:px-3.5")}
        onClick={() =>
          navigator.clipboard?.writeText(text).then(
            () => setCopied(true),
            () => toast.error("Couldn’t copy: select it and copy it"),
          )
        }
      >
        {copied && <Check className="h-3 w-3" aria-hidden="true" />}
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

function CodeLine({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-center gap-2 rounded-md border border-subtle bg-ground py-1.5 pl-3 pr-1.5">
      <code role="group" tabIndex={0} aria-label="Command to run on the server" className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs text-fg">{text}</code>
      <button
        type="button"
        className="inline-flex h-7 flex-none items-center gap-1 rounded-md border border-subtle bg-raised px-2.5 text-xs text-fg hover:bg-raised/70"
        onClick={() =>
          navigator.clipboard?.writeText(text).then(
            () => setCopied(true),
            () => toast.error("Couldn’t copy: select it and copy it"),
          )
        }
      >
        {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

/** A server moved to SSH: its host key, checked on its console, and the
 *  requirements; then provisioning, which the card follows. */
function Verify({ serverId, name, srv, onRemove, onBack }: { serverId: string; name: string; srv: Server; onRemove: () => void; onBack: () => void }) {
  const queryClient = useQueryClient();
  const [matched, setMatched] = useState(false);
  // Connect and check's disclosure is gone: focus moves here.
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);
  const discard = useMutation({
    mutationFn: () => serverService.delete(serverId),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["servers"] }),
    onError: (err) => toast.error(err.message || "The server could not be removed"),
  });
  const busy = discard.isPending;
  const onProvision = () => {
    // The run is recorded as it goes; the card follows it.
    queryClient.setQueryData(["provision-run", serverId], { started_at: new Date().toISOString(), started_by: "", finished_at: null, ok: null, error: "", log: "" });
    serverService
      .provision(serverId, () => {})
      // A failure is the recorded run's, which the card shows.
      .catch(() => {})
      .finally(() => {
        queryClient.invalidateQueries({ queryKey: ["provision-run", serverId] });
        queryClient.invalidateQueries({ queryKey: ["server", serverId] });
      });
  };
  // The first connection pins the host key; then the system is read.
  const conn = useQuery<ConnectionResult>({
    queryKey: ["server-connect", serverId],
    queryFn: () => serverService.testConnection(serverId),
    retry: false,
    staleTime: Infinity,
  });
  const reqs = useQuery<Requirements>({
    queryKey: ["server-requirements", serverId],
    queryFn: () => serverService.requirements(serverId),
    enabled: conn.isSuccess,
    retry: false,
  });
  const scan = useQuery({
    queryKey: ["server-scan", serverId],
    queryFn: () => serverService.discoverCapacity(serverId),
    enabled: conn.isSuccess,
    retry: false,
    staleTime: Infinity,
  });
  const { data: summary } = useQuery({
    queryKey: ["server-capacity", serverId],
    queryFn: () => serverService.getCapacitySummary(serverId),
    enabled: scan.isSuccess,
  });
  const blocked = reqs.data && !reqs.data.met;
  const checkAgain = () => {
    queryClient.invalidateQueries({ queryKey: ["server-requirements", serverId] });
    conn.refetch();
  };
  const command = `ssh-keygen -lf ${hostKeyFile(conn.data?.host_key_type)}`;

  return (
    <section aria-labelledby="v-h" className={cn(card, "flex flex-col")}>
      <div className="flex flex-col gap-0.5 border-b border-subtle px-4 py-3">
        <h2 id="v-h" ref={heading} tabIndex={-1} className="text-[15px] font-semibold leading-[22px] text-fg outline-none">Check it’s your server</h2>
        <span className="text-xs text-muted">
          {conn.isLoading ? "Connecting…" : conn.isError || !conn.data ? "Not connected." : `Connected in ${conn.data.latency_ms} ms. Nothing has been installed.`}
        </span>
      </div>

      {conn.isError ? (
        <div className="flex flex-col gap-3 p-4">
          <p role="alert" className="flex items-start gap-2.5 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-failed-fg">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
            <span className="min-w-0 break-words">
              The panel couldn’t sign in to {name}: {conn.error.message.replace(/^connection failed: /, "")}. Check the address, the port and that the key’s
              public half is in the user’s ~/.ssh/authorized_keys.
            </span>
          </p>
        </div>
      ) : (
        <div className="flex flex-col gap-[18px] p-4">
          <div className="flex flex-col gap-2.5">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h3 className="text-[13px] font-semibold text-fg">Host key</h3>
              <span className="text-xs text-muted">{conn.data?.host_key_newly_pinned ? "Pinned on this first connection" : conn.data ? "Pinned" : ""}</span>
            </div>
            <div className="break-all rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-[13px] leading-5 text-fg">
              {conn.data ? (
                <>
                  <span className="text-muted">{conn.data.host_key_type}</span> {conn.data.host_key_fingerprint}
                </>
              ) : (
                <span className="font-sans text-muted">Connecting…</span>
              )}
            </div>
            <p className="text-[13px] leading-[19px] text-fg/85">
              Every later connection must present this key, or the panel refuses it before sending any credential. Check it now on the server’s console
              (your provider’s web console, or a session you already trust):
            </p>
            <CodeLine text={command} />
            <label className="flex min-h-6 cursor-pointer items-start gap-2.5 text-[13px] text-fg">
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]"
                checked={matched}
                disabled={!conn.data}
                onChange={(e) => setMatched(e.target.checked)}
              />
              <span>
                The <span className="font-mono text-xs">SHA256:</span> fingerprint matches what the server’s console shows
              </span>
            </label>
          </div>

          <div className="flex flex-col gap-2.5 border-t border-subtle pt-4">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h3 className="text-[13px] font-semibold text-fg">Requirements</h3>
              {summary?.capacity_discovered && (
                <span className="font-mono text-xs text-muted">
                  {cores(summary.effective_cpu_cores)} vCPU · {gib(summary.effective_memory_mb)} GiB
                  {summary.disk_total_mb ? ` · ${diskGB(summary.disk_total_mb)} disk` : ""}
                </span>
              )}
            </div>
            {reqs.isError ? (
              <p role="alert" className="text-[13px] text-status-failed-fg">The requirements could not be read: {reqs.error.message}</p>
            ) : (
              <ul className="overflow-hidden rounded-lg border border-subtle">
                {(reqs.data?.checks ?? []).map((q, i) => (
                  <li key={q.id} className={cn("flex flex-wrap items-center gap-2.5 px-3 py-[9px] text-[13px]", i > 0 && "border-t border-subtle")}>
                    {q.met ? <Check className="h-3.5 w-3.5 text-status-running" aria-hidden="true" /> : <X className="h-3.5 w-3.5 text-status-failed" aria-hidden="true" />}
                    <span className="flex-[0_0_210px] text-fg">{q.label}</span>
                    <span className={cn("min-w-0 flex-[1_1_200px] font-mono text-xs", q.met ? "text-fg/85" : "text-status-failed-fg")}>{q.found}</span>
                    <span className="sr-only">{q.met ? "Met" : "Not met"}</span>
                  </li>
                ))}
                {!reqs.data && (
                  <li className="flex items-center gap-2 px-3 py-[9px] text-[13px] text-muted">
                    <Loader2 className="h-3.5 w-3.5 motion-safe:animate-spin" aria-hidden="true" />
                    Reading the system…
                  </li>
                )}
              </ul>
            )}
            {blocked &&
              reqs.data!.checks
                .filter((q) => !q.met)
                .map((q) => (
                  <div key={q.id} role="alert" className="flex gap-2.5 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-status-failed-fg">
                    <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
                    <span>{q.fix}</span>
                  </div>
                ))}
          </div>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2 border-t border-subtle px-4 py-3">
        <RemoveServer
          srv={srv}
          run={null}
          className="h-9 rounded-lg px-2.5 text-[13px] font-medium text-status-failed-fg hover:bg-status-failed-soft disabled:opacity-50"
          label={blocked || conn.isError ? "Remove server" : "It doesn’t match: remove server"}
          disabled={busy}
          onRemoved={onRemove}
        />
        <span className="flex-1" />
        <button type="button" className={cancelButton} disabled={busy} onClick={() => discard.mutate(undefined, { onSuccess: onBack })}>Back</button>
        {blocked || conn.isError ? (
          <button type="button" className={primaryButton} onClick={checkAgain} disabled={conn.isFetching || reqs.isFetching}>
            <RotateCw className={cn("h-3.5 w-3.5", (conn.isFetching || reqs.isFetching) && "motion-safe:animate-spin")} aria-hidden="true" />
            Check again
          </button>
        ) : (
          <button type="button" className={primaryButton} disabled={!matched || !reqs.data?.met} onClick={onProvision}>
            <span className="max-w-[240px] truncate">Provision {name}</span>
          </button>
        )}
      </div>
    </section>
  );
}
