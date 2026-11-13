"use client";

import { WorkspaceIssuesCard } from "@/components/issues/settings";
import { WorkspaceIsolationCard } from "@/components/settings/workspace-isolation";
import { CyclesSettingsCard, EstimatesSettingsCard } from "@/components/issues/cycles";
import { Suspense, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, ChevronRight, MoreHorizontal, Plus } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { issueTypesQuery } from "@/lib/api/issue.service";
import { SectionsLayout, type PageSection } from "@/app/servers/[id]/sections";
import { btnSm, ghostSm, primarySm, saveBar } from "@/app/projects/_lib/ui";
import { Read, readFailed } from "@/components/shell/unreachable";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import {
  orgService,
  type OrgInvite,
  type OrgMember,
} from "@/lib/api/org.service";
import { sharedVolumeService } from "@/lib/api/sharedvolume.service";
import {
  projectsQuery,
  serversQuery,
  workspaceServicesQuery,
} from "@/lib/queries";
import { untilHint } from "@/lib/time";
import { useQueryFlag } from "@/lib/use-query-flag";
import { cn, formatDateTime, initials, possessive } from "@/lib/utils";
import {
  CardHead,
  Segmented,
  SettingsFrame,
  card,
  dangerButton,
  fieldInput,
  help,
  readInCard,
  roleChip,
  smallButton,
  td,
  th,
} from "@/components/settings/settings-frame";
import { deleteRows } from "./_components/workspace";
import {
  DeleteDialog,
  InviteDialog,
  LeaveDialog,
  ROLE_TEXT,
  RemoveDialog,
  RevokeDialog,
  RoleDialog,
  inviteURL,
} from "./_components/workspace-dialogs";

const BYPASS = [
  { value: "owner", label: "Owners" },
  { value: "developer", label: "Owners and developers" },
] as const;
type Bypass = (typeof BYPASS)[number]["value"];

/** Settings → Workspace (board: ReworkSettings): one scroll of cards with an
 *  "On this page" index: members and invites, the workspace's name and
 *  capacity rule, isolation, issues, cycles, estimates, leave or delete. */
export default function WorkspaceSettingsPage() {
  // useQueryFlag reads the URL: a boundary on a page Next prerenders.
  return (
    <Suspense>
      <Workspace />
    </Suspense>
  );
}

