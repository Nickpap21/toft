// Add a server's rules (board: ReworkAddServer, D-153), kept apart from the
// page so they are tested.
import type { ProvisionRun, Server } from "@/lib/api/server.service";
import { stagesOf } from "@/components/servers/provisioning";
import { missingList } from "@/components/servers/capacity";

/** Loose on purpose: an IPv4 or IPv6 address or a hostname; it catches
 *  typos such as spaces or commas. The API validates it again. */
export function validHost(value: string): boolean {
  const v = value.trim();
  if (!v) return false;
  if (/^(\d{1,3}\.){3}\d{1,3}$/.test(v)) return v.split(".").every((octet) => Number(octet) <= 255);
  return /^[a-zA-Z0-9.:_-]+$/.test(v);
}

/** The name rule for a new name or a rename (the API's, D-153): a hostname
 *  label. */
export function validServerName(name: string): boolean {
  return /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name);
}

export type AddState = "command" | "expired" | "verify" | "installing" | "unmet" | "failed" | "joined";

/** Where Add a server is for this server: its last provisioning run first,
 *  then how it connects. commandLive: the page holds a command that hasn't
 *  expired. */
export function addState(s: Pick<Server, "connection" | "agent" | "provisioned_at">, run: ProvisionRun | null | undefined, commandLive: boolean): AddState {
  if (s.provisioned_at) return "joined";
  if (run && !run.finished_at) return "installing";
  // Requirements come first; a run that never met them installed nothing.
  if (run) return run.log.includes("Requirements met:") ? "failed" : "unmet";
  if (s.connection === "ssh") return "verify";
  // Joined: provisioning starts on its own in a moment.
  if (s.agent?.key_fingerprint) return "installing";
  return commandLive ? "command" : "expired";
}

const lines = (run: ProvisionRun | null | undefined) => (run?.log ? run.log.replace(/\n$/, "").split("\n") : []);

/** "Ubuntu 24.04" from the run's "Requirements met: Ubuntu 24.04.4 LTS · amd64, …". */
export function osOf(run: ProvisionRun | null | undefined): string {
  const l = lines(run).find((x) => x.startsWith("Requirements met: "));
  return l ? shortOS(l.slice(18).split(" · ")[0]) : "";
}

/** "Ubuntu 24.04" from "Ubuntu 24.04.4 LTS". */
export function shortOS(os: string): string {
  return os.replace(/^(\S+ \d+\.\d+)(\.\d+)?( LTS)?/, "$1");
}

/** The run named the server after its hostname. */
export function namedFromHost(run: ProvisionRun | null | undefined): boolean {
  return lines(run).some((l) => /^Named \S+, from its hostname$/.test(l));
}

/** The checklist under the command, past "Connected from …". */
export function progressOf(state: AddState, run: ProvisionRun | null | undefined, unmet: number): { tone: "ok" | "now" | "warn" | "bad"; text: string }[] {
  const ls = lines(run);
  const met = ls.some((l) => l.startsWith("Requirements met:"));
  const outcome = !run || !run.finished_at ? "running" : run.ok ? "ok" : "failed";
  const stages = stagesOf(ls, outcome);
  switch (state) {
    case "installing":
      return met
        ? [{ tone: "ok", text: "Requirements met" }, { tone: "now", text: "Installing Docker, Traefik and the firewall · about 2 minutes" }]
        : [{ tone: "now", text: "Checking the requirements" }];
    case "unmet":
      return [{ tone: "bad", text: unmet === 1 ? "1 requirement not met" : unmet > 1 ? `${unmet} requirements not met` : "The requirements couldn’t be read" }];
    case "failed": {
      const at = stages.find((s) => s.state === "failed");
      return [{ tone: "ok", text: "Requirements met" }, { tone: "bad", text: at && at.id !== "ssh" ? `Installing ${at.label} failed` : "Installing failed" }];
    }
    case "joined": {
      const docker = /(\d[\w.-]*)/.exec(stages.find((s) => s.id === "docker")?.sub ?? "")?.[1];
      const name = docker ? `Docker ${docker}` : "Docker";
      // Provisioned all the same (D-37), but not as a clean success.
      if (run?.warnings?.length) {
        return [
          { tone: "ok", text: `${name} and Traefik ready` },
          { tone: "warn", text: `Provisioned without ${missingList(run.warnings)}: the server’s log says why; Re-provision there once it’s fixed` },
        ];
      }
      return [{ tone: "ok", text: `${name}, firewall and Traefik ready` }];
    }
    default:
      return [];
  }
}

/** A failed install's headline: where it stopped and what is in place. */
export function stoppedAt(run: ProvisionRun): string {
  const stages = stagesOf(lines(run), "failed");
  const at = stages.find((s) => s.state === "failed");
  if (!at || at.id === "ssh") return "Installing stopped before it changed anything.";
  const done = stages.filter((s) => s.state === "done" && s.id !== "ssh").map((s) => (s.id === "networks" ? "the networks" : s.label));
  if (!done.length) return `Installing stopped at ${at.label}. Nothing else changed.`;
  const list = done.length > 1 ? `${done.slice(0, -1).join(", ")} and ${done[done.length - 1]}` : done[0];
  return `Installing stopped at ${at.label}. ${list[0].toUpperCase()}${list.slice(1)} ${done.length > 1 ? "are" : "is"} in place; nothing else changed.`;
}

/** The last log lines and the error, for the stop box. */
export function logTail(run: ProvisionRun): string {
  const ls = lines(run).slice(-2);
  if (run.error && !ls.includes(run.error)) ls.push(run.error);
  return ls.join("\n");
}

/** What Toft left on a server that joined, for Remove server's confirm:
 *  the agent, then what the install got to. Empty for a waiting server. */
export function leftOnServer(s: Pick<Server, "connection" | "agent">, run: ProvisionRun | null | undefined): string[] {
  const left = s.connection === "agent" && s.agent?.key_fingerprint ? ["the agent (toft-agent.service)"] : [];
  if (s.connection === "ssh") left.push("the key you added for root");
  if (!run) return left;
  const outcome = !run.finished_at ? "running" : run.ok ? "ok" : "failed";
  const NAMES: Record<string, string> = { docker: "Docker", networks: "its networks", traefik: "Traefik", firewall: "the firewall" };
  for (const st of stagesOf(lines(run), outcome)) if (st.state === "done" && NAMES[st.id]) left.push(NAMES[st.id]);
  return left;
}
