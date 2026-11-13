import type { BackupDestination, CapacitySummary, DiskReport, FitResult, Server, ServerShare, WorkspaceAllocation } from "@/lib/api/server.service";
import { formatDay, formatMiB } from "@/lib/api/resources.service";
import { ago } from "@/lib/time";
import { possessive } from "@/lib/utils";
import { gb, safeRows } from "./disk-view";

// What the Servers page computes from the capacity figures the API gives it.
// Whether something fits is never decided here: the API's fit check is the
// deploy gate's own arithmetic.

export type Resource = "cpu" | "memory";

/** A server is online once it is active and provisioned. */
export const isOnline = (s: Pick<Server, "status" | "provisioned_at">) => s.status === "active" && !!s.provisioned_at;

/** One stretch of a capacity bar: what a workspace's services reserve, or
 *  (setAside) the part of a share its services do not reserve yet. */
export interface Segment {
    orgId: string;
    pct: number;
    setAside: boolean;
}

const reserved = (w: WorkspaceAllocation | undefined, r: Resource) =>
    !w ? 0 : r === "cpu" ? w.cpu_cores : w.memory_mb;

/** The bar of one resource on a server the workspace owns, as percentages
 *  of the server: its own reservations, then each share's, then the unused
 *  rest of each share. Never more than 100 in all. */
export function ownerSegments(
    total: number,
    ownOrgId: string,
    workspaces: WorkspaceAllocation[],
    shares: ServerShare[],
    r: Resource,
): Segment[] {
    if (total <= 0) return [];
    const out: Segment[] = [];
    let left = 100;
    const push = (orgId: string, amount: number, setAside: boolean) => {
        const pct = Math.min(left, (amount / total) * 100);
        if (pct > 0) {
            out.push({ orgId, pct, setAside });
            left -= pct;
        }
    };
    const find = (orgId: string) => workspaces.find((w) => w.org_id === orgId);
    push(ownOrgId, reserved(find(ownOrgId), r), false);
    for (const s of shares) {
        const used = reserved(find(s.org_id), r);
        push(s.org_id, used, false);
        push(s.org_id, (r === "cpu" ? s.cpu_cores : s.memory_mb) - used, true);
    }
    // Services of a workspace whose share was removed still reserve.
    for (const w of workspaces) {
        if (w.org_id !== ownOrgId && !shares.some((s) => s.org_id === w.org_id)) push(w.org_id, reserved(w, r), false);
    }
    return out;
}

/** The bar of ownerSegments in words, when another workspace is on the
 *  server: this workspace's reservations, each share's size and how much of
 *  it is reserved, and services left from a removed share. With what is
 *  free they add up to the server. Empty when only this workspace is there. */
export function ownerParts(
    ownOrgId: string,
    ownName: string,
    workspaces: WorkspaceAllocation[],
    shares: ServerShare[],
    r: Resource,
): string[] {
    const amount = (n: number) => (r === "cpu" ? `${cores(n)} vCPU` : `${gib(n)} GiB`);
    const find = (orgId: string) => workspaces.find((w) => w.org_id === orgId);
    const unshared = workspaces.filter((w) => w.org_id !== ownOrgId && !shares.some((s) => s.org_id === w.org_id));
    if (shares.length === 0 && unshared.length === 0) return [];
    const out: string[] = [];
    const own = reserved(find(ownOrgId), r);
    if (own > 0) out.push(`${ownName} ${amount(own)}`);
    for (const s of shares) {
        const used = reserved(find(s.org_id), r);
        const size = amount(r === "cpu" ? s.cpu_cores : s.memory_mb);
        out.push(`${possessive(s.org_name)} share ${size}${used > 0 ? `, ${r === "cpu" ? cores(used) : gib(used)} of it reserved` : ""}`);
    }
    for (const w of unshared) {
        if (reserved(w, r) > 0) out.push(`${w.org_name} ${amount(reserved(w, r))}`);
    }
    return out;
}

/** What the page knows of one server, for the Capacity card. */
export interface ServerFigures {
    name: string;
    summary?: CapacitySummary;
    /** The room this workspace has left (the fit check with no limits). */
    room?: FitResult;
}

/** "the firewall (ufw, fail2ban) and the host firewall for app containers". */
export function missingList(warnings: string[]): string {
    return warnings.length > 1 ? `${warnings.slice(0, -1).join(", ")} and ${warnings[warnings.length - 1]}` : warnings[0] ?? "";
}

