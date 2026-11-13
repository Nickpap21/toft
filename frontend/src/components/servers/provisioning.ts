// A provisioning run's stages and log lines, read from the lines the
// provisioner writes (backend internal/server/provisioner.go), for the
// server page and Add a server (boards: Server, AddServer).

export type StageId = "ssh" | "docker" | "networks" | "traefik" | "firewall";
export type StageState = "done" | "now" | "todo" | "failed";

export interface Stage {
  id: StageId;
  label: string;
  state: StageState;
  /** What it found or changed: "29.1.3, unchanged", "restarted". */
  sub: string;
}

const ORDER: { id: StageId; label: string }[] = [
  { id: "ssh", label: "SSH" },
  { id: "docker", label: "Docker" },
  { id: "networks", label: "Networks" },
  { id: "traefik", label: "Traefik" },
  { id: "firewall", label: "Firewall" },
];

/** The stage a line starts or belongs to, in the provisioner's order: Docker,
 *  the address pool, the Loki driver, paas-network, Traefik, then ufw,
 *  fail2ban, the host firewall, backups and security updates. */
function stageOf(line: string): StageId | null {
  if (line.startsWith("Connecting via SSH")) return "ssh";
  if (/^(Docker already installed|Installing Docker)/.test(line)) return "docker";
  if (/[Ff]irewall|ufw|fail2ban|nftables|security updates|unattended|[Bb]ackup/.test(line)) return "firewall";
  if (/Traefik/.test(line)) return "traefik";
  if (/address pool|Loki Docker log driver|paas-network/.test(line)) return "networks";
  return null;
}

/** Each line with the stage it falls in (-1 before SSH): stages only move
 *  forward, so a later line that mentions an earlier stage stays where it is. */
function assign(lines: string[]): number[] {
  let at = -1;
  return lines.map((l) => {
    const s = stageOf(l);
    if (s) {
      const i = ORDER.findIndex((o) => o.id === s);
      if ((at >= 0 || s === "ssh") && i > at) at = i;
    }
    return at;
  });
}

function subOf(id: StageId, lines: string[]): string {
  const has = (re: RegExp) => lines.some((l) => re.test(l));
  switch (id) {
    case "ssh":
      return has(/^Pinned SSH host key/) ? "host key pinned" : "host key matched";
    case "docker": {
      const found = lines.map((l) => /^Docker already installed: Docker version ([\w.-]+)/.exec(l)).find(Boolean);
      if (found) return `${found[1]}, unchanged`;
      const v = lines.map((l) => /^\s*Version:\s+([\w.-]+)/.exec(l)).find(Boolean);
      return v ? `installed ${v[1]}` : "installed";
    }
    case "networks": {
      const pool = lines.map((l) => /address pool to (\S+)/.exec(l)).find(Boolean);
      if (pool) return pool[1];
      return has(/^Created paas-network/) ? "created" : "unchanged";
    }
    case "traefik":
      if (has(/restarting it/)) return "restarted";
      if (has(/left as it is/)) return "not Toft's, left as is";
      if (has(/started successfully/)) return "started";
      return "unchanged";
    case "firewall":
      if (has(/^Warning/)) return "warning";
      return has(/^(Firewall enabled|Host firewall loaded:|fail2ban installed)/) ? "set up" : "unchanged";
  }
}

/** What a running stage is doing, in the board's words. */
function nowOf(id: StageId, lines: string[]): string {
  if (id === "docker") return lines.some((l) => l.startsWith("Installing Docker")) ? "installing" : "checking";
  return { ssh: "connecting", networks: "setting up", traefik: "starting", firewall: "setting up" }[id];
}

/**
 * The run's stages: done up to the one its last line is in, which is now
 * (running), failed (the run failed) or done (it succeeded).
 */
export function stagesOf(lines: string[], outcome: "running" | "ok" | "failed"): Stage[] {
  const at = assign(lines);
  const last = at.length ? at[at.length - 1] : -1;
  return ORDER.map((o, i) => {
    const own = lines.filter((_, j) => at[j] === i);
    let state: StageState = "todo";
    if (outcome === "ok" || i < last) state = "done";
    else if (i === last || (last < 0 && i === 0 && outcome !== "running")) state = outcome === "failed" ? "failed" : "now";
    const sub = state === "done" ? subOf(o.id, own) : state === "failed" ? "failed" : state === "now" ? nowOf(o.id, own) : "—";
    return { id: o.id, label: o.label, state, sub };
  });
}

export type LineTone = "text" | "muted" | "ok" | "warn" | "error";

/** A log line's colour on the boards: muted for what was already there,
 *  green for what was made, amber for a restart or a warning. */
export function lineTone(line: string): LineTone {
  if (/\b(error|failed|fatal)\b/i.test(line) && !/non-fatal/.test(line)) return "error";
  if (/^warning|restarting it|^WARNING|^Server provisioned with warnings/i.test(line)) return "warn";
  if (/already|untouched|unchanged|^#|^\+ |^\s/.test(line)) return "muted";
  if (/successfully|^Created |started successfully|loaded:/.test(line)) return "ok";
  return "text";
}
