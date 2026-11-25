"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@/lib/api/client";
import { serverService, type BuildDestination } from "@/lib/api/server.service";
import { useReauth } from "@/components/reauth";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { cn } from "@/lib/utils";
import { card, fieldInput, fieldLabel, help } from "@/components/settings/settings-frame";
import { btnSm, ghostSm } from "@/app/projects/_lib/ui";
import { settingsFoot, settingsHead, titleRow } from "@/app/servers/[id]/aside";
import { sentence } from "@/app/oauth/authorize/view";

const WHY = "A build destination opens a private address to every build on this server, so Toft asks for your password first.";

/** The password confirmation was cancelled: nothing to report. */
class Cancelled extends Error {}

/** An entry as it is written: 10.0.5.20:5000, or the address alone for every port. */
export const destinationText = (d: Pick<BuildDestination, "address" | "port">) => (d.port ? `${d.address}:${d.port}` : d.address);

/** What a refused change says, and the field it is about ("" for the list).
 *  Below 500 the API's sentence names the address and the reason; a 502 is
 *  the server not taking the list, so nothing was saved. */
export function destinationError(err: unknown, fallback: string): { field: string; text: string } {
  if (!(err instanceof ApiError)) return { field: "", text: fallback };
  const data = (err.body as { data?: { code?: string; field?: string } } | null)?.data;
  if (data?.code === "build_destinations_not_applied") {
    return { field: "", text: "The server didn’t take the list, so nothing was saved. Check that it is online, then try again." };
  }
  // Every refusal of one entry (build_destination_…) is about its address.
  const field = data?.field || (data?.code?.startsWith("build_destination_") ? "address" : "");
  return err.status < 500 ? { field, text: sentence(err.message) } : { field: "", text: fallback };
}

/**
 * The private addresses a server's builds may reach (D-170, boards
 * ReworkServerSettings and ReworkInstanceServer). serverId is null for the
 * panel's own server. Instance operators only: mount it for them alone.
 */