export interface FleetTotals {
    cpuReserved: number;
    cpuTotal: number;
    /** Cores in use now, on the servers whose live figures were read. */
    cpuLive: number;
    memReservedMB: number;
    memTotalMB: number;
    memLiveMB: number;
    /** How many servers' live figures were read (a failed read gives none). */
    liveRead: number;
    /** The server with the most memory free for this workspace. */
    largest: { server: string; cpu: number; memMB: number } | null;
}

export const DISK_WARN_PCT = 85;
/** Past this share of its room reserved, a server needs attention: the next
 *  default-sized service may not fit (review, /servers stat strip). */
export const LOW_ROOM_PCT = 80;
export const CPU_WAIT_WARN_PCT = 10;

/** Whether the server's live figures (CPU and memory in use, disk) were
 *  read: the API leaves the disk out when its SSH read failed. */
export function liveRead(s: CapacitySummary | undefined): boolean {
    return !!s && !s.share && !!s.disk_total_mb;
}

export function diskPct(s: CapacitySummary | undefined): number | null {
    return s?.disk_total_mb ? Math.round((s.disk_used_mb ?? 0) / s.disk_total_mb * 100) : null;
}

/** The chip of a server shared with other workspaces: their names up to
 *  two, a count past that, and the full list for its title. */
export function sharedWith(names: string[]): { text: string; full: string } {
    const full = `Shared with ${names.join(", ")}`;
    return { text: names.length > 2 ? `Shared with ${names.length} workspaces` : full, full };
}

/** The summary's disk figures are df -BM's MiB; they read in GB, as the
 *  Disk card's bytes and Docker do. */
export const diskGB = (mb: number) => gb(mb * 1048576);

/** "137 GB of 160 GB", as the Disk card says it. */
export const diskUsedLabel = (usedMB: number, totalMB: number) => `${diskGB(usedMB)} of ${diskGB(totalMB)}`;

export function fleetTotals(servers: ServerFigures[]): FleetTotals {
    const t: FleetTotals = { cpuReserved: 0, cpuTotal: 0, cpuLive: 0, memReservedMB: 0, memTotalMB: 0, memLiveMB: 0, liveRead: 0, largest: null };
    for (const s of servers) {
        const sum = s.summary;
        if (sum?.capacity_discovered) {
            t.cpuReserved += sum.allocated_cpu_cores;
            t.cpuTotal += sum.effective_cpu_cores;
            t.memReservedMB += sum.allocated_memory_mb;
            t.memTotalMB += sum.effective_memory_mb;
            if (liveRead(sum)) {
                t.liveRead++;
                t.cpuLive += (sum.live_cpu_percent / 100) * sum.effective_cpu_cores;
                t.memLiveMB += sum.live_memory_mb;
            }
        }
        const room = s.room;
        if (room?.capacity_known) {
            const memMB = Math.max(0, room.effective_memory_mb - room.allocated_memory_mb);
            if (!t.largest || memMB > t.largest.memMB) {
                t.largest = { server: s.name, cpu: Math.max(0, room.effective_cpu_cores - room.allocated_cpu_cores), memMB };
            }
        }
    }
    return t;
}

/** One thing wrong with a server, in words, for its block on Servers: a
 *  problem (red), a warning (amber) or a suggestion. */
export interface Problem {
    kind: "backup" | "no_backup" | "provision" | "disk" | "cpu_wait" | "room" | "suggestions";
    level: "problem" | "warning" | "info";
    text: string;
}

/** What the page knows of one server it owns, for its problems. */
export interface ProblemFigures {
    online: boolean;
    summary?: CapacitySummary;
    room?: FitResult;
    /** Memory limits suggested for its services, and what they would free. */
    suggestions: number;
    freesMB?: number;
    cpuWait?: { day: string; avg_pct: number } | null;
    provisionWarnings?: string[];
    /** Its backup destination; null when none is set up, undefined unread. */
    backup?: BackupDestination | null;
    /** The Inbox's no_backup item for it, while one holds. */
    noBackup?: string;
    disk?: DiskReport;
    now: number;
}

const HOUR = 3_600_000;

