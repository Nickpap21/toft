// The Updates card's wording (D-133, D-134), kept apart from the card so it
// is tested. The alerts' rules are the backend's (server.HostUpdates.Health),
// and so is who may run what (server.CheckUpdateAction): this only says what
// the last read found and what a run does.
import type { HostUpdates, UpdateAction, UpdateRun } from "@/lib/api/server.service";
import { formatDateTime } from "@/lib/utils";

export type Tone = "ok" | "warn" | "bad" | "info";

export interface UpdateRow {
  key: string;
  label: string;
  /** The row's line on the board: "Security updates: 2 waiting". */
  title: string;
  tone: Tone;
  state: string;
  detail: string;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

const when = (iso: string | null) => (iso ? formatDateTime(iso) : "");

/** What unattended-upgrades is doing, in a sentence. */
export function unattendedDetail(u: HostUpdates["unattended"]): string {
  const last = u.last_run_at ? `Last ran ${when(u.last_run_at)}` : "It has never run";
  if (u.state === "missing") return "unattended-upgrades isn’t installed, so nothing installs security updates here.";
  if (u.last_result === "failed") {
    return `${last} and failed${u.last_error ? `: ${u.last_error.replace(/^\S+ \S+ ERROR /, "")}` : "."}`;
  }
  return u.state === "active" ? `${last}. It installs security updates each night.` : `${last}. Its service is stopped.`;
}

/** The card's rows, most urgent facts first. */
export function updateRows(u: HostUpdates): UpdateRow[] {
  const uu = u.unattended;
  const uuOK = uu.state === "active" && uu.last_result === "ok";
  const security: UpdateRow = u.security
    ? {
        key: "security",
        label: "Security updates",
        title: `Security updates: ${u.security} waiting`,
        tone: "warn",
        state: `${u.security} waiting`,
        detail: uuOK
          ? "unattended-upgrades installs them at its next nightly run; an alert fires if it runs and leaves them."
          : "unattended-upgrades isn’t installing them: see below.",
      }
    : { key: "security", label: "Security updates", title: "Security updates: none waiting", tone: "ok", state: "None waiting", detail: "From the package lists apt last downloaded." };
  const other: UpdateRow = {
    key: "other",
    label: "Other updates",
    title: `Other updates: ${u.other ? `${u.other} waiting` : "none waiting"}`,
    tone: u.other ? "info" : "ok",
    state: u.other ? `${u.other} waiting` : "None waiting",
    detail: u.other ? "Not security fixes, so nothing installs them on its own." : "Everything else is current.",
  };
  const reboot: UpdateRow = u.reboot.required
    ? {
        key: "reboot",
        label: "Reboot",
        title: u.reboot.since ? `Reboot required since ${when(u.reboot.since)}` : "Reboot required",
        tone: "warn",
        state: u.reboot.since ? `Required since ${when(u.reboot.since)}` : "Required",
        detail: u.reboot.packages.length
          ? `For ${u.reboot.packages.slice(0, 3).join(", ")}${u.reboot.packages.length > 3 ? ` and ${u.reboot.packages.length - 3} more` : ""}. Until then the old versions keep running.`
          : "Until then the old versions keep running.",
      }
    : { key: "reboot", label: "Reboot", title: "Reboot not needed", tone: "ok", state: "Not needed", detail: "Nothing installed asks for one." };
  const d = u.docker;
  const worst = d.advisories[0]?.severity;
  const docker: UpdateRow = {
    key: "docker",
    label: "Docker",
    title: !d.version
      ? "Docker isn’t answering"
      : d.advisories.length
        ? `Docker ${d.version}: ${plural(d.advisories.length, "advisory", "advisories")}`
        : d.candidate
          ? `Docker ${d.version}: ${d.candidate} available`
          : u.held
            ? `Docker ${d.version}: ${plural(u.held, "package update", "package updates")}`
            : d.package === "docker.io"
              ? `Docker ${d.version}`
              : `Docker ${d.version}: no advisories`,
    tone: !d.version ? "bad" : worst === "critical" || worst === "high" ? "bad" : worst || d.candidate || u.held ? "warn" : "ok",
    state: !d.version
      ? "Not answering"
      : d.advisories.length
        ? `${d.version} · ${plural(d.advisories.length, "advisory", "advisories")}`
        : d.candidate
          ? `${d.version} · ${d.candidate} available`
          : u.held
            ? `${d.version} · ${plural(u.held, "package update", "package updates")}`
            : d.package === "docker.io"
            ? d.version
            : `${d.version} · no advisories`,
    detail: !d.version
      ? "Docker didn’t answer the read."
      : d.advisories.length
        ? `Published advisories affect this engine${d.candidate ? `; ${d.candidate} is available` : ""}. Updating Docker restarts its containers.`
        : d.package === "docker.io"
          ? `The distribution’s docker.io, whose maintainers backport fixes, so Docker’s advisories aren’t matched against it.${d.candidate ? " Updating Docker restarts its containers." : ""}`
          : d.candidate
            ? "A newer engine is in Docker’s repository. Updating Docker restarts its containers."
            : u.held
              ? "The engine is current; one of Docker’s other packages has an update. Updating Docker restarts its containers."
              : "No published advisory affects this engine.",
  };
  const unattended: UpdateRow = {
    key: "unattended",
    label: "unattended-upgrades",
    title:
      uu.state === "missing"
        ? "unattended-upgrades isn’t installed"
        : uu.last_result === "failed"
          ? "unattended-upgrades: last run failed"
          : uu.state !== "active"
            ? "unattended-upgrades is stopped"
            : uu.last_result === "never"
              ? "unattended-upgrades has never run"
              : "unattended-upgrades is running",
    tone: uuOK ? "ok" : uu.state === "missing" || uu.last_result === "failed" || uu.state === "inactive" ? "bad" : "warn",
    state:
      uu.state === "missing"
        ? "Not installed"
        : uu.last_result === "failed"
          ? "Last run failed"
          : uu.state !== "active"
            ? "Stopped"
            : uu.last_result === "never"
              ? "Never ran"
              : "Running",
    detail: unattendedDetail(uu),
  };
  // Every count above comes from apt's lists: without them, "none waiting" misleads.
  const lists: UpdateRow[] = u.lists_updated_at
    ? []
    : [{
        key: "lists",
        label: "Package lists",
        title: "apt has never downloaded its package lists",
        tone: "warn",
        state: "Never downloaded",
        detail: "Updates published since the server was set up don’t count until apt’s daily job runs.",
      }];
  return [...lists, security, reboot, docker, unattended, other];
}

/** The card head's note on apt's lists, once apt has downloaded them
 *  (the row says so when it never has). */
export const listsFrom = (u: HostUpdates) => (u.lists_updated_at ? `lists from ${when(u.lists_updated_at)}` : "");

/** How many updates Install updates installs: Docker's wait for its own
 *  button. */
export const installCount = (u: HostUpdates) => Math.max(u.security + u.other - u.held, 0);

/** Docker came from a package and a newer one waits. */
/** A newer engine, or an update to one of Docker’s other packages (containerd.io). */
export const dockerUpdatable = (u: HostUpdates) => (u.docker.package === "docker-ce" || u.docker.package === "docker.io") && (!!u.docker.candidate || u.held > 0);

/** 2 min 10 s, 34 s, 1 h 5 min. */
export function duration(ms: number): string {
  const s = Math.max(Math.round(ms / 1000), 0);
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min${s % 60 ? ` ${s % 60} s` : ""}`;
  return `${Math.floor(s / 3600)} h${Math.floor((s % 3600) / 60) ? ` ${Math.floor((s % 3600) / 60)} min` : ""}`;
}

export const DOING: Record<UpdateAction, string> = { install: "Installing updates", docker: "Updating Docker", reboot: "Rebooting" };
export const DONE: Record<UpdateAction, string> = { install: "Updates installed", docker: "Docker updated", reboot: "Rebooted" };

export interface RunView {
  label: string;
  state: string;
  tone: "running" | "ok" | "bad";
  detail: string;
  meta: string;
  /** What a screen reader hears when it changes: no ticking time. */
  announce: string;
}

/** The run's row at the top of the card, at now (ms). */
export function runView(run: UpdateRun, now: number): RunView {
  const by = run.started_by ? ` by ${run.started_by}` : "";
  const started = new Date(run.started_at).getTime();
  if (run.outcome === "running" || run.outcome === "rebooting") {
    const step =
      run.outcome === "rebooting" || run.step === "reboot"
        ? "waiting for the server"
        : run.step === "starting"
          ? "starting"
          : run.step === "lists"
            ? "refreshing package lists"
            : run.action === "docker"
              ? "installing Docker"
              : run.packages
                ? `installing ${plural(run.packages, "package", "packages")}`
                : "installing";
    const what =
      run.action === "install"
        ? "Docker is held back, so containers keep running."
        : run.action === "docker"
          ? "Its containers restart with Docker."
          : "Its services are down until it’s back; if it isn’t back within 10 minutes, the run reads as failed.";
    return {
      label: DOING[run.action],
      state: `${step} · ${duration(now - started)}`,
      tone: "running",
      detail: `Started${by} at ${formatDateTime(run.started_at)}. ${what}`,
      meta: "It keeps running on the server if you leave this page.",
      announce: `${DOING[run.action]}: ${step}`,
    };
  }
  const took = run.finished_at ? ` · ${duration(new Date(run.finished_at).getTime() - started)}` : "";
  const meta = `${formatDateTime(run.started_at)}${took}`;
  if (run.outcome === "succeeded") {
    const state =
      run.action === "install"
        ? run.packages
          ? plural(run.packages, "package", "packages")
          : "nothing new to install"
        : run.action === "reboot"
          ? "back up"
          : "";
    return {
      label: DONE[run.action],
      state,
      tone: "ok",
      detail: `Run from Toft${by}.`,
      meta,
      announce: state ? `${DONE[run.action]}, ${state}` : DONE[run.action],
    };
  }
  return {
    label: `${DOING[run.action]} failed`,
    state: "",
    tone: "bad",
    detail: `Run from Toft${by}.${run.action === "install" ? " Docker and its containers weren’t touched." : ""}`,
    meta,
    announce: `${DOING[run.action]} failed`,
  };
}

export interface ConfirmCopy {
  title: string;
  lead: string;
  steps: string[];
  downTitle: string;
  down: string;
  /** The down box is a warning: something stops. */
  loud: boolean;
  note: string;
  confirm: string;
}

/** What each action's confirmation says. where is the server's name, or
 *  null for the panel's own server; services are what runs there; agent:
 *  the server is reached through its agent. */
export function confirmCopy(action: UpdateAction, u: HostUpdates, where: string | null, services: string[], agent = false): ConfirmCopy {
  const panel = where === null;
  const here = panel ? "the panel’s server" : where;
  const apps = panel
    ? `The panel, its API and git${services.length ? `, and ${plural(services.length, "service", "services")}` : ""}`
    : services.length > 4
      ? `${services.slice(0, 4).join(", ")}, Traefik and ${plural(services.length - 4, "more service", "more services")}`
      : services.length
        ? `${services.join(", ")} and Traefik`
        : "Traefik and every container here";
  const waits = panel ? "" : " Deploys to this server wait until it’s done.";
  if (action === "install") {
    const n = installCount(u);
    const kernel = u.packages.find((p) => p.name.startsWith("linux-image-"));
    const notes = [
      u.removals.length ? `apt removes ${u.removals.join(", ")} to install them.` : "",
      kernel ? `${kernel.name} asks for a reboot afterwards. The card says so when the run ends.` : "",
    ].filter(Boolean);
    return {
      title: `Install ${plural(n, "update", "updates")} on ${here}?`,
      lead: `The ${plural(n, "update", "updates")} the card lists${u.held ? ", Docker’s apart" : ""}, and any released since apt last downloaded its lists.`,
      steps: [
        "Refreshes apt’s package lists.",
        "Installs the updates. A config file changed on this server keeps this server’s copy.",
        "Restarts the system services whose libraries changed, as Ubuntu does after every update.",
      ],
      downTitle: "Nothing Toft runs goes down",
      down: "Docker and its packages are held back for this run, so every container keeps running. Docker’s own update is the button on its row.",
      loud: false,
      note: notes.join(" "),
      confirm: "Install updates",
    };
  }
  if (action === "docker") {
    const fixes = u.docker.advisories.length ? `Fixes the ${plural(u.docker.advisories.length, "advisory", "advisories")} listed. ` : "";
    return {
      title: `Update Docker on ${here}${u.docker.candidate ? ` to ${u.docker.candidate}` : ""}?`,
      lead: `${fixes}Updates Docker’s packages${u.docker.package === "docker.io" ? " from the distribution" : " from Docker’s repository"}.`,
      steps: ["Refreshes apt’s package lists.", `${u.docker.candidate ? `Installs Docker ${u.docker.candidate}` : "Installs the updates to Docker’s packages"}. Docker restarts as it installs.`, "Every container starts again by itself."],
      downTitle: panel ? "Toft and everything here restart" : "Everything here restarts",
      down: `${apps} stop and start again with Docker.${panel ? " This page reconnects by itself." : " Their sites don’t answer meanwhile."}${waits}`,
      loud: true,
      note: "",
      confirm: "Update Docker",
    };
  }
  const since = u.reboot.since ? ` since ${when(u.reboot.since)}` : "";
  return {
    title: panel ? "Reboot the panel’s server?" : `Reboot ${here}?`,
    lead: u.reboot.required
      ? `Loads the kernel and library updates installed${since}${u.reboot.packages.length ? `: ${u.reboot.packages.slice(0, 3).join(", ")}` : ""}.`
      : "Nothing installed asks for a reboot.",
    steps: [
      panel ? "Docker stops every container cleanly, the panel’s Postgres included." : "Docker stops every container cleanly.",
      "The server restarts.",
      panel
        ? "Docker starts everything again; this page reconnects by itself."
        : agent
          ? "Docker starts the containers again, and Toft’s agent connects back by itself."
          : "Docker starts the containers again by itself.",
    ],
    downTitle: panel ? "Toft and everything here are down" : "Everything here is down",
    down: panel
      ? `${apps}, until the server is back. Servers with the agent keep running their apps and connect back when the panel is up.`
      : `${apps}, until the server is back.${waits}`,
    loud: true,
    note: "",
    confirm: "Reboot",
  };
}

/** Pending packages' summary: "16 packages · 2 security, 14 other". */
export function packagesSummary(u: HostUpdates): string {
  return `${plural(u.security + u.other, "package", "packages")} · ${u.security} security, ${u.other} other`;
}

/** "29.5.10" > "29.5.2": dotted numbers compared part by part. */
function newer(a: string, b: string): boolean {
  const x = a.split(/[.-]/).map(Number), y = b.split(/[.-]/).map(Number);
  for (let i = 0; i < Math.max(x.length, y.length); i++) {
    const d = (x[i] || 0) - (y[i] || 0);
    if (d) return d > 0;
  }
  return false;
}

/** Docker advisories' summary: "5 affect 29.4.3 · 29.8.2 fixes all". */
export function advisoriesSummary(u: HostUpdates): string {
  const d = u.docker;
  const n = d.advisories.length;
  const head = `${n} ${n === 1 ? "affects" : "affect"} ${d.version}`;
  if (!d.candidate) return head;
  const fixed = d.advisories.filter((a) => !newer(a.fixed_in, d.candidate)).length;
  return `${head} · ${d.candidate} fixes ${fixed === n ? "all" : fixed}`;
}