function Workspace() {
  const { user } = useAuth();
  const role = useOrgRole();
  const isOwner = role === "owner";
  const workspace = user?.orgName ?? "";

  const org = useQuery({
    queryKey: ["org"],
    queryFn: () => orgService.getOrg(),
  });
  const members = useQuery({
    queryKey: ["org-members"],
    queryFn: () => orgService.listMembers(),
  });
  const invites = useQuery({
    queryKey: ["org-invites"],
    queryFn: () => orgService.listInvites(),
    enabled: isOwner,
  });
  const settings = useQuery({
    queryKey: ["org-settings"],
    queryFn: () => orgService.getSettings(),
  });
  const projects = useQuery(projectsQuery);
  const servers = useQuery(serversQuery);
  const services = useQuery(workspaceServicesQuery);
  const types = useQuery(issueTypesQuery);
  const volumes = useQuery({
    queryKey: ["shared-volumes"],
    queryFn: () => sharedVolumeService.list(),
    enabled: isOwner,
  });

  const [inviting, setInviting] = useQueryFlag("invite");
  const [roleOf, setRoleOf] = useState<OrgMember | null>(null);
  const [removing, setRemoving] = useState<OrgMember | null>(null);
  const [revoking, setRevoking] = useState<OrgInvite | null>(null);
  const [leaving, setLeaving] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const memberName = (id: string) => {
    const m = members.data?.find((x) => x.user_id === id);
    return m ? m.user_name || m.user_email : "a former member";
  };
  const owners = (members.data ?? []).filter((m) => m.role === "owner");
  const ownServers = (servers.data ?? []).filter((s) => !s.shared);
  const projectCount = projects.data?.length ?? 0;
  // A count that couldn't be read isn't zero.
  const reach = projects.data && servers.data
    ? `its ${projectCount} ${projectCount === 1 ? "project" : "projects"}, ${ownServers.length} ${ownServers.length === 1 ? "server" : "servers"} and settings`
    : "its projects, servers and settings";

  const typeCount = (types.data ?? []).filter((t) => !t.archived).length;
  const sections: PageSection[] = [
    { id: "members", label: "Members", count: { n: members.data?.length ?? 0 } },
    { id: "general", label: "General" },
    { id: "isolation", label: "Isolation" },
    { id: "issues", label: "Issues", count: { n: typeCount } },
    { id: "cycles", label: "Cycles" },
    { id: "estimates", label: "Estimates" },
    { id: "leave", label: isOwner ? "Leave or delete" : "Leave", danger: true },
  ];
  const anchor = "focus-visible:outline-none";

  return (
    <SettingsFrame title="Workspace" gap="gap-6">
      <SectionsLayout label="Jump to a settings section" sections={sections}>
      <section id="members" tabIndex={-1} aria-labelledby="mem-h" className={cn(card, anchor)}>
        <CardHead
          id="mem-h"
          title="Members"
          sum={`${members.data?.length ?? "–"} · ${isOwner ? "role changes apply on the member’s next request" : "owners manage members and invites"}`}
        >
          {isOwner && (
            <button type="button" className={btnSm} onClick={() => setInviting(true)}>
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
              Invite member
            </button>
          )}
        </CardHead>
        <table className="w-full border-collapse text-[13px] max-sm:block">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className={th}>
                Member
              </th>
              <th scope="col" className={cn(th, "w-32")}>
                Role
              </th>
              <th scope="col" className={th}>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {readFailed(members) ? (
              <ErrorRow
                cols={3}
                what="the members"
                error={members.error}
                onRetry={() => members.refetch()}
              />
            ) : members.isLoading ? (
              <SkeletonRow cols={3} />
            ) : (
              members.data?.map((m, i) => {
                const self = m.user_id === user?.id;
                const name = m.user_name || m.user_email;
                return (
                  <tr key={m.id} className={cn(phoneRow, i > 0 && "max-sm:border-t max-sm:border-subtle")}>
                    <td className={cn(cell, i === 0 && "border-t-0", "max-sm:col-start-1 max-sm:row-start-1")}>
                      <span className="flex items-center gap-2.5">
                        <span
                          aria-hidden="true"
                          className="flex h-7 w-7 flex-none items-center justify-center rounded-full bg-raised text-[10px] font-semibold text-fg"
                        >
                          {initials(name)}
                        </span>
                        <span className="flex min-w-0 flex-col">
                          <span className="font-medium text-fg [overflow-wrap:anywhere]">
                            {name}
                            {self && <span className="font-normal text-muted"> (you)</span>}
                          </span>
                          <span className="font-mono text-xs leading-5 text-muted [overflow-wrap:anywhere]">
                            {m.user_email}
                          </span>
                        </span>
                      </span>
                    </td>
                    <td className={cn(cell, i === 0 && "border-t-0", "max-sm:col-start-1 max-sm:row-start-2")}>
                      <span className={roleChip}>{m.role}</span>
                    </td>
                    <td className={cn(cell, i === 0 && "border-t-0", "py-1.5 text-right",
                        self
                          ? "max-sm:col-span-2 max-sm:row-start-3 max-sm:text-left"
                          : "max-sm:col-start-2 max-sm:row-span-2 max-sm:row-start-1 max-sm:self-center",
                        !isOwner && "max-sm:hidden")}>
                      {isOwner && !self && (
                        <RowActions
                          label={`Actions for ${name}: change role, remove`}
                          actions={[
                            { label: "Change role", aria: `Change role for ${name}`, run: () => setRoleOf(m) },
                            { label: "Remove", aria: `Remove ${name}`, run: () => setRemoving(m) },
                          ]}
                        />
                      )}
                      {isOwner && self && (
                        <span className="text-xs text-muted">
                          {owners.length === 1 ? "You’re the only owner" : "Another owner can change your role"}
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
        <details className="group border-t border-subtle">
          <summary className={cn(ghostSm, "mx-4 my-2.5 w-fit cursor-pointer list-none [&::-webkit-details-marker]:hidden")}>
            <ChevronRight className="h-3.5 w-3.5 transition-transform group-open:rotate-90" aria-hidden="true" />
            What each role can do
          </summary>
          <dl className="grid grid-cols-[repeat(auto-fit,minmax(min(240px,100%),1fr))] gap-x-6 gap-y-2 px-4 pb-3.5 text-[13px] leading-[19px]">
            {(["owner", "developer", "viewer"] as const).map((r) => (
              <div key={r} className="flex flex-col gap-0.5">
                <dt className="font-medium capitalize text-fg">{r}</dt>
                <dd className="text-fg/70">{ROLE_TEXT[r]}</dd>
              </div>
            ))}
          </dl>
        </details>
        {isOwner && (
          <>
            <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 border-t border-subtle px-4 pb-2 pt-3">
              <h3 id="inv-h" className="text-[13px] font-semibold text-fg">
                Pending invites
              </h3>
              <span className="text-xs text-muted">
                {invites.data?.length ?? "–"} · each link works once, for 7 days
              </span>
            </div>
            <table aria-labelledby="inv-h" className="w-full border-collapse text-[13px] max-sm:block">
              <thead className="max-sm:hidden">
                <tr>
                  <th scope="col" className={th}>
                    Link
                  </th>
                  <th scope="col" className={cn(th, "w-32")}>
                    Role
                  </th>
                  <th scope="col" className={th}>
                    Expires
                  </th>
                  <th scope="col" className={th}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="max-sm:block">
                {readFailed(invites) ? (
                  <ErrorRow
                    cols={4}
                    what="the invites"
                    error={invites.error}
                    onRetry={() => invites.refetch()}
                  />
                ) : invites.isLoading ? (
                  <SkeletonRow cols={4} />
                ) : invites.data?.length === 0 ? (
                  <tr className="max-sm:block">
                    <td colSpan={4} className={cn(td, "border-t-0 py-3 text-muted max-sm:block")}>
                      No pending invites. Invite member makes a link to send.
                    </td>
                  </tr>
                ) : (
                  invites.data?.map((inv, i) => (
                    <InviteRow
                      key={inv.id}
                      first={i === 0}
                      invite={inv}
                      by={memberName(inv.created_by)}
                      onRevoke={() => setRevoking(inv)}
                    />
                  ))
                )}
              </tbody>
            </table>
          </>
        )}
      </section>

      <section id="general" tabIndex={-1} aria-labelledby="gen-h" className={cn(card, readInCard, anchor)}>
        <CardHead id="gen-h" title="General" />
        <Read query={org} what="the workspace’s settings" loading={generalLoading}>{(o) => (
        <Read query={settings} what="the workspace’s settings" loading={generalLoading}>{(s) => isOwner ? (
            <GeneralForm
              key={`${o.name}/${s.resource_bypass_min_role}`}
              name={o.name}
              bypass={s.resource_bypass_min_role}
            />
        ) : (
          <>
            <dl className="text-[13px] leading-[19px]">
              <div className="grid grid-cols-[minmax(0,280px)_minmax(0,1fr)] gap-3 px-4 py-2.5 max-sm:grid-cols-1 max-sm:gap-0.5">
                <dt className="text-muted">Workspace name</dt>
                <dd className="text-fg">{o.name}</dd>
              </div>
              <div className="grid grid-cols-[minmax(0,280px)_minmax(0,1fr)] gap-3 border-t border-subtle px-4 py-2.5 max-sm:grid-cols-1 max-sm:gap-0.5">
                <dt className="text-muted">Who can deploy past a server’s capacity</dt>
                <dd className="text-fg">
                  {s.resource_bypass_min_role === "developer"
                    ? "Owners and developers"
                    : "Owners"}
                </dd>
              </div>
            </dl>
            <p className={saveBar}>Owners change the name and who can deploy past capacity.</p>
          </>
        )}</Read>
        )}</Read>
      </section>

      <div id="isolation" tabIndex={-1} className={anchor}>
        <WorkspaceIsolationCard isOwner={isOwner} />
      </div>
      <div id="issues" tabIndex={-1} className={anchor}>
        <WorkspaceIssuesCard isOwner={isOwner} />
      </div>
      <div id="cycles" tabIndex={-1} className={anchor}>
        <CyclesSettingsCard isOwner={isOwner} />
      </div>
      <div id="estimates" tabIndex={-1} className={anchor}>
        <EstimatesSettingsCard isOwner={isOwner} />
      </div>

      <section
        id="leave"
        tabIndex={-1}
        aria-labelledby="danger-h"
        className={cn("rounded-[10px] border border-danger/40 bg-surface", anchor)}
      >
        <CardHead id="danger-h" title={isOwner ? "Leave or delete" : "Leave"} />
        <div className="flex flex-wrap items-center gap-x-6 gap-y-3 p-4">
          <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-0.5">
            <h3 className="text-[13px] font-medium text-fg">Leave {workspace}</h3>
            <p className="text-[13px] leading-[19px] text-muted">
              {isOwner && owners.length === 1
                ? `You’re ${possessive(workspace)} only owner. Make another member an owner first, or delete the workspace.`
                : "You lose access at once. Your account and your other workspaces stay; to come back, an owner invites you again."}
            </p>
          </div>
          {!(isOwner && owners.length <= 1) && members.data && (
            <button
              type="button"
              className="touch-target inline-flex h-9 items-center rounded-lg border border-danger/45 bg-raised px-3 text-[13px] font-medium text-status-failed-fg hover:bg-raised/70"
              onClick={() => setLeaving(true)}
            >
              Leave workspace…
            </button>
          )}
        </div>
        {isOwner && (
          <div className="flex flex-wrap items-center gap-x-6 gap-y-3 border-t border-subtle p-4">
            <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-0.5">
              <h3 className="text-[13px] font-medium text-fg">Delete {workspace}</h3>
              <p className="text-[13px] leading-[19px] text-muted">
                Removes every service from its server, with its volumes and
                their data. Then deletes the shared volumes, the server
                entries (the machines are not touched), every project, and the
                members, invites, settings, alert webhook and audit log. It
                can’t be undone.
              </p>
            </div>
            <button
              type="button"
              className={dangerButton}
              onClick={() => setDeleting(true)}
            >
              Delete workspace…
            </button>
          </div>
        )}
      </section>
      </SectionsLayout>

      <InviteDialog
        open={inviting}
        workspace={workspace}
        onClose={() => setInviting(false)}
      />
      <RoleDialog member={roleOf} onClose={() => setRoleOf(null)} />
      <RemoveDialog
        member={removing}
        workspace={workspace}
        reach={reach}
        onClose={() => setRemoving(null)}
      />
      <RevokeDialog
        invite={revoking}
        by={revoking ? memberName(revoking.created_by) : ""}
        onClose={() => setRevoking(null)}
      />
      <LeaveDialog
        open={leaving}
        workspace={workspace}
        reach={reach}
        lastWorkspace={(user?.orgs?.length ?? 1) <= 1}
        onClose={() => setLeaving(false)}
      />
      <DeleteDialog
        open={deleting}
        workspace={workspace}
        rows={deleteRows({
          projects: projects.data?.map((p) => p.name),
          services: services.data?.length,
          volumes: volumes.data?.length,
          servers: servers.data && ownServers.map((s) => s.name),
          members: members.data?.length,
          invites: invites.data?.length ?? 0,
        })}
        onClose={() => setDeleting(false)}
      />
    </SettingsFrame>
  );
}

const generalLoading = (
  <div
    role="status"
    className="h-[120px] motion-safe:animate-pulse"
    aria-label="Loading the workspace’s settings"
  />
);

// A table row that is a two-column card on a phone (the board's .lt below
// 640px): what the row is on the left, its actions on the right.
const phoneRow =
  "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3";
const cell = cn(td, "align-middle max-sm:block max-sm:border-0 max-sm:p-0");

/** A row's buttons, folded into a ⋯ menu on a phone. */
function RowActions({
  label,
  actions,
}: {
  label: string;
  actions: { label: string; aria: string; run: () => void }[];
}) {
  return (
    <>
      <span className="inline-flex gap-1 max-sm:hidden">
        {actions.map((a, i) => (
          <button
            key={a.label}
            type="button"
            className={i === 0 ? btnSm : ghostSm}
            aria-label={a.aria}
            onClick={a.run}
          >
            {a.label}
          </button>
        ))}
      </span>
      <DropdownMenu>
        <DropdownMenuTrigger
          aria-label={label}
          className="touch-target inline-flex h-11 w-11 items-center justify-center rounded-lg text-fg/70 hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus data-[state=open]:bg-raised sm:hidden"
        >
          <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {actions.map((a) => (
            <DropdownMenuItem key={a.label} onSelect={a.run}>
              {a.label}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  );
}

function InviteRow({
  invite,
  first,
  by,
  onRevoke,
}: {
  invite: OrgInvite;
  first: boolean;
  by: string;
  onRevoke: () => void;
}) {
  const [now] = useState(() => Date.now());
  const url = inviteURL(invite.token);
  const shown = `${url.replace(/^https?:\/\//, "").slice(0, -invite.token.length)}${invite.token.slice(0, 8)}…`;
  const copy = () =>
    navigator.clipboard.writeText(url).then(
      () => toast.success("Link copied"),
      () => toast.error("Couldn’t copy the link"),
    );
  const c = cn(cell, first && "border-t-0");
  return (
    <tr className={cn(phoneRow, !first && "max-sm:border-t max-sm:border-subtle")}>
      <td className={cn(c, "font-mono text-xs text-fg [overflow-wrap:anywhere] max-sm:col-start-1 max-sm:row-start-1")}>{shown}</td>
      <td className={cn(c, "max-sm:col-start-1 max-sm:row-start-2")}>
        <span className={roleChip}>{invite.role}</span>
      </td>
      <td className={cn(c, "whitespace-nowrap text-muted max-sm:col-start-1 max-sm:row-start-3 max-sm:whitespace-normal")}>
        <time dateTime={invite.expires_at} title={formatDateTime(invite.expires_at)}>
          {untilHint(invite.expires_at, now)}
        </time>{" "}
        · by {by}
      </td>
      <td className={cn(c, "py-1.5 text-right max-sm:col-start-2 max-sm:row-span-3 max-sm:row-start-1 max-sm:self-center")}>
        <RowActions
          label={`Actions for the ${invite.role} invite: copy link, revoke`}
          actions={[
            { label: "Copy link", aria: `Copy link for the ${invite.role} invite`, run: copy },
            { label: "Revoke", aria: `Revoke the ${invite.role} invite`, run: onRevoke },
          ]}
        />
      </td>
    </tr>
  );
}

/** The workspace's name and capacity rule, saved together. Keyed on the
 *  saved values, so a save (or another tab's) resets it. */
function GeneralForm({ name, bypass }: { name: string; bypass: Bypass }) {
  const queryClient = useQueryClient();
  const { orgRenamed } = useAuth();
  const [draftName, setDraftName] = useState(name);
  const [draftBypass, setDraftBypass] = useState<Bypass>(bypass);
  const changes =
    (draftName.trim() !== name ? 1 : 0) + (draftBypass !== bypass ? 1 : 0);
  const save = useMutation({
    mutationFn: async () => {
      if (draftName.trim() !== name) {
        await orgService.updateOrgName(draftName.trim());
        orgRenamed(draftName.trim());
      }
      if (draftBypass !== bypass)
        await orgService.updateSettings({
          resource_bypass_min_role: draftBypass,
        });
    },
    onSuccess: () => {
      toast.success("Workspace settings saved");
      queryClient.invalidateQueries({ queryKey: ["org"] });
      queryClient.invalidateQueries({ queryKey: ["org-settings"] });
    },
  });
  const discard = () => {
    setDraftName(name);
    setDraftBypass(bypass);
    save.reset();
  };

  const changed =
    changes === 2
      ? "2 unsaved changes"
      : draftName.trim() !== name
        ? "Unsaved change to the name"
        : "Unsaved change to who can deploy past capacity";

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid grid-cols-2 gap-3.5 p-4 max-sm:grid-cols-1">
        <div className="flex min-w-0 flex-col gap-1.5">
          <label htmlFor="ws-name" className="text-[13px] font-medium text-fg">
            Workspace name
          </label>
          <input
            id="ws-name"
            required
            maxLength={100}
            value={draftName}
            onChange={(e) => setDraftName(e.target.value)}
            aria-describedby="ws-name-help"
            className={fieldInput}
          />
          <span id="ws-name-help" className={help}>
            Shown in the sidebar and on invite links.
          </span>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <span className="text-[13px] font-medium text-fg">
            Who can deploy past a server’s capacity
          </span>
          <Segmented
            label="Who can deploy past a server’s capacity"
            options={BYPASS}
            value={draftBypass}
            onChange={setDraftBypass}
          />
          <span className={cn(help, "leading-[18px]")}>
            When a deploy’s CPU or memory doesn’t fit on its server, these
            members can confirm and deploy anyway; each override goes to the
            audit log. Viewers never can. On a server another workspace shares
            with you, only that workspace’s owners can.
          </span>
        </div>
      </div>
      {save.error && (
        <p
          role="alert"
          className="mx-4 mb-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
        >
          {save.error instanceof Error
            ? save.error.message
            : "The settings were not saved."}
        </p>
      )}
      {changes > 0 && (
        <div className={saveBar}>
          <span aria-live="polite">{changed}</span>
          <span className="flex gap-1">
            <button type="button" onClick={discard} className={ghostSm}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={save.isPending || draftName.trim() === ""}
              className={primarySm}
            >
              {save.isPending ? "Saving…" : "Save"}
            </button>
          </span>
        </div>
      )}
    </form>
  );
}

function SkeletonRow({ cols }: { cols: number }) {
  return (
    <tr aria-busy="true">
      <td colSpan={cols} className={cn(td, "py-3.5")}>
        <span
          role="status"
          className="flex flex-col gap-2.5 motion-safe:animate-pulse"
          aria-label="Loading"
        >
          <span className="block h-2.5 w-[62%] rounded bg-raised" />
          <span className="block h-2.5 w-[44%] rounded bg-raised" />
        </span>
      </td>
    </tr>
  );
}

function ErrorRow({
  cols,
  what,
  error: current,
  onRetry,
}: {
  cols: number;
  what: string;
  error: unknown;
  onRetry: () => void;
}) {
  // A failed read fetching again has dropped its error: keep the last one.
  const [last, setLast] = useState<unknown>(current);
  if (current && current !== last) setLast(current);
  const error = current ?? last;
  return (
    <tr>
      <td colSpan={cols} role="alert" className={cn(td, "py-3.5")}>
        <span className="flex flex-wrap items-center gap-2.5">
          <AlertTriangle
            className="h-4 w-4 text-status-failed"
            aria-hidden="true"
          />
          <span className="flex-[1_1_320px] text-status-failed-fg">
            Could not load {what}:{" "}
            {error instanceof Error ? error.message : "unknown error"}. Nothing
            has changed.
          </span>
          <button type="button" className={smallButton} onClick={onRetry}>
            Retry
          </button>
        </span>
      </td>
    </tr>
  );
}
