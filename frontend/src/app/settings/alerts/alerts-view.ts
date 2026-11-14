// Settings → Alerts' wording (board: Alerts), kept apart from the page so it
// is tested. The rules are the backend's (internal/alert/rules.go).
import type {
  AlertChannel,
  AlertKind,
  RecentAlert,
} from "@/lib/api/alert.service";
import { serviceHref } from "@/components/home/home";
import { formatDateTime } from "@/lib/utils";

export const KIND_LABEL: Record<AlertKind, string> = {
  deploy_failed: "Deploy failed",
  oom_killed: "Killed for memory",
  service_down: "Service down",
  service_restarting: "Restarting in a loop",
  service_unhealthy: "Health check failing",
  container_exited: "Container exited with an error",
  server_unreachable: "Server unreachable",
  disk_full: "Disk almost full",
  volume_full: "Shared volume almost full",
  backup_failed: "Backup failing",
  panel_container: "Panel container not healthy",
  panel_certificate: "Panel certificate not renewing",
  panel_upgrade_failed: "Panel upgrade failed",
  mirror_failing: "Mirror failing",
  agent_offline: "Agent offline",
  agent_frozen: "Server kept its own state",
  security_updates: "Security updates waiting",
  reboot_required: "Reboot required",
  docker_advisory: "Docker affected by an advisory",
};

/** What alerts, in the board's order: when each fires and resolves. */
/** The disk alert's default threshold (internal/disk DefaultAlertPercent);
 *  each server's disk policy can change it (D-135). */
export const DISK_ALERT_PERCENT = 85;

export const RULES: { kind: AlertKind; fires: string; resolves: string }[] = [
  {
    kind: "deploy_failed",
    fires: "Any failed deploy, including a refused git push.",
    resolves: "One message; nothing to resolve.",
  },
  {
    kind: "service_down",
    fires: "A running service has no running container for 2 minutes.",
    resolves: "Resolves when it has one again for 3 minutes.",
  },
  {
    kind: "service_restarting",
    fires: "A container keeps crashing and restarting for 2 minutes.",
    resolves: "Resolves after 3 minutes without a restart.",
  },
  {
    kind: "service_unhealthy",
    fires: "A container reports unhealthy for 2 minutes.",
    resolves: "Resolves after 3 healthy minutes.",
  },
  {
    kind: "container_exited",
    fires: "A container, such as a compose job, exited with a non-zero code.",
    resolves: "Resolves when none has for 3 minutes.",
  },
  {
    kind: "oom_killed",
    fires: "A container was killed for memory: it reached its limit, or its server ran out.",
    resolves: "One message; nothing to resolve.",
  },
  {
    kind: "server_unreachable",
    fires: "A server running services can’t be reached for 2 minutes.",
    resolves: "Resolves as soon as it answers.",
  },
  {
    kind: "agent_offline",
    fires: "A server’s agent hasn’t connected for 5 minutes. Its apps keep serving.",
    resolves: "Resolves when it connects again.",
  },
  {
    kind: "agent_frozen",
    fires: "The panel was restored from a backup and a server’s agent kept its own state. Deploys there wait for a choice.",
    resolves: "Resolves when someone adopts the server’s state or uses the panel’s.",
  },
  {
    kind: "disk_full",
    fires: `A server’s / or /var/lib/docker is ${DISK_ALERT_PERCENT}% full, or as full as the server’s disk policy says.`,
    resolves: "Resolves 5 points under.",
  },
  {
    kind: "volume_full",
    fires:
      "A shared volume reaches 80% of its size limit. Measured every 15 minutes.",
    resolves: "Resolves under 75%.",
  },
  {
    kind: "backup_failed",
    fires:
      "A server’s last nightly backup failed, or none has succeeded for 26 hours.",
    resolves: "Resolves at the next successful backup.",
  },
  {
    kind: "security_updates",
    fires:
      "Security updates wait on a server after unattended-upgrades ran, or unattended-upgrades is stopped, failing or hasn’t run for 2 days. Read every 6 hours.",
    resolves: "Resolves when none wait.",
  },
  {
    kind: "reboot_required",
    fires: "An update on a server asks for a reboot. Read every 6 hours.",
    resolves: "Resolves once the server has rebooted.",
  },
  {
    kind: "docker_advisory",
    fires: "A security advisory Docker published affects the engine a server runs. Read every 6 hours.",
    resolves: "Resolves when Docker is updated past it.",
  },
  {
    kind: "mirror_failing",
    fires: "A repository’s import, pull mirror or push mirror failed 3 times in a row.",
    resolves: "Resolves at its next successful sync.",
  },
  {
    kind: "panel_container",
    fires:
      "One of the panel’s own containers stops, keeps restarting or fails its health check for 2 minutes. Operators only.",
    resolves: "Resolves after 3 healthy minutes.",
  },
  {
    kind: "panel_certificate",
    fires:
      "The panel’s certificate has under 14 days left, or Let’s Encrypt never issued one. Operators only.",
    resolves: "Resolves when it is renewed.",
  },
  {
    kind: "panel_upgrade_failed",
    fires: "An upgrade of the panel rolled back or stopped. Operators only.",
    resolves: "One message; nothing to resolve.",
  },
];

