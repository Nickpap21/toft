"use client";

import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { REVOKE_EXCEPTION_WHY, useReauth } from "@/components/reauth";
import {
  AlertTriangle,
  Check,
  ChevronRight,
  Info,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
} from "lucide-react";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { ApiError } from "@/lib/api/client";
import {
  deployPolicyService,
  type DeployPolicyMode,
  type DeployPolicyReportEntry,
  type PolicyViolation,
} from "@/lib/api/deploypolicy.service";
import { cn, formatDateTime } from "@/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  CardHead,
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldLabel,
  help,
  smallButton,
  td,
  th,
} from "@/components/settings/settings-frame";
import { InstanceFrame } from "@/components/instance/instance-frame";
import { SectionsLayout } from "@/app/servers/[id]/sections";
import {
  exceptionValues,
  notesSummary,
  perWorkspace,
  sections,
  whereOf,
  type WorkspaceGroup,
} from "./report-view";

const QUERY_KEY = ["deploy-policy-report"];
const settingsHref = (e: DeployPolicyReportEntry, tab = "settings") =>
  `/projects/${e.project_id}/services/${e.service_id}?tab=${tab}`;
const nameOf = (e: DeployPolicyReportEntry) =>
  e.project_name ? `${e.project_name} / ${e.service_name}` : e.service_name;

/** Instance → Deploy policy (board ReworkInstancePolicy, D-139): every
 *  service on the instance the policy (D-19) has something to say about,
 *  the exceptions granted, and the rules behind a disclosure, in one scroll
 *  with On this page. Instance operators only. */
