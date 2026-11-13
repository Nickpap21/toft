import { describe, expect, it } from "vitest";
import type { CapacitySummary, FitResult } from "@/lib/api/server.service";
import type { BackupDestination, DiskReport } from "@/lib/api/server.service";
import { diskGB, diskUsedLabel, fleetTotals, ownerParts, ownerSegments, parseCores, parseMemoryMB, runsLine, serverProblems, sharedWith } from "./capacity";
import { gb } from "./disk-view";

const ws = (org_id: string, cpu_cores: number, memory_mb: number) =>
    ({ org_id, org_name: org_id, projects: [], services: 1, cpu_cores, memory_mb });
const share = (org_id: string, cpu_cores: number, memory_mb: number) =>
    ({ org_id, org_name: org_id, cpu_cores, memory_mb, created_at: "" });

describe("ownerSegments", () => {
    it("puts the owner's reservations first, then each share's, then what the share leaves unused", () => {
        const segs = ownerSegments(8000, "own", [ws("own", 0, 2000), ws("guest", 0, 1000)], [share("guest", 1, 1600)], "memory");
        expect(segs).toEqual([
            { orgId: "own", pct: 25, setAside: false },
            { orgId: "guest", pct: 12.5, setAside: false },
            { orgId: "guest", pct: 7.5, setAside: true },
        ]);
    });

    it("never goes past the whole bar, and a guest over its share sets nothing aside", () => {
        const segs = ownerSegments(4, "own", [ws("own", 3, 0), ws("guest", 2, 0)], [share("guest", 1, 0)], "cpu");
        expect(segs.reduce((a, s) => a + s.pct, 0)).toBe(100);
        expect(segs.some((s) => s.setAside)).toBe(false);
    });

    it("still shows the services of a workspace whose share was removed", () => {
        expect(ownerSegments(10, "own", [ws("gone", 0, 5)], [], "memory")).toEqual([{ orgId: "gone", pct: 50, setAside: false }]);
    });
});

describe("ownerParts", () => {
    const workspaces = [ws("own", 0, 3968), ws("guest", 0, 1458)];
    const shares = [share("guest", 1, 1536)];

    it("names each share's size and how much of it is reserved", () => {
        expect(ownerParts("own", "Northwind", workspaces, shares, "memory")).toEqual(["Northwind 3.9 GiB", "guest’s share 1.5 GiB, 1.4 of it reserved"]);
        expect(ownerParts("own", "Northwind", workspaces, shares, "cpu")).toEqual(["guest’s share 1 vCPU"]);
    });

    it("says nothing when only this workspace is on the server", () => {
        expect(ownerParts("own", "Northwind", [ws("own", 1, 2048)], [], "memory")).toEqual([]);
    });

    it("names the services of a workspace whose share was removed", () => {
        expect(ownerParts("own", "Northwind", [ws("own", 0, 1024), ws("gone", 0, 512)], [], "memory")).toEqual(["Northwind 1 GiB", "gone 0.5 GiB"]);
    });
});

const summary = (over: Partial<CapacitySummary>): CapacitySummary => ({
    server_id: "s", effective_cpu_cores: 4, effective_memory_mb: 8192, allocated_cpu_cores: 1, allocated_memory_mb: 2048,
    live_cpu_percent: 25, live_memory_mb: 3000, capacity_discovered: true, share: false, disk_used_mb: 10, disk_total_mb: 100, ...over,
});
const room = (over: Partial<FitResult>): FitResult => ({
    capacity_known: true, fits: true, cpu_ok: true, memory_ok: true, share: false,
    effective_cpu_cores: 3, effective_memory_mb: 6144, allocated_cpu_cores: 1, allocated_memory_mb: 2048, ...over,
});