/** "4 min", "2 h", "3 d"; at least a minute. */
export function span(ms: number): string {
  const min = Math.max(1, Math.round(ms / 60_000));
  if (min < 60) return `${min} min`;
  const h = Math.round(min / 60);
  if (h < 48) return `${h} h`;
  return `${Math.round(h / 24)} d`;
}

/** A time today as "14:02", an older one as "2026-10-12 03:12". */
export function clockOrDate(iso: string, now: number): string {
  const full = formatDateTime(iso);
  return full.slice(0, 10) === formatDateTime(now).slice(0, 10)
    ? full.slice(11)
    : full;
}

const host = (url: string) => {
  try {
    return new URL(url).host;
  } catch {
    return "the receiver";
  }
};

export type Tone = "ok" | "warn" | "fail" | "off";

/** The webhook's status pill and the line beside it. */
export function channelStatus(
  ch: AlertChannel | null,
  now: number,
): { label: string; tone: Tone; note: string; failed: boolean } {
  if (!ch)
    return {
      label: "Off",
      tone: "off",
      note: "No webhook: nothing is sent",
      failed: false,
    };
  const label = ch.enabled ? "On" : "Paused";
  const tone: Tone = ch.enabled ? "ok" : "warn";
  if (!ch.last_delivery_at)
    return {
      label,
      tone,
      note: ch.enabled ? "Nothing sent yet" : "Paused: nothing is sent",
      failed: false,
    };
  const when = clockOrDate(ch.last_delivery_at, now);
  const to = host(ch.webhook_url);
  if (!ch.last_delivery_error) {
    return {
      label,
      tone,
      note: `Last delivery ${when} · ${to} answered ${ch.last_delivery_status}`,
      failed: false,
    };
  }
  const answer = ch.last_delivery_status
    ? `${to} answered ${ch.last_delivery_status}`
    : `${to} did not answer: ${ch.last_delivery_error}`;
  return {
    label,
    tone: "fail",
    note: `Last delivery ${when} failed · ${answer}`,
    failed: true,
  };
}

/** A recent alert's Now column: Open for…, Resolved after…, or one message. */
export function alertState(
  a: RecentAlert,
  now: number,
): { label: string; note: string; tone: Tone } {
  // Not "Sent": whether the webhook was on when it fired isn't recorded.
  if (a.one_off)
    return { label: "One message", note: "nothing to resolve", tone: "off" };
  const fired = new Date(a.fired_at).getTime();
  if (!a.resolved_at)
    return { label: "Open", note: `for ${span(now - fired)}`, tone: "warn" };
  return {
    label: "Resolved",
    note: `after ${span(new Date(a.resolved_at).getTime() - fired)}`,
    tone: "ok",
  };
}

/** What an alert is on, the line under it, and where it opens. */
export function alertSubject(a: RecentAlert): {
  name: string;
  where: string;
  href?: string;
} {
  if (a.subject === "control-plane")
    return {
      name: "panel",
      where: "control-plane server",
      href: a.kind.startsWith("panel_") ? "/instance" : undefined,
    };
  switch (a.kind) {
    case "server_unreachable":
    case "agent_offline":
    case "agent_frozen":
    case "disk_full":
    case "backup_failed":
      return {
        name: a.subject_name || "a removed server",
        where: "server",
        href: a.subject_name ? `/servers/${a.subject}` : undefined,
      };
    case "mirror_failing":
      return {
        name: a.project_name ? `${a.project_name} / ${a.subject_name}` : a.subject_name || "a removed mirror",
        where: "repository mirror",
        href: a.subject_name && a.project_id ? `/projects/${a.project_id}/code/${encodeURIComponent(a.subject_name)}/settings` : undefined,
      };
    case "volume_full":
      return {
        name: a.subject_name || "a deleted volume",
        where: a.server_name
          ? `shared volume on ${a.server_name}`
          : "shared volume",
        href: "/settings/storage",
      };
    default: {
      const name = a.project_name
        ? `${a.project_name} / ${a.subject_name}`
        : a.subject_name || "a deleted service";
      const tab = a.kind === "deploy_failed" ? "?tab=deployments" : "";
      return {
        name,
        where: a.server_name || "panel host",
        href: a.subject_name
          ? `${serviceHref(a.project_id ?? "", a.subject)}${tab}`
          : undefined,
      };
    }
  }
}
