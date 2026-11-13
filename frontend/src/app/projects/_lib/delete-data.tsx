import { useQuery } from "@tanstack/react-query";
import {
  serviceService,
  type DataVolume,
  type ServiceData,
} from "@/lib/api/service.service";
import { formatBytes } from "@/lib/api/sharedvolume.service";

/** "pg-data · 2.3 GiB", "uptime_data · size unknown, its compose stack's";
 *  "reading its size…" while the sized read runs. */
export function volumeText(v: DataVolume, sizing = false): string {
  const size =
    v.bytes >= 0
      ? formatBytes(v.bytes)
      : sizing
        ? "reading its size…"
        : "size unknown";
  return `${v.name} · ${size}${v.from === "compose" ? ", its compose stack's" : ""}`;
}

/** True when the delete would remove data, or what it would remove is unknown. */
export function deletesData(data: ServiceData[] | undefined): boolean {
  return (data ?? []).some((d) => !d.read || d.volumes.length > 0);
}

/**
 * What deleting a service, an environment or a project removes. Two reads:
 * the names at once (?sizes=false), then the sizes, which can take up to
 * the server's 10 s on big volumes; the list never waits on them.
 */
export function useDeleteData(
  scope: "services" | "environments" | "projects",
  id: string,
  enabled: boolean,
) {
  const quick = useQuery({
    queryKey: ["delete-data", scope, id, "names"],
    queryFn: () => serviceService.data(scope, id, false),
    enabled,
    staleTime: 0,
  });
  const sized = useQuery({
    queryKey: ["delete-data", scope, id],
    queryFn: () => serviceService.data(scope, id),
    enabled,
    staleTime: 0,
  });
  return {
    data: sized.data ?? quick.data,
    sizing: !sized.data && !sized.isError,
    failed: !sized.data && !quick.data && sized.isError && quick.isError,
  };
}

/**
 * The volumes a delete removes, each with its size, as GET …/data names
 * them. perService prefixes each with its service (environment, project).
 */
export function DeleteData({
  data,
  sizing,
  failed,
  perService,
}: {
  data?: ServiceData[];
  sizing?: boolean;
  failed?: boolean;
  perService?: boolean;
}) {
  if (failed)
    return (
      <span className="text-status-failed-fg">
        Couldn’t read its volumes. Every volume of it is deleted.
      </span>
    );
  if (!data) return <span className="text-muted">Reading its volumes…</span>;
  // Services that couldn't be read for the same reason share one line.
  const unread = new Map<string, string[]>();
  for (const d of data.filter((d) => !d.read)) {
    const why = d.reason || `couldn’t read ${d.server_name || "the panel host"}`;
    unread.set(why, [...(unread.get(why) ?? []), d.service_name]);
  }
  const rows = [
    ...data.flatMap((d) =>
      d.volumes.map((v) => ({
        key: `${d.service_id}/${v.name}`,
        text: (perService ? `${d.service_name}: ` : "") + volumeText(v, sizing),
      })),
    ),
    ...[...unread].map(([why, names]) => ({
      key: `unread/${why}`,
      text: `${perService ? `${names.join(", ")}: ` : ""}${why}; any compose volumes go too`,
    })),
  ];
  if (rows.length === 0) return <>none: no volumes</>;
  return (
    <ul className="flex flex-col gap-0.5">
      {rows.map((r) => (
        <li
          key={r.key}
          className="break-words font-mono text-xs text-status-failed-fg"
        >
          {r.text}
        </li>
      ))}
    </ul>
  );
}