describe("fleetTotals", () => {
    it("adds up reservations, counts live use only from whole servers, and picks the most free memory", () => {
        const t = fleetTotals([
            { name: "a", summary: summary({}), room: room({}) },
            { name: "b", summary: summary({ share: true, effective_cpu_cores: 1, effective_memory_mb: 1536, allocated_memory_mb: 1330, live_cpu_percent: 0, live_memory_mb: 0 }), room: room({ share: true, effective_memory_mb: 1536, allocated_memory_mb: 1330 }) },
        ]);
        expect(t.cpuTotal).toBe(5);
        expect(t.memReservedMB).toBe(3378);
        expect(t.cpuLive).toBe(1);
        expect(t.memLiveMB).toBe(3000);
        expect(t.largest).toEqual({ server: "a", cpu: 2, memMB: 4096 });
    });

    it("counts no live use from a server it could not read", () => {
        const t = fleetTotals([{ name: "a", summary: summary({ disk_total_mb: undefined }) }]);
        expect(t.liveRead).toBe(0);
        expect(t.cpuLive).toBe(0);
        expect(t.cpuTotal).toBe(4);
    });

    it("leaves out servers whose capacity was never scanned", () => {
        const t = fleetTotals([{ name: "a", summary: summary({ capacity_discovered: false }), room: room({ capacity_known: false }) }]);
        expect(t.cpuTotal).toBe(0);
        expect(t.largest).toBeNull();
    });
});

const now = Date.parse("2026-10-14T12:00:00Z");
const backup: BackupDestination = {
    endpoint: "https://fsn1.your-objectstorage.com", bucket: "b", access_key: "AK", schedule: "03:00",
    repository: "s3:x", last_run_at: "2026-10-14T03:04:00Z", last_ok: true, last_ok_at: "2026-10-14T03:04:00Z",
    last_bytes: null, last_error: "", created_at: "2026-10-01T10:00:00Z",
};
const disk = (classes: Partial<DiskReport["classes"][number]>[]): DiskReport =>
    ({ classes: classes.map((c) => ({ removable: true, automatic: false, count: 0, bytes: 0, examples: [], ...c })), volumes_in_use: { count: 9, bytes: 0 } }) as unknown as DiskReport;

describe("serverProblems", () => {
    const fine = { online: true, suggestions: 0, summary: summary({}), room: room({}), backup, now };

    it("says nothing about a server with nothing wrong", () => {
        expect(serverProblems(fine)).toEqual([]);
    });

    it("puts a failed backup first, with its reason and the newest good backup's age", () => {
        const failed = { ...backup, last_run_at: "2026-10-14T03:04:00Z", last_ok: false, last_ok_at: "2026-10-13T01:00:00Z", last_error: "it couldn’t create its S3 repository" };
        expect(serverProblems({ ...fine, backup: failed, summary: summary({ disk_used_mb: 86, disk_total_mb: 100 }) })).toEqual([
            { kind: "backup", level: "problem", text: "The last backup failed 8h ago: it couldn’t create its S3 repository. The newest good backup is 35 hours old." },
            { kind: "disk", level: "warning", text: "Disk 86% full." },
        ]);
        expect(serverProblems({ ...fine, backup: { ...failed, last_ok_at: null, last_error: "" } })[0].text).toBe(
            "The last backup failed 8h ago: see the server’s /var/lib/toft/backup/last.json. There is no good backup yet.",
        );
    });

    it("names what a full disk could free: the images no service uses", () => {
        const report = disk([{ kind: "in_use", removable: false, count: 20, bytes: 9e9 }, { kind: "untagged", count: 30, bytes: 20e9 }, { kind: "unused_pulled", count: 11, bytes: 8e9 }]);
        expect(serverProblems({ ...fine, summary: summary({ disk_used_mb: 86, disk_total_mb: 100 }), disk: report })).toEqual([
            { kind: "disk", level: "warning", text: "Disk 86% full. 41 images no service uses take about 28 GB." },
        ]);
        expect(serverProblems({ ...fine, summary: summary({ disk_used_mb: 84, disk_total_mb: 100 }), disk: report })).toEqual([]);
    });

    it("says nothing backs up the data while the Inbox says so and no backup is set up", () => {
        expect(serverProblems({ ...fine, backup: null, noBackup: "Nothing backs up 2 databases and 3 volumes" })).toEqual([
            { kind: "no_backup", level: "warning", text: "Nothing backs up 2 databases and 3 volumes: if this server’s disk is lost, the data goes with it." },
        ]);
        expect(serverProblems({ ...fine, noBackup: "Nothing backs up 2 databases" })).toEqual([]);
    });

    it("lists provisioning gaps, CPU waiting, low room, then suggestions", () => {
        expect(
            serverProblems({
                ...fine,
                suggestions: 3,
                freesMB: 1536,
                cpuWait: { day: "2026-10-13", avg_pct: 12.4 },
                provisionWarnings: ["the firewall (ufw, fail2ban)", "the host firewall for app containers"],
                room: room({ share: true, effective_memory_mb: 1536, allocated_memory_mb: 1330 }),
            }).map((p) => `${p.level} ${p.kind}: ${p.text}`),
        ).toEqual([
            "warning provision: Provisioned without the firewall (ufw, fail2ban) and the host firewall for app containers: its log says why.",
            "warning cpu_wait: Services waited for CPU 12% of the time on 13 Oct.",
            "warning room: Memory 87% reserved: 206 MiB left for this workspace.",
            "info suggestions: 3 memory limits suggested, frees 1.5 GiB.",
        ]);
        expect(serverProblems({ ...fine, cpuWait: { day: "2026-10-13", avg_pct: 9 } })).toEqual([]);
    });

    it("reads no room on a server that isn't online", () => {
        expect(serverProblems({ ...fine, online: false, room: room({ allocated_memory_mb: 6000 }) })).toEqual([]);
    });
});