export default function DeployPolicyPage() {
  const { isLoading: authLoading } = useAuth();
  const isOperator = useIsOperator();
  const report = useQuery({
    queryKey: QUERY_KEY,
    queryFn: () => deployPolicyService.violationsReport(),
    enabled: isOperator,
    retry: false,
  });
  const [revoking, setRevoking] = useState<DeployPolicyReportEntry | null>(
    null,
  );
  const r = report.data;
  const s = r ? sections(r) : null;
  const warn = r?.mode === "warn";

  return (
    <InstanceFrame title="Deploy policy" gap="gap-6">
      {authLoading ? null : !isOperator ? (
        <div className={cn(card, "p-4 text-[13px] text-fg/70")}>
          Only instance operators see the deploy policy report.
        </div>
      ) : report.isLoading ? (
        <div
          className={cn(card, "h-48 motion-safe:animate-pulse")}
          aria-label="Checking every service"
        />
      ) : report.error || !r || !s ? (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-danger/35 bg-surface px-4 py-3.5"
        >
          <span className="flex items-center gap-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
            Could not run the report:{" "}
            {report.error instanceof ApiError && report.error.status < 500
              ? report.error.message
              : "the panel did not answer"}
            . Nothing was changed.
          </span>
          <button
            type="button"
            className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
            onClick={() => report.refetch()}
          >
            Retry
          </button>
        </div>
      ) : (
        <SectionsLayout
          label="Jump to a deploy policy section"
          sections={[
            { id: "mode", label: "Mode" },
            {
              id: "refused",
              label: warn ? "Would be refused" : "Will be refused",
              count: { n: s.refusedCount, tone: warn ? "warn" : "bad" },
            },
            { id: "exceptions", label: "Exceptions", count: { n: s.excepted.length } },
            ...(s.notes.length > 0 ? [{ id: "notes", label: "Notes", count: { n: s.notes.length } }] : []),
            { id: "rules", label: "Rules", count: { n: r.rules.length } },
          ]}
        >
          <section id="mode" tabIndex={-1} aria-labelledby="mode-h" className={card}>
            <CardHead
              id="mode-h"
              title="Mode"
              sum={
                <span
                  className={cn(
                    "inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium",
                    warn ? "bg-status-pending-soft text-status-pending-fg" : "bg-status-running-soft text-status-running-fg",
                  )}
                >
                  <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", warn ? "bg-status-pending" : "bg-status-running")} />
                  {warn ? "Warn" : "Enforce"}
                </span>
              }
            >
              <span className="text-xs text-muted">
                checked on save and again at deploy, for every service in every workspace
              </span>
            </CardHead>
            <ModeNotice mode={r.mode} refused={s.refusedCount} />
            <dl className="flex flex-col text-[13px]">
              {[
                [
                  warn ? "Would be refused" : "Will be refused",
                  <span
                    key="r"
                    className={cn(
                      "font-medium",
                      s.refusedCount === 0 ? "text-fg" : warn ? "text-status-pending-fg" : "text-status-failed-fg",
                    )}
                  >
                    {s.refusedCount} {s.refusedCount === 1 ? "service" : "services"}
                  </span>,
                  [
                    s.refusedCount === 0 && s.waitingCount === 0 ? "Every service passes" : perWorkspace(s.refused),
                    s.waitingCount > 0 ? `${s.waitingCount} waiting for an operator` : "",
                  ]
                    .filter(Boolean)
                    .join(" · "),
                  false,
                ],
                [
                  "With an exception",
                  `${s.excepted.length} ${s.excepted.length === 1 ? "service" : "services"}`,
                  s.excepted.map((e) => e.allowed_rules.join(", ")).join(" · ") || "None granted",
                  s.excepted.length > 0,
                ],
                ["Notes", `${s.notes.length}, not refused`, notesSummary(s.notes), false],
              ].map(([dt, dd, sub, mono], i) => (
                <div
                  key={i}
                  className="grid grid-cols-[120px_minmax(0,1fr)] gap-3 border-t border-subtle px-4 py-2.5 max-sm:grid-cols-1 max-sm:gap-0.5"
                >
                  <dt className="text-muted">{dt}</dt>
                  <dd className="min-w-0 text-fg">
                    {dd}
                    <span className={cn(help, "block", mono && "font-mono [overflow-wrap:anywhere]")}>{sub}</span>
                  </dd>
                </div>
              ))}
            </dl>
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-xs text-muted">
              <span>
                Checked {formatDateTime(r.generated_at).slice(11)} · {r.services_checked}{" "}
                {r.services_checked === 1 ? "service" : "services"} on {r.servers_checked}{" "}
                {r.servers_checked === 1 ? "server" : "servers"}
              </span>
              <button type="button" className={smallButton} disabled={report.isFetching} onClick={() => report.refetch()}>
                <RefreshCw
                  className={cn("h-3.5 w-3.5", report.isFetching && "motion-safe:animate-spin")}
                  aria-hidden="true"
                />
                {report.isFetching ? "Checking…" : "Check again"}
              </button>
            </div>
          </section>

          <section id="refused" tabIndex={-1} aria-labelledby="refused-h" className={card}>
            <CardHead
              id="refused-h"
              title={
                s.refusedCount === 0
                  ? warn
                    ? "Nothing would be refused in enforce mode"
                    : "Nothing will be refused"
                  : warn
                    ? "Would be refused in enforce mode"
                    : "The next deploy will be refused"
              }
              sum={`${s.refusedCount} · fixes are made in each service’s settings`}
            />
            {s.refusedCount === 0 ? (
              <p className="flex items-center gap-2.5 px-4 py-3 text-[13px] text-fg/85">
                <Check className="h-4 w-4 flex-none text-status-running" aria-hidden="true" />
                No service would be refused. Every one of the {r.services_checked} services passes, or has an
                exception for what it needs.
              </p>
            ) : (
              s.refused.map((g, i) => <Group key={g.orgId || "none"} g={g} warn={warn} first={i === 0} />)
            )}
          </section>

          <section id="exceptions" tabIndex={-1} aria-labelledby="exc-h" className={card}>
            <CardHead
              id="exc-h"
              title="Exceptions"
              sum={`${s.excepted.length} · per service, for named rules and the values they had`}
            />
            <table className="w-full border-collapse text-[13px] max-sm:block">
              <thead className={cn("max-sm:hidden", s.excepted.length === 0 && "hidden")}>
                <tr>
                  <th scope="col" className={cn(th, "w-[24%]")}>Service</th>
                  <th scope="col" className={cn(th, "w-[34%]")}>Allows</th>
                  <th scope="col" className={th}>Reason</th>
                  <th scope="col" className={th}><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="max-sm:block">
                {s.excepted.length === 0 ? (
                  <tr className="max-sm:block">
                    <td colSpan={4} className={cn(td, "border-t-0 py-3 text-fg/70 max-sm:block")}>
                      No service has an exception. Grant one from a service’s Settings → Deploy policy.
                    </td>
                  </tr>
                ) : (
                  s.excepted.map((e, i) => {
                    const ev = exceptionValues(e);
                    return (
                      <tr key={e.service_id} className={cn(phoneRow, i > 0 && "max-sm:border-t max-sm:border-subtle")}>
                        <td className={cn(cell, i === 0 && "border-t-0", "align-top max-sm:col-start-1 max-sm:row-start-1")}>
                          <span className="flex flex-col">
                            <ServiceLink e={e} />
                            <span className={help}>
                              {e.org_name} · {e.server_name || "panel host"}
                            </span>
                            <span className={help}>
                              {e.exception_granted_by || "a removed account"}
                              {e.exception_granted_at && ` · ${formatDateTime(e.exception_granted_at)}`}
                            </span>
                          </span>
                        </td>
                        <td className={cn(cell, i === 0 && "border-t-0", "align-top max-sm:col-span-2 max-sm:row-start-2")}>
                          <span className="flex flex-col gap-1">
                            <span className="self-start rounded-sm border border-subtle bg-ground px-1.5 font-mono text-[11px] leading-5 text-fg/85">
                              {e.allowed_rules.join(" · ")}
                            </span>
                            {ev.values.map((pv) => (
                              <span
                                key={`${pv.pending}-${pv.text}`}
                                className={cn(
                                  "whitespace-pre-wrap break-all font-mono text-xs",
                                  pv.pending ? "text-status-pending-fg" : "text-fg/85",
                                )}
                              >
                                {pv.pending ? "+ " : ""}
                                {pv.text}
                              </span>
                            ))}
                            <span
                              className={cn(
                                "inline-flex items-center gap-1.5 text-xs",
                                ev.state === "pending" || ev.state === "legacy"
                                  ? "text-status-pending-fg"
                                  : "text-status-running-fg",
                              )}
                            >
                              {ev.state === "pinned" ? (
                                <Check className="h-3 w-3 flex-none" aria-hidden="true" />
                              ) : (
                                <AlertTriangle className="h-3 w-3 flex-none" aria-hidden="true" />
                              )}
                              {ev.state === "pinned"
                                ? "Deploys with these values"
                                : ev.state === "pending"
                                  ? "A value changed: waiting for an operator"
                                  : "Any value: granted before v0.25.0"}
                            </span>
                          </span>
                        </td>
                        <td className={cn(cell, i === 0 && "border-t-0", "align-top text-fg/85 max-sm:col-span-2 max-sm:row-start-3")}>
                          {e.exception_reason || <span className="text-muted">No reason given</span>}
                        </td>
                        <td
                          className={cn(
                            cell,
                            i === 0 && "border-t-0",
                            "py-1.5 text-right align-top max-sm:col-start-2 max-sm:row-start-1",
                          )}
                        >
                          <button
                            type="button"
                            className={smallButton}
                            aria-label={`Revoke the exception for ${nameOf(e)}`}
                            onClick={() => setRevoking(e)}
                          >
                            Revoke
                          </button>
                        </td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
            <p className="border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
              Grants and revokes go to the workspace’s audit log.
            </p>
          </section>

          {s.notes.length > 0 && (
            <section id="notes" tabIndex={-1} aria-labelledby="notes-h" className={card}>
              <CardHead id="notes-h" title="Notes" sum={`${s.notes.length} · nothing is refused for these`} />
              {s.notes.map((n, i) => (
                <div
                  key={`${n.entry.service_id}-${n.kind}`}
                  className={cn("flex items-start gap-3 px-4 py-3", i > 0 && "border-t border-subtle")}
                >
                  <Info className="mt-0.5 h-4 w-4 flex-none text-muted" aria-label="Note" />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="font-medium text-fg">{nameOf(n.entry)}</span>
                    <span className="text-[13px] leading-[19px] text-muted">
                      {n.text} {n.code && <span className="font-mono text-xs text-fg/85">{n.code}</span>}
                    </span>
                  </span>
                  {n.entry.project_id && (
                    <Link
                      href={settingsHref(n.entry, n.kind === "env" ? "variables" : "settings")}
                      className="touch-target inline-flex flex-none items-center text-[13px] inline-link"
                    >
                      {n.kind === "env" ? "Open variables" : "Open settings"}
                    </Link>
                  )}
                </div>
              ))}
            </section>
          )}

          <Rules rules={r.rules} />
        </SectionsLayout>
      )}

      <RevokeDialog
        target={revoking}
        mode={r?.mode ?? "enforce"}
        onClose={() => setRevoking(null)}
      />
    </InstanceFrame>
  );
}

// A table row that is a two-column card on a phone (the board's .lt below
// 640px), as Settings' members.
const phoneRow =
  "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-2 max-sm:px-4 max-sm:py-3";
const cell = cn(td, "max-sm:block max-sm:border-0 max-sm:p-0");

/** The Mode card's line under its head: what warn or enforce means now. */
function ModeNotice({
  mode,
  refused,
}: {
  mode: DeployPolicyMode;
  refused: number;
}) {
  const env = (v: string) => (
    <span className="font-mono text-xs text-fg/85">TOFT_DEPLOY_POLICY={v}</span>
  );
  return (
    <div className="flex items-start gap-3 px-4 py-3">
      {mode === "warn" ? (
        <AlertTriangle className="mt-0.5 h-4 w-4 flex-none text-status-pending" aria-label="Warning" />
      ) : (
        <ShieldCheck className="mt-0.5 h-4 w-4 flex-none text-status-running" aria-hidden="true" />
      )}
      <div className="flex min-w-0 flex-col gap-0.5 text-[13px] leading-[19px]">
        <span className="font-medium text-fg">
          {mode === "warn" ? "Nothing is refused yet" : "A deploy that breaks a rule is refused"}
        </span>
        <span className="text-muted">
          {mode === "warn" ? (
            <>
              Violations are logged and deploys go ahead.{" "}
              {refused > 0
                ? `In enforce mode the next deploy of the ${refused === 1 ? "service" : `${refused} services`} below would be refused: fix them or grant exceptions, then set `
                : "No service would be refused in enforce mode, so you can set "}
              {env("enforce")} and restart the panel.
            </>
          ) : (
            <>The deploy log says which rule and where. {env("warn")} logs instead of refusing.</>
          )}
        </span>
      </div>
    </div>
  );
}

function ServiceLink({ e }: { e: DeployPolicyReportEntry }) {
  const { user } = useAuth();
  const active = e.org_id === user?.orgId;
  return active && e.project_id ? (
    <Link
      href={settingsHref(e)}
      className="touch-target inline-flex items-center font-medium text-fg underline-offset-2 hover:underline"
    >
      {nameOf(e)}
    </Link>
  ) : (
    <span className="font-medium text-fg">{nameOf(e)}</span>
  );
}

function Group({ g, warn, first }: { g: WorkspaceGroup; warn: boolean; first: boolean }) {
  const { user, switchOrg } = useAuth();
  const [switching, setSwitching] = useState(false);
  const active = !!g.orgId && g.orgId === user?.orgId;
  const member =
    !!g.orgId && (user?.orgs ?? []).some((o) => o.org_id === g.orgId);
  const open = async (href: string) => {
    setSwitching(true);
    try {
      await switchOrg(g.orgId, href);
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : "Failed to switch workspace",
      );
      setSwitching(false);
    }
  };
  return (
    <>
      <div className={cn("flex flex-wrap items-center gap-x-2.5 gap-y-1 px-4 pb-2 pt-3", !first && "border-t border-subtle")}>
        <h3 className="text-[13px] font-semibold text-fg">{g.orgName || "No workspace"}</h3>
        <span className="text-xs text-muted">
          {active
            ? "your active workspace"
            : member
              ? `switch to ${g.orgName} to open its services`
              : "you are not a member of this workspace"}
        </span>
      </div>
      {g.entries.map((e) => (
        <div key={e.service_id} className="flex flex-col gap-3 border-t border-subtle px-4 py-3.5">
          <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className="flex flex-wrap items-center gap-2">
                <span className="font-semibold text-fg">{nameOf(e)}</span>
                <span className="rounded-sm border border-subtle px-1.5 font-mono text-[11px] leading-5 text-fg/85">
                  {e.source_type}
                </span>
              </span>
              <span className={help}>{whereOf(e)}</span>
            </div>
            {e.project_id &&
              (active ? (
                <Link href={settingsHref(e)} className={cn(smallButton, "font-medium")}>
                  Open settings
                </Link>
              ) : member ? (
                <button
                  type="button"
                  className={cn(smallButton, "font-medium")}
                  disabled={switching}
                  onClick={() => open(settingsHref(e))}
                >
                  {switching ? "Switching…" : `Switch to ${g.orgName} and open settings`}
                </button>
              ) : null)}
          </div>
          {e.violations.map((v, j) => (
            <Violation key={`${v.rule}-${v.field}-${j}`} v={v} warn={warn} />
          ))}
        </div>
      ))}
    </>
  );
}

function Violation({ v, warn }: { v: PolicyViolation; warn: boolean }) {
  const sentence = v.message.charAt(0).toUpperCase() + v.message.slice(1);
  return (
    <div
      className={cn(
        "flex flex-col gap-1.5 rounded-lg border px-3 py-3 text-[13px] leading-[19px] text-fg/85",
        warn ? "border-status-pending/35" : "border-status-failed/35",
      )}
    >
      <span className="flex flex-wrap items-center gap-2">
        <ShieldAlert
          className={cn("h-3.5 w-3.5", warn ? "text-status-pending" : "text-status-failed")}
          aria-hidden="true"
        />
        <span className="font-semibold text-fg">{v.title || v.rule}</span>
        <span className="rounded-sm border border-subtle px-1.5 font-mono text-[11px] leading-5 text-fg/85">
          {v.rule}
        </span>
        <span className="font-mono text-xs text-muted [overflow-wrap:anywhere]">
          {v.service ? `services.${v.service}.${v.field}` : v.field}
        </span>
      </span>
      <span className="text-fg">{sentence.endsWith(".") ? sentence : `${sentence}.`}</span>
      {v.why && (
        <span>
          <span className="font-semibold text-fg">Why it matters:</span> {v.why}
        </span>
      )}
      {v.fix && (
        <span>
          <span className="font-semibold text-fg">What to do:</span> {v.fix}
        </span>
      )}
      <span className={help}>
        {v.grantable
          ? "Grantable: if you trust this service, grant an exception for this rule from its settings instead."
          : "Never granted: it protects routing, other tenants or the control plane."}
      </span>
    </div>
  );
}

/** The 24 rules behind a disclosure (D-139): one line until opened. */
function Rules({
  rules,
}: {
  rules: { rule: string; grantable: boolean; description: string }[];
}) {
  const [open, setOpen] = useState(false);
  const grantable = rules.filter((r) => r.grantable);
  const never = rules.filter((r) => !r.grantable);
  const list = (items: typeof rules) => (
    <dl className="flex flex-col text-[13px]">
      {items.map((r) => (
        <div
          key={r.rule}
          className="grid grid-cols-[180px_minmax(0,1fr)] gap-3 border-t border-subtle px-4 py-2 max-sm:grid-cols-1 max-sm:gap-0.5"
        >
          <dt className="font-mono text-xs leading-5 text-fg">{r.rule}</dt>
          <dd className="min-w-0 text-fg/85">{r.description}</dd>
        </div>
      ))}
    </dl>
  );
  const subhead = (title: string, sum: string) => (
    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 border-t border-subtle px-4 pb-2 pt-3">
      <h3 className="text-[13px] font-semibold text-fg">{title}</h3>
      <span className="text-xs text-muted">{sum}</span>
    </div>
  );
  return (
    <section id="rules" tabIndex={-1} aria-labelledby="rules-h" className={card}>
      <CardHead id="rules-h" title="Rules" sum="what every deploy is checked against" />
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 px-4 py-2.5">
        <span className="text-[13px] text-muted">
          {grantable.length} an operator can grant an exception for · {never.length} never granted
        </span>
        <button
          type="button"
          className="touch-target inline-flex h-[30px] items-center gap-1.5 rounded-md px-2 text-xs font-medium text-fg hover:bg-raised"
          aria-expanded={open}
          aria-controls="rules-list"
          onClick={() => setOpen((o) => !o)}
        >
          <ChevronRight
            className={cn("h-3.5 w-3.5 motion-safe:transition-transform", open && "rotate-90")}
            aria-hidden="true"
          />
          {open ? `Hide the ${rules.length} rules` : `Show the ${rules.length} rules`}
        </button>
      </div>
      <div id="rules-list" hidden={!open}>
        {subhead("An operator can grant an exception", String(grantable.length))}
        {list(grantable)}
        {subhead("Never granted", `${never.length} · they protect routing, other tenants and the control plane`)}
        {list(never)}
      </div>
    </section>
  );
}

class Cancelled extends Error {}

function RevokeDialog({
  target,
  mode,
  onClose,
}: {
  target: DeployPolicyReportEntry | null;
  mode: DeployPolicyMode;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const reauth = useReauth();
  const [reason, setReason] = useState("");
  const revoke = useMutation({
    // Revoking needs the operator's password confirmed (D-53).
    mutationFn: async () => {
      if (!(await reauth(REVOKE_EXCEPTION_WHY))) throw new Cancelled();
      return deployPolicyService.revokeException(target!.service_id, reason.trim());
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY });
      toast.success(
        `Exception for ${target ? nameOf(target) : "the service"} revoked`,
      );
      close();
    },
  });
  const close = () => {
    onClose();
    setReason("");
    revoke.reset();
  };
  return (
    <Dialog
      open={target !== null}
      onOpenChange={(o) => !o && !revoke.isPending && close()}
    >
      <DialogContent
        role="alertdialog"
        className={dialogContent}
        showClose={false}
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            revoke.mutate();
          }}
        >
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={dialogTitle}>
              Revoke the exception for {target ? nameOf(target) : ""}?
            </DialogTitle>
            <DialogDescription className={dialogText}>
              Its next deploy is checked against{" "}
              <span className="font-mono text-xs text-fg">
                {target?.allowed_rules.join(", ")}
              </span>{" "}
              again.{" "}
              {mode === "warn"
                ? "Under warn mode it still deploys, with a warning; under enforce mode it is refused."
                : "Under enforce mode, that deploy is refused."}{" "}
              The running containers are not touched.
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="rv-reason" className={fieldLabel}>
              Reason{" "}
              <span className="font-normal text-muted">
                optional, saved in the audit log
              </span>
            </label>
            <textarea
              id="rv-reason"
              rows={2}
              maxLength={500}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              className="rounded-lg border border-strong bg-ground px-2.5 py-2 text-[13px] text-fg"
            />
          </div>
          {revoke.error && !(revoke.error instanceof Cancelled) && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {revoke.error instanceof Error
                ? revoke.error.message
                : "The exception was not revoked."}
            </p>
          )}
          <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
            <button
              type="button"
              className={cancelButton}
              disabled={revoke.isPending}
              onClick={close}
            >
              Cancel
            </button>
            <button
              type="submit"
              className={dangerButton}
              disabled={revoke.isPending}
            >
              {revoke.isPending ? "Revoking…" : "Revoke exception"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
