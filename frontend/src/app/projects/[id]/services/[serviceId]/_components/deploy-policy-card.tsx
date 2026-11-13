"use client";

import { useState } from "react";
import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check } from "lucide-react";
import { GRANT_EXCEPTION_WHY, REVOKE_EXCEPTION_WHY, useReauth } from "@/components/reauth";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { PolicyViolationList } from "@/components/ui/policy-violations";
import { ApiError, changedSinceRead } from "@/lib/api/client";
import {
  deployPolicyService,
  type PolicyViolation,
  type ServiceDeployPolicy,
} from "@/lib/api/deploypolicy.service";
import { cn, formatDateTime } from "@/lib/utils";

export const deployPolicyQueryKey = (serviceId: string) =>
  ["deploy-policy", serviceId] as const;

/** The service's deploy policy status, shared by this card and the settings
 *  tab's Domain routing card (which reads compose_services from it). */
export const deployPolicyQuery = (serviceId: string) =>
  queryOptions({
    queryKey: deployPolicyQueryKey(serviceId),
    queryFn: () => deployPolicyService.getForService(serviceId),
    staleTime: 15_000,
    retry: false,
  });

/** The chip in the Deploy policy card's header. */
export function policyChip(
  policy: ServiceDeployPolicy | undefined,
): { text: string; tone: "running" | "pending" | "failed" | "neutral" } | null {
  if (!policy) return null;
  if (pendingValues(policy).length > 0 && policy.violations.every((v) => v.state === "pending"))
    return { text: "Waiting for an operator", tone: "pending" };
  if (policy.violations.length > 0)
    return policy.mode === "warn"
      ? { text: "Would be refused", tone: "pending" }
      : { text: "Refused", tone: "failed" };
  if (policy.compose_checked_at_deploy)
    return { text: "Checked at deploy", tone: "neutral" };
  if (policy.allowed.length > 0)
    return { text: "Passes with an exception", tone: "running" };
  return { text: "Passes", tone: "running" };
}

/** Every violation the card knows of: the saved configuration's, and for a
 *  compose file from a repository what its last deploy found. */
const allViolations = (p: ServiceDeployPolicy): PolicyViolation[] => [
  ...p.allowed,
  ...p.violations,
  ...(p.last_deploy_check?.violations ?? []),
];

/** Values the exception approved, and values of its rules it didn't (D-124). */
const pinnedRows = (p: ServiceDeployPolicy) =>
  allViolations(p).filter((v) => v.state === "pinned" || v.state === "pending");

const pendingValues = (p: ServiceDeployPolicy) =>
  pinnedRows(p).filter((v) => v.state === "pending");

/** What a value runs with: the compose service and its image, or the
 *  service's image or repository. */
const runs = (v: { service?: string; image?: string }) =>
  [
    v.service,
    v.image && v.service && !v.image.startsWith("build ")
      ? `image ${v.image}`
      : v.image,
  ]
    .filter(Boolean)
    .join(" · ");

/**
 * The service's deploy policy status (D-19): what its saved configuration
 * would be refused for on the next deploy, its exception, and, for instance
 * operators, the dialog that grants or revokes one. The body of the Settings
 * tab's Deploy policy card.
 */