/** A server's problems, worst first. Being offline isn't one: its state says so. */
export function serverProblems(f: ProblemFigures): Problem[] {
    const out: Problem[] = [];
    const b = f.backup;
    if (b && b.last_ok === false && b.last_run_at) {
        const hours = b.last_ok_at ? Math.round((f.now - Date.parse(b.last_ok_at)) / HOUR) : null;
        const good = hours === null ? "There is no good backup yet." : `The newest good backup is ${hours === 1 ? "1 hour" : `${hours} hours`} old.`;
        out.push({ kind: "backup", level: "problem", text: `The last backup failed ${ago(b.last_run_at, f.now)}: ${b.last_error || "see the server’s /var/lib/toft/backup/last.json"}. ${good}` });
    }
    if (b === null && f.noBackup) {
        out.push({ kind: "no_backup", level: "warning", text: `${f.noBackup}: if this server’s disk is lost, the data goes with it.` });
    }
    if (f.provisionWarnings?.length) {
        out.push({ kind: "provision", level: "warning", text: `Provisioned without ${missingList(f.provisionWarnings)}: its log says why.` });
    }
    const disk = diskPct(f.summary);
    if (disk !== null && disk >= DISK_WARN_PCT) {
        const safe = f.disk ? safeRows(f.disk) : [];
        const n = safe.reduce((a, r) => a + (f.disk!.classes.find((c) => c.kind === r.kind)?.count ?? 0), 0);
        const bytes = safe.reduce((a, r) => a + r.bytes, 0);
        out.push({ kind: "disk", level: "warning", text: `Disk ${disk}% full.${n > 0 ? ` ${n} ${n === 1 ? "image" : "images"} no service uses take about ${gb(bytes)}.` : ""}` });
    }
    if (f.cpuWait && f.cpuWait.avg_pct > CPU_WAIT_WARN_PCT) {
        out.push({ kind: "cpu_wait", level: "warning", text: `Services waited for CPU ${Math.round(f.cpuWait.avg_pct)}% of the time on ${formatDay(f.cpuWait.day)}.` });
    }
    const room = f.room;
    if (room?.capacity_known && f.online) {
        const pct = (used: number, all: number) => (all > 0 ? (used / all) * 100 : 0);
        const mem = pct(room.allocated_memory_mb, room.effective_memory_mb);
        const cpu = pct(room.allocated_cpu_cores, room.effective_cpu_cores);
        if (mem > LOW_ROOM_PCT) {
            const free = Math.max(0, Math.round(room.effective_memory_mb - room.allocated_memory_mb));
            out.push({ kind: "room", level: "warning", text: `Memory ${Math.round(mem)}% reserved: ${formatMiB(free)} left for this workspace.` });
        } else if (cpu > LOW_ROOM_PCT) {
            const free = cores(Math.max(0, room.effective_cpu_cores - room.allocated_cpu_cores));
            out.push({ kind: "room", level: "warning", text: `CPU ${Math.round(cpu)}% reserved: ${free} vCPU left for this workspace.` });
        }
    }
    if (f.suggestions > 0) {
        const frees = f.freesMB ? `, frees ${formatMiB(f.freesMB)}` : "";
        out.push({ kind: "suggestions", level: "info", text: `${f.suggestions} memory ${f.suggestions === 1 ? "limit" : "limits"} suggested${frees}.` });
    }
    return out;
}

/** "a", "a and b", "a, b and c". */
const and = (items: string[]) => (items.length < 2 ? items.join("") : `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`);

/** What runs on a server, this workspace's projects first: "Runs storefront
 *  and billing-api (7 services), and acme’s shop (2 services)". */
export function runsLine(ownOrgId: string, workspaces: WorkspaceAllocation[]): string {
    const sorted = [...workspaces].sort((a, b) => Number(b.org_id === ownOrgId) - Number(a.org_id === ownOrgId));
    if (sorted.length === 0) return "Nothing runs here yet";
    const parts = sorted.map((w) => {
        const n = `${w.services} ${w.services === 1 ? "service" : "services"}`;
        const what = w.projects.length ? `${and(w.projects)} (${n})` : n;
        return w.org_id === ownOrgId ? what : `${possessive(w.org_name)} ${what}`;
    });
    return `Runs ${parts.join(", and ")}`;
}

/** MiB from what people type for memory: "512m", "2g", "1.5 GiB"; a bare
 *  number is MiB. Null when it is not one of those. */
export function parseMemoryMB(input: string): number | null {
    const m = input.trim().match(/^(\d+(?:\.\d+)?)\s*(m|mb|mib|g|gb|gib)?$/i);
    if (!m) return null;
    const n = parseFloat(m[1]);
    return Math.round(m[2]?.toLowerCase().startsWith("g") ? n * 1024 : n);
}

/** Cores from what people type: "1", "0.5", "2 vCPU". Null otherwise. */
export function parseCores(input: string): number | null {
    const m = input.trim().match(/^(\d+(?:\.\d+)?)\s*(v?cpus?|cores?)?$/i);
    return m ? parseFloat(m[1]) : null;
}

/** "7.6" (GiB, one decimal, no trailing .0). */
export function gib(mb: number): string {
    return String(Math.round((mb / 1024) * 10) / 10);
}

/** "0.5", "4" (cores, at most two decimals). */
export function cores(c: number): string {
    return String(Math.round(c * 100) / 100);
}