export function BuildDestinationsCard({ serverId }: { serverId: string | null }) {
  const queryClient = useQueryClient();
  const reauth = useReauth();
  const key = ["build-destinations", serverId];
  const read = useQuery({ queryKey: key, queryFn: () => serverService.listBuildDestinations(serverId), retry: false });
  const [address, setAddress] = useState("");
  const [port, setPort] = useState("");
  const [note, setNote] = useState("");
  const [refused, setRefused] = useState<{ field: string; text: string } | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const [closed, setClosed] = useState<string | null>(null);

  const add = useMutation({
    mutationFn: async () => {
      if (!(await reauth(WHY))) throw new Cancelled();
      return serverService.addBuildDestination(serverId, { address: address.trim(), port: port.trim() ? Number(port) : null, note: note.trim() });
    },
    onSuccess: async (d) => {
      await queryClient.invalidateQueries({ queryKey: key });
      setAddress("");
      setPort("");
      setNote("");
      toast.success(`Builds on this server now reach ${destinationText(d)}`);
    },
    onError: (err) => {
      if (!(err instanceof Cancelled)) refuse(destinationError(err, "The destination could not be added."));
    },
  });
  const remove = useMutation({
    mutationFn: async (d: BuildDestination) => {
      if (!(await reauth(WHY))) throw new Cancelled();
      await serverService.removeBuildDestination(serverId, d.id);
      return d;
    },
    onSuccess: async (d) => {
      await queryClient.invalidateQueries({ queryKey: key });
      setRemoving(null);
      toast.success(`Builds on this server no longer reach ${destinationText(d)}`);
      // The row is gone, and the focus with it: the form's first field takes it.
      document.getElementById("bd-address")?.focus();
    },
    onError: (err) => {
      if (!(err instanceof Cancelled)) toast.error(destinationError(err, "The destination could not be removed.").text);
    },
  });

  const refuse = (r: { field: string; text: string }) => {
    setRefused(r);
    document.getElementById(`bd-${r.field || "address"}`)?.focus();
  };
  const described = (field: string) => (refused && (refused.field === field || refused.field === "") ? "bd-error" : undefined);

  const list = read.data ?? [];
  const portOk = port.trim() === "" || (/^\d+$/.test(port.trim()) && Number(port) >= 1 && Number(port) <= 65535);
  return (
    <section aria-labelledby="bd-h" className={cn(card, "overflow-hidden")}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="bd-h" className="text-[15px] font-semibold leading-[22px] text-fg">Build destinations</h2>
          {read.data && <span className="text-xs text-muted">{list.length === 0 ? "none" : `${list.length} private ${list.length === 1 ? "address" : "addresses"}`}</span>}
        </div>
      </div>
      {readFailed(read) ? (
        <div className="p-4">
          <LoadFailed what="the build destinations" onRetry={() => void read.refetch()} />
        </div>
      ) : !read.data ? null : list.length > 0 ? (
        <ul>
          {list.map((d, i) => (
            <li key={d.id} className={cn("flex flex-wrap items-center justify-between gap-x-3 gap-y-2 px-4 py-2.5 text-[13px]", i > 0 && "border-t border-subtle")}>
              <div className="min-w-0">
                <span className="break-all font-mono text-xs text-fg">{destinationText(d)}</span>
                {!d.port && <span className="text-xs text-muted"> every TCP port</span>}
                <p className="text-xs leading-[18px] text-muted [overflow-wrap:anywhere]">
                  {[d.note, `added ${new Date(d.created_at).toLocaleDateString("en-US", { month: "short", day: "numeric" })}`].filter(Boolean).join(" · ")}
                </p>
              </div>
              {removing === d.id ? (
                <span role="group" aria-label={`Remove ${destinationText(d)}`} className="inline-flex flex-wrap items-center justify-end gap-2 text-xs text-fg/85">
                  <span>Stop builds here reaching <span className="break-all font-mono">{destinationText(d)}</span>?</span>
                  <button
                    type="button"
                    className="touch-target inline-flex h-[30px] items-center rounded-md bg-danger px-2.5 font-semibold text-on-danger disabled:opacity-50"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(d)}
                  >
                    {remove.isPending ? "Removing…" : "Remove"}
                  </button>
                  <button type="button" autoFocus className={btnSm} onClick={() => { setRemoving(null); setClosed(d.id); }}>Cancel</button>
                </span>
              ) : (
                <button
                  type="button"
                  aria-label={`Remove ${destinationText(d)}…`}
                  className={ghostSm}
                  onClick={() => setRemoving(d.id)}
                  // Back from a cancelled confirmation, the focus returns here.
                  autoFocus={closed === d.id}
                  onBlur={() => setClosed(null)}
                >
                  Remove…
                </button>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="px-4 py-3 text-[13px] text-fg/70">None yet. Add the registry or the mirror your builds need.</p>
      )}
      {read.data && (
        <form
          className="flex flex-col gap-2.5 border-t border-subtle px-4 py-3"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            setRefused(null);
            if (!address.trim()) refuse({ field: "address", text: "Give an address or a CIDR, such as 10.0.5.20 or 10.0.5.0/24." });
            else if (!portOk) refuse({ field: "port", text: "A port is a number from 1 to 65535; leave it empty for every TCP port." });
            else add.mutate();
          }}
        >
          <p className={cn(help, "leading-[18px]")}>
            Builds on this server reach the internet and no private address. An entry opens one to every build here, in any workspace. A preview opened by an agent or with an API key builds apart and reaches none of them.
          </p>
          <div className="flex flex-wrap items-end gap-2.5">
            <div className="flex min-w-0 flex-[2_1_150px] flex-col gap-1.5">
              <label htmlFor="bd-address" className={fieldLabel}>Address or CIDR</label>
              <input
                id="bd-address"
                value={address}
                onChange={(e) => { setAddress(e.target.value); setRefused(null); }}
                placeholder="10.0.5.20"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={refused?.field === "address" || undefined}
                aria-describedby={described("address")}
                className={cn(fieldInput, "h-8 w-full font-mono text-xs aria-invalid:border-status-failed")}
              />
            </div>
            <div className="flex flex-[0_0_96px] flex-col gap-1.5">
              <label htmlFor="bd-port" className={fieldLabel}>TCP port</label>
              <input
                id="bd-port"
                inputMode="numeric"
                value={port}
                onChange={(e) => { setPort(e.target.value); setRefused(null); }}
                placeholder="every"
                autoComplete="off"
                aria-invalid={refused?.field === "port" || undefined}
                aria-describedby={described("port")}
                className={cn(fieldInput, "h-8 w-full font-mono text-xs aria-invalid:border-status-failed")}
              />
            </div>
            <div className="flex min-w-0 flex-[3_1_170px] flex-col gap-1.5">
              <label htmlFor="bd-note" className={fieldLabel}>Note</label>
              <input
                id="bd-note"
                value={note}
                onChange={(e) => { setNote(e.target.value); setRefused(null); }}
                placeholder="What it is"
                maxLength={200}
                autoComplete="off"
                aria-invalid={refused?.field === "note" || undefined}
                aria-describedby={described("note")}
                className={cn(fieldInput, "h-8 w-full text-xs aria-invalid:border-status-failed")}
              />
            </div>
            <button type="submit" className={cn(btnSm, "h-8")} disabled={add.isPending}>
              {add.isPending ? "Adding…" : "Add destination…"}
            </button>
          </div>
          {refused && <p id="bd-error" role="alert" className="text-xs leading-[18px] text-status-failed-fg">{refused.text}</p>}
        </form>
      )}
      <div className={settingsFoot}>
        <span className="text-xs">
          Private IPv4 only, and never {serverId ? "the panel, " : ""}this server’s own addresses or its Docker networks. A change asks for your password and is written to the audit log.
        </span>
      </div>
    </section>
  );
}