export function DeployPolicyBody({
  serviceId,
  name,
}: {
  serviceId: string;
  name: string;
}) {
  const [dialogOpen, setDialogOpen] = useState(false);
  const {
    data: policy,
    isLoading,
    isError,
  } = useQuery(deployPolicyQuery(serviceId));
  const current = policy ? pinnedRows(policy) : [];
  // Values the exception approved that the service no longer has, so a
  // replaced value shows what it replaced.
  const replaced: PolicyViolation[] = (policy?.exception?.allowed_values ?? []).filter(
    (a) =>
      !current.some(
        (r) =>
          r.state === "pinned" &&
          r.rule === a.rule &&
          r.service === a.service &&
          r.field === a.field &&
          r.value === a.value &&
          r.image === a.image,
      ),
  );
  const rows = [...current, ...replaced];
  const pending = policy ? pendingValues(policy).length : 0;
  const legacy = policy?.exception != null && policy.exception.allowed_values === null;
  // A value in the table below isn't listed again with the violations.
  const refused = policy ? policy.violations.filter((v) => v.state !== "pending") : [];

  return (
    <div className="flex flex-col gap-3 text-[13px]">
      <p className="leading-5 text-fg/85">
        Every deploy is checked for settings that give a container power over
        its server: privileged mode, added capabilities, devices, host
        namespaces, the Docker socket, server folders, commands on the host and
        reserved labels.
      </p>
      {isLoading && <p className="text-muted">Checking…</p>}
      {isError && (
        <p role="alert" className="text-status-failed-fg">
          Could not load the deploy policy status.
        </p>
      )}
      {policy && (
        <>
          <dl className="overflow-hidden rounded-lg border border-subtle">
            {[
              [
                "Mode",
                <>
                  <span className="font-mono">{policy.mode}</span>:{" "}
                  {policy.mode === "warn"
                    ? "violations are logged, the deploy goes ahead"
                    : "a deploy that breaks a rule is refused"}
                </>,
              ],
              [
                "Saved settings",
                policy.compose_checked_at_deploy
                  ? "The compose file comes from the repository, so it is checked at each deploy, after the clone."
                  : pending > 0 && refused.length === 0
                    ? `${pending === 1 ? "One value isn't" : `${pending} values aren't`} approved yet; everything else passes.`
                    : refused.length === 0
                    ? policy.allowed.length > 0
                      ? "Pass every rule the exception doesn't allow."
                      : "Pass every rule."
                    : `Break ${refused.length} ${refused.length === 1 ? "rule" : "rules"}: see below.`,
              ],
              [
                "Exception",
                policy.exception ? (
                  <>
                    Allows{" "}
                    <span className="font-mono">
                      {policy.exception.allowed_rules.join(", ")}
                    </span>
                    , granted by{" "}
                    {policy.exception.granted_by_name ||
                      policy.exception.granted_by_email ||
                      "an operator"}{" "}
                    on{" "}
                    <span className="font-mono">
                      {policy.exception.updated_at.slice(0, 10)}
                    </span>
                    : {policy.exception.reason}
                  </>
                ) : (
                  "None granted"
                ),
              ],
            ].map(([k, v], i) => (
              <div
                key={k as string}
                className={cn(
                  "grid grid-cols-[120px_1fr] gap-3 px-3 py-2.5",
                  i > 0 && "border-t border-subtle",
                )}
              >
                <dt className="text-muted">{k}</dt>
                <dd className="text-fg/85">{v}</dd>
              </div>
            ))}
          </dl>

          {pending > 0 && policy.exception && (
            <p
              role="status"
              className="flex items-start gap-2 rounded-lg border border-status-pending/40 bg-status-pending-soft px-3 py-2.5 leading-5 text-fg"
            >
              <AlertTriangle
                className="mt-0.5 h-[15px] w-[15px] flex-none text-status-pending-fg"
                aria-hidden="true"
              />
              <span>
                {pending === 1 ? "One value" : `${pending} values`} changed after
                the exception was approved on{" "}
                <span className="font-mono text-xs">
                  {policy.exception.updated_at.slice(0, 10)}
                </span>
                . Deploys of {name}{" "}
                {policy.mode === "warn" ? "would be refused in enforce mode" : "are refused"}{" "}
                until an instance operator approves it.
              </span>
            </p>
          )}
          {legacy && (
            <p className="rounded-lg border border-subtle px-3 py-2.5 leading-5 text-fg/85">
              Covers any value: granted before v0.25.0. Grant it again to pin it
              to today&apos;s values, so a changed path, image or command needs
              an operator again.
            </p>
          )}
          {!legacy && current.length > 0 && (
            <div className="flex flex-col gap-2">
              <h3 className="text-[13px] font-semibold text-fg">
                Values for the allowed rules
              </h3>
              <div role="group" tabIndex={0} aria-label="Values for the allowed rules" className="overflow-x-auto rounded-lg border border-subtle">
                <table className="w-full min-w-[640px] border-collapse">
                  <thead>
                    <tr className="text-left text-xs text-muted">
                      {["State", "Rule", "Value", "Runs"].map((h) => (
                        <th
                          key={h}
                          scope="col"
                          className="border-b border-subtle px-3 py-2 font-medium"
                        >
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((v, i) => (
                      <tr key={`${v.rule}-${v.field}-${v.value}-${v.image}-${i}`}>
                        <td className="whitespace-nowrap border-t border-subtle px-3 py-2">
                          {v.state === "pinned" ? (
                            <span className="inline-flex items-center gap-1.5 text-status-running-fg">
                              <Check className="h-[13px] w-[13px]" aria-hidden="true" />
                              Approved
                            </span>
                          ) : v.state === "pending" ? (
                            <span className="inline-flex items-center gap-1.5 text-status-pending-fg">
                              <AlertTriangle className="h-[13px] w-[13px]" aria-hidden="true" />
                              Not approved
                            </span>
                          ) : (
                            <span className="text-muted">Approved, not used now</span>
                          )}
                        </td>
                        <td className="border-t border-subtle px-3 py-2 font-mono text-xs text-fg">
                          {v.rule}
                        </td>
                        <td className="border-t border-subtle px-3 py-2">
                          <span className="flex flex-col">
                            <span className="whitespace-pre-wrap break-all font-mono text-xs text-fg">
                              {v.value}
                            </span>
                            <span className="text-xs text-muted">{v.field}</span>
                          </span>
                        </td>
                        <td className="break-all border-t border-subtle px-3 py-2 font-mono text-xs text-fg/85">
                          {runs(v)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className="text-xs leading-[17px] text-muted">
                A deploy with another value for these rules, another image or
                another repository is refused until an operator approves it.
                Saving it is not.
              </p>
            </div>
          )}

          {policy.control_plane_restricted && (
            <p className="rounded-lg border border-subtle bg-raised/50 px-3 py-2.5 leading-5 text-fg/85">
              {name} has no server of its own, so it deploys to the
              control-plane server. Until app networks are isolated, only
              instance operators can deploy it there, and git-push deploys are
              refused. An operator can allow it with the{" "}
              <span className="font-mono">control_plane</span> exception.
            </p>
          )}
          <PolicyViolationList
            violations={refused}
            tone={policy.mode === "warn" ? "warning" : "refused"}
            title={
              policy.mode === "warn"
                ? "Would be refused in enforce mode"
                : "The next deploy will be refused"
            }
          />
          {policy.exception && (
            <PolicyViolationList
              violations={policy.allowed.filter((v) => v.state !== "pinned")}
              tone="allowed"
              title="Allowed by the exception"
            />
          )}
          {policy.invalid_env_keys.length > 0 && (
            <p className="text-status-pending-fg">
              Variable names to fix (letters, digits and _ only, not starting
              with a digit):{" "}
              <span className="font-mono">
                {policy.invalid_env_keys.join(", ")}
              </span>
            </p>
          )}
          <div className="flex flex-wrap items-center gap-3">
            {policy.can_manage_exceptions && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => setDialogOpen(true)}
              >
                {!policy.exception
                  ? "Grant exception…"
                  : legacy
                    ? "Grant it again…"
                    : pending > 0
                      ? "Approve the changed value…"
                      : "Change or revoke the exception…"}
              </Button>
            )}
            <span className="text-xs text-muted">
              Shown to instance operators only. Grants and their reasons go to
              the audit log.
            </span>
          </div>
        </>
      )}

      {policy?.can_manage_exceptions && (
        <ExceptionDialog
          key={String(dialogOpen)}
          open={dialogOpen}
          onOpenChange={setDialogOpen}
          serviceId={serviceId}
          name={name}
          policy={policy}
        />
      )}
    </div>
  );
}

/** The password prompt was dismissed: nothing to report. */
class Cancelled extends Error {}

function ExceptionDialog({
  open,
  onOpenChange,
  serviceId,
  name,
  policy,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  serviceId: string;
  name: string;
  policy: ServiceDeployPolicy;
}) {
  const queryClient = useQueryClient();
  // Granting and revoking need the operator's password confirmed (D-53).
  const reauth = useReauth();
  const grantable = policy.rules.filter((r) => r.grantable);
  // The current exception's rules, or else the rules the saved
  // configuration breaks.
  const [selected, setSelected] = useState<Set<string>>(() => {
    if (policy.exception) return new Set(policy.exception.allowed_rules);
    return new Set(
      policy.violations
        .map((v) => v.rule)
        .filter((r) => grantable.some((g) => g.rule === r)),
    );
  });
  const [reason, setReason] = useState(policy.exception?.reason ?? "");
  // The read the rules were chosen on: a grant sends its version, and a new
  // read keeps what was changed since it.
  const [read, setRead] = useState(policy);
  const [conflict, setConflict] = useState(false);
  const readAgain = async () => {
    const fresh = await queryClient
      .fetchQuery({ ...deployPolicyQuery(serviceId), staleTime: 0 })
      .catch(() => null);
    if (!fresh) {
      toast.error("Couldn’t read the exception. Try again.");
      return;
    }
    const before = read.exception?.allowed_rules ?? [];
    const added = [...selected].filter((r) => !before.includes(r));
    const removed = before.filter((r) => !selected.has(r));
    setSelected(
      new Set([
        ...(fresh.exception?.allowed_rules ?? []).filter((r) => !removed.includes(r)),
        ...added,
      ]),
    );
    setRead(fresh);
    setConflict(false);
  };

  const done = (message: string) => {
    toast.success(message);
    queryClient.invalidateQueries({
      queryKey: deployPolicyQueryKey(serviceId),
    });
    onOpenChange(false);
  };
  const grant = useMutation({
    mutationFn: async () => {
      if (!(await reauth(GRANT_EXCEPTION_WHY))) throw new Cancelled();
      return deployPolicyService.grantException(
        serviceId,
        Array.from(selected),
        reason.trim(),
        policy.values_digest,
        read.version,
      );
    },
    onSuccess: () => done("Exception saved and recorded in the audit log"),
    onError: (err: Error) => {
      if (err instanceof Cancelled) return;
      if (changedSinceRead(err)) {
        setConflict(true);
        return;
      }
      toast.error(err.message ?? "Failed to grant the exception");
      // values_changed: reload, so the dialog lists the values to approve.
      if (err instanceof ApiError && err.status === 409)
        queryClient.invalidateQueries({ queryKey: deployPolicyQueryKey(serviceId) });
    },
  });
  const revoke = useMutation({
    mutationFn: async () => {
      if (!(await reauth(REVOKE_EXCEPTION_WHY))) throw new Cancelled();
      return deployPolicyService.revokeException(serviceId, reason.trim());
    },
    onSuccess: () => done("Exception revoked"),
    onError: (err: Error) => {
      if (!(err instanceof Cancelled))
        toast.error(err.message ?? "Failed to revoke the exception");
    },
  });
  const busy = grant.isPending || revoke.isPending;
  // What granting pins (D-124), as the server will: the current values of
  // the selected rules. control_plane and net_raw have none.
  const fromRepo = policy.compose_checked_at_deploy;
  const seen = new Set<string>();
  const values = allViolations(policy).filter((v) => {
    const key = `${v.rule}\u0000${v.service}\u0000${v.value}\u0000${v.image}`;
    if (!v.value || !selected.has(v.rule) || seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  const check = policy.last_deploy_check;
  const toggle = (rule: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(rule)) next.delete(rule);
      else next.add(rule);
      return next;
    });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Deploy policy exception for {name}</DialogTitle>
          <DialogDescription>
            Rules you allow stop blocking {name}&apos;s deploys. Each one gives
            it more power over the server it runs on, so allow only what it
            needs. Reserved labels, files outside the project and the other
            integrity rules can never be allowed.
          </DialogDescription>
        </DialogHeader>
        <fieldset className="flex flex-col">
          <legend className="mb-2 text-[13px] font-medium text-fg">
            Allowed rules · {selected.size} of {grantable.length} selected
          </legend>
          <div className="max-h-72 overflow-y-auto rounded-lg border border-subtle">
            {grantable.map((r, i) => (
              <label
                key={r.rule}
                className={cn(
                  "flex cursor-pointer items-start gap-2.5 px-3 py-2 text-[13px]",
                  i > 0 && "border-t border-subtle",
                  selected.has(r.rule) && "bg-accent-soft",
                )}
              >
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 accent-[var(--link)]"
                  checked={selected.has(r.rule)}
                  onChange={() => toggle(r.rule)}
                  disabled={busy}
                />
                <span className="flex flex-col">
                  <span className="font-mono text-xs text-fg">{r.rule}</span>
                  <span className="text-xs text-muted">{r.description}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <div className="mt-1.5 flex flex-col gap-1.5">
          <span className="text-[13px] font-medium text-fg">
            Values it approves
          </span>
          <span className="text-xs leading-[17px] text-muted">
            {fromRepo
              ? check
                ? `${name}'s compose file is read from the repository at deploy, so these are what its last deploy found (${check.commit_sha.slice(0, 7) || "unknown commit"}, ${formatDateTime(check.checked_at)}).`
                : `${name}'s compose file is read from the repository at deploy, and it hasn't deployed since v0.25.0. Deploy it once, then approve what the deploy found.`
              : `${name}'s saved settings now, for the rules you allow. A deploy with another value, image or repository is refused until an operator approves it again.`}
            {policy.exception?.allowed_values?.length
              ? " Values approved before stay approved while you allow their rule."
              : ""}
          </span>
          {values.length > 0 ? (
            <ul tabIndex={0} aria-label="Values it approves" className="flex max-h-48 flex-col overflow-y-auto rounded-lg border border-subtle">
              {values.map((v, i) => (
                <li
                  key={`${v.rule}-${v.field}-${v.value}-${i}`}
                  className={cn(
                    "flex flex-wrap gap-x-3 gap-y-0.5 px-3 py-2.5 text-[13px]",
                    i > 0 && "border-t border-subtle",
                  )}
                >
                  <span className="flex-[0_0_130px] font-mono text-xs text-fg/70">
                    {v.rule}
                  </span>
                  <span className="flex min-w-0 flex-[1_1_260px] flex-col">
                    <span className="whitespace-pre-wrap break-all font-mono text-xs text-fg">
                      {v.value}
                    </span>
                    <span className="text-xs text-muted">{runs(v)}</span>
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <span className="rounded-lg border border-subtle px-3 py-2 text-xs text-muted">
              None for the rules selected.
            </span>
          )}
        </div>
        <div className="mt-1.5 flex flex-col gap-1.5">
          <label
            htmlFor="exception-reason"
            className="text-[13px] font-medium text-fg"
          >
            Reason, written to the audit log
          </label>
          <Textarea
            id="exception-reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            maxLength={1000}
            disabled={busy}
            rows={2}
          />
          <p className="text-xs text-muted">
            For example: monitoring reads container stats from the Docker
            socket.
          </p>
        </div>
        {conflict && (
          <div role="alert" className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
            <p className="min-w-0 flex-[1_1_320px]">
              This exception changed since you opened it, so nothing was saved. What you chose is still here.
            </p>
            <Button type="button" variant="outline" size="sm" onClick={() => void readAgain()}>
              Read it again
            </Button>
          </div>
        )}
        <DialogFooter className="flex-wrap">
          {policy.exception && (
            <Button
              variant="destructive"
              onClick={() => revoke.mutate()}
              disabled={busy}
              className="mr-auto"
            >
              {revoke.isPending ? "Revoking…" : "Revoke exception"}
            </Button>
          )}
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            Cancel
          </Button>
          <Button
            onClick={() => grant.mutate()}
            disabled={busy || conflict || selected.size === 0 || !reason.trim()}
          >
            {grant.isPending
              ? "Saving…"
              : policy.exception
                ? "Update exception"
                : "Grant exception"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