describe("runsLine", () => {
    const w = (org_id: string, org_name: string, projects: string[], services: number) => ({ org_id, org_name, projects, services, cpu_cores: 0, memory_mb: 0 });

    it("names this workspace's projects first, then the others'", () => {
        expect(runsLine("own", [w("acme", "acme", ["shop"], 2), w("own", "founders", ["storefront", "billing-api", "handbook"], 7)])).toBe(
            "Runs storefront, billing-api and handbook (7 services), and acme’s shop (2 services)",
        );
        expect(runsLine("own", [w("own", "founders", ["billing-api"], 1)])).toBe("Runs billing-api (1 service)");
        expect(runsLine("own", [w("own", "founders", [], 4)])).toBe("Runs 4 services");
    });

    it("says when nothing runs there", () => {
        expect(runsLine("own", [])).toBe("Nothing runs here yet");
    });
});

describe("parsing what people type", () => {
    it("reads memory in MiB", () => {
        expect(parseMemoryMB("512m")).toBe(512);
        expect(parseMemoryMB("2g")).toBe(2048);
        expect(parseMemoryMB("1.5 GiB")).toBe(1536);
        expect(parseMemoryMB("300")).toBe(300);
        expect(parseMemoryMB("lots")).toBeNull();
        expect(parseMemoryMB("-1g")).toBeNull();
    });

    it("reads cores", () => {
        expect(parseCores("1 vCPU")).toBe(1);
        expect(parseCores("0.5")).toBe(0.5);
        expect(parseCores("two")).toBeNull();
    });
});

describe("disk figures", () => {
    // df -BM's MiB, as the capacity summary carries them, and the disk report's bytes of the same disk.
    const usedMB = 131_072;
    const totalMB = 152_588;

    it("read the same on Overview, the Servers list and Maintenance", () => {
        // Overview and the list show diskUsedLabel; Maintenance's Disk card shows gb(used) of gb(size).
        expect(diskUsedLabel(usedMB, totalMB)).toBe("137 GB of 160 GB");
        expect(diskUsedLabel(usedMB, totalMB)).toBe(`${gb(usedMB * 1048576)} of ${gb(totalMB * 1048576)}`);
        expect(diskGB(totalMB)).toBe("160 GB");
    });
});

describe("sharedWith", () => {
    it("names up to two workspaces, and counts more", () => {
        expect(sharedWith(["acme"])).toEqual({ text: "Shared with acme", full: "Shared with acme" });
        expect(sharedWith(["acme", "beta"]).text).toBe("Shared with acme, beta");
        expect(sharedWith(["acme", "beta", "gamma"])).toEqual({ text: "Shared with 3 workspaces", full: "Shared with acme, beta, gamma" });
    });
});
