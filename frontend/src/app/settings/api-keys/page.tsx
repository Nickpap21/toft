"use client";

import { Suspense, useState, useSyncExternalStore } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Copy, Plus } from "lucide-react";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { BASE_URL, apiOrigin } from "@/lib/api/client";
import {
  apiKeyService,
  type APIKey,
  type APIKeyScope,
  type CreatedAPIKey,
} from "@/lib/api/apikey.service";
import { gitInfoQuery } from "@/lib/api/code.service";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import {
  environmentsQuery,
  projectServicesQuery,
  projectsQuery,
} from "@/lib/queries";
import { sinceShort, untilHint } from "@/lib/time";
import { useQueryFlag } from "@/lib/use-query-flag";
import { cn, formatDateTime } from "@/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  SectionHead,
  SettingsFrame,
  cancelButton,
  card,
  choice,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  help,
  primaryButton,
  phoneLabel,
  phoneNote,
  phoneRow,
  phoneTd,
  smallButton,
  th,
} from "@/components/settings/settings-frame";

const SCOPE_LABEL: Record<APIKeyScope, string> = {
  deploy: "Deploy, follow and roll back",
  variables: "Variables and secrets",
  code_read: "Clone and fetch its code",
  code_write: "Push to its code",
  checks: "Report checks from CI",
  issues_read: "Read its issues",
  issues_write: "Open and change its issues",
  docs_read: "Read its docs",
  docs_write: "Write its docs",
};

/** "Code, checks, issues and docs": what a key for some services can't
 *  have, of the scopes this instance offers. */
function projectWide(choices: APIKeyScope[]): string {
  const words = [
    ...new Set(
      choices
        .filter((sc) => !SERVICE_SCOPES.includes(sc))
        .map((sc) => sc.replace(/_(read|write)$/, "")),
    ),
  ];
  const s = words.length > 1 ? `${words.slice(0, -1).join(", ")} and ${words.at(-1)}` : words.join("");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

const access = (k: APIKey) => (k.project_id ? "One project" : "Full account");

/** The scopes that act on one service: all a key for some services may have. */
const SERVICE_SCOPES: APIKeyScope[] = ["deploy", "variables"];

/** A key for some services: their names, and how many were deleted since. */
function serviceNames(k: APIKey): string {
  const names = k.services.map((s) => s.name);
  const gone = k.service_ids.length - k.services.length;
  if (gone > 0) names.push(gone === 1 ? "1 deleted service" : `${gone} deleted services`);
  return names.join(", ");
}

const SOON = 14 * 86_400_000;

/** A key's expiry as the list shows it (K4): soon is within 14 days. */
function expiry(expiresAt: string | null, now: number) {
  if (!expiresAt) return { tone: "never" as const, date: "", note: "" };
  const date = formatDateTime(expiresAt).slice(0, 10);
  const left = new Date(expiresAt).getTime() - now;
  if (left <= 0) return { tone: "expired" as const, date, note: `on ${date}` };
  return {
    tone: left <= SOON ? ("soon" as const) : ("ok" as const),
    date,
    note: untilHint(expiresAt, now),
  };
}

const EXPIRY_CHOICES = [
  { value: "30", label: "In 30 days", days: 30 },
  { value: "90", label: "In 90 days", days: 90 },
  { value: "365", label: "In 1 year", days: 365 },
  { value: "date", label: "On a date…", days: 0 },
  { value: "never", label: "Never", days: 0 },
] as const;
type ExpiryChoice = (typeof EXPIRY_CHOICES)[number]["value"];

/** YYYY-MM-DD in the viewer's time zone. */
const localDate = (t: number) => {
  const d = new Date(t);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};

const noop = () => () => {};

/** The API's absolute origin; BASE_URL ("" when same-origin) while prerendering. */
function useApiOrigin(): string {
  return useSyncExternalStore(noop, apiOrigin, () => BASE_URL);
}

/** Settings → API keys (board: ApiKeys): the keys you created, a dialog to
 *  create one, and what a key may call. */
export default function ApiKeysPage() {
  // useQueryFlag reads the URL: a boundary on a page Next prerenders.
  return (
    <Suspense>
      <ApiKeys />
    </Suspense>
  );
}

function ApiKeys() {
  const git = useQuery(gitInfoQuery);
  const { user } = useAuth();
  const api = useApiOrigin();
  const [now] = useState(() => Date.now());
  const [creating, setCreating] = useQueryFlag("new");
  const [created, setCreated] = useState<CreatedAPIKey | null>(null);
  const [revoking, setRevoking] = useState<APIKey | null>(null);
  const keys = useQuery({
    queryKey: ["api-keys"],
    queryFn: () => apiKeyService.list(),
  });
  const workspace = user?.orgName ?? "";
  const actsIn = (k: APIKey) =>
    k.project_id
      ? k.service_ids.length > 0
        ? {
            main: `${k.project_name ?? ""} · ${serviceNames(k)}`,
            note: `${k.org_name ?? ""} · only these services`,
          }
        : { main: k.project_name ?? "", note: k.org_name ?? "" }
      : { main: "Your default workspace", note: "the last you switched to" };

  return (
    <SettingsFrame
      title="API keys"
      gap="gap-6"
      action={
        <button
          type="button"
          className={primaryButton}
          onClick={() => setCreating(true)}
        >
          <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
          New API key
        </button>
      }
    >
      <p className="max-w-[760px] text-[13px] leading-[19px] text-fg/70">
        Keys let scripts and CI call the Toft API as you. A key never does more
        than your role allows, and stops working if you leave the workspace or
        your account is deactivated. Each key is shown once, when it is created,
        and expires on the date you choose, unless you choose Never.{" "}
        The full reference, with every call and a place to try them with your key, is at{" "}
        <a href={`${api}/api/v1/docs`} target="_blank" rel="noreferrer" className="inline-link">
          {api.replace(/^https?:\/\//, "")}/api/v1/docs
        </a>
        .
      </p>

      <section aria-labelledby="keys-h" className="flex flex-col gap-3">
        <SectionHead
          id="keys-h"
          title="Keys you created"
          count={keys.data?.length ?? "–"}
          note="Only you see these. Revoking one takes effect at once."
        />
        <div className={cn(card, "overflow-x-auto")}>
          <table className="w-full border-collapse text-[13px] max-sm:block sm:min-w-[1000px]">
            <thead className="max-sm:hidden">
              <tr>
                <th scope="col" className={th}>
                  Name
                </th>
                <th scope="col" className={th}>
                  Access
                </th>
                <th scope="col" className={th}>
                  Acts in
                </th>
                <th scope="col" className={th}>
                  Created
                </th>
                <th scope="col" className={th}>
                  Expires
                </th>
                <th scope="col" className={th}>
                  Last used
                </th>
                <th scope="col" className={th}>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="max-sm:block">
              {keys.isLoading ? (
                <tr aria-busy="true" className="max-sm:block">
                  <td colSpan={7} className={cn(phoneNote, "py-3.5")}>
                    <span
                      className="flex flex-col gap-2.5 motion-safe:animate-pulse"
                      aria-label="Loading"
                    >
                      <span className="block h-2.5 w-[62%] rounded bg-raised" />
                      <span className="block h-2.5 w-[44%] rounded bg-raised" />
                    </span>
                  </td>
                </tr>
              ) : keys.error ? (
                <tr className="max-sm:block">
                  <td colSpan={7} role="alert" className={cn(phoneNote, "py-5")}>
                    <span className="flex flex-wrap items-center justify-between gap-3">
                      <span className="flex items-center gap-2.5 text-status-failed-fg">
                        <AlertTriangle
                          className="h-4 w-4 flex-none"
                          aria-hidden="true"
                        />
                        Could not load your API keys:{" "}
                        {keys.error instanceof Error
                          ? keys.error.message
                          : "unknown error"}
                      </span>
                      <button
                        type="button"
                        className={cn(
                          smallButton,
                          "h-8 px-3 text-[13px] font-medium rounded-lg",
                        )}
                        onClick={() => keys.refetch()}
                      >
                        Retry
                      </button>
                    </span>
                  </td>
                </tr>
              ) : keys.data?.length === 0 ? (
                <tr className="max-sm:block">
                  <td colSpan={7} className={cn(phoneNote, "py-7")}>
                    <span className="flex flex-wrap items-center justify-between gap-3">
                      <span className="flex flex-col gap-0.5">
                        <span className="font-medium text-fg">
                          No API keys yet
                        </span>
                        <span className="text-fg/70">
                          Create one for a CI job or a script. For CI, a project
                          key is enough.
                        </span>
                      </span>
                      <button
                        type="button"
                        className={cn(
                          smallButton,
                          "h-8 px-3 text-[13px] font-medium rounded-lg",
                        )}
                        onClick={() => setCreating(true)}
                      >
                        New API key
                      </button>
                    </span>
                  </td>
                </tr>
              ) : (
                keys.data?.map((k) => {
                  const acts = actsIn(k);
                  const exp = expiry(k.expires_at, now);
                  return (
                    <tr key={k.id} className={phoneRow}>
                      <td className={cn(phoneTd, "max-sm:col-start-1 max-sm:row-start-1")}>
                        <span className="flex flex-col">
                          <span className="font-medium text-fg">{k.name}</span>
                          <span className="font-mono text-xs leading-5 text-muted">
                            {k.prefix}…
                          </span>
                        </span>
                      </td>
                      <td className={cn(phoneTd, "max-sm:col-span-2")}>
                        <span className="flex flex-wrap items-center gap-1.5">
                          <span className="whitespace-nowrap rounded-sm border border-subtle px-[7px] text-xs leading-[22px] text-fg">
                            {access(k)}
                          </span>
                          {k.scopes.length > 0 && (
                            <span className="font-mono text-xs leading-5 text-fg/85">
                              {k.scopes.join(", ")}
                            </span>
                          )}
                        </span>
                      </td>
                      <td className={cn(phoneTd, "text-fg/85 max-sm:col-span-2")}>
                        <span className="flex flex-col">
                          <span>{acts.main}</span>
                          <span className="text-xs leading-5 text-muted">
                            {acts.note}
                          </span>
                        </span>
                      </td>
                      <td className={cn(phoneTd, "font-mono text-xs text-fg/70 max-sm:col-span-2")}>
                        <span className={cn(phoneLabel, "font-sans")}>Created </span>
                        {formatDateTime(k.created_at).slice(0, 10)}
                      </td>
                      <td className={cn(phoneTd, "max-sm:col-span-2")}>
                        <span className="flex flex-col">
                          <span
                            className={cn(
                              "font-mono text-xs",
                              exp.tone === "expired"
                                ? "text-status-failed-fg"
                                : exp.tone === "soon"
                                  ? "text-status-pending-fg"
                                  : exp.tone === "never"
                                    ? "text-fg/70"
                                    : "text-fg/85",
                            )}
                          >
                            {exp.tone !== "expired" && <span className={cn(phoneLabel, "font-sans")}>Expires </span>}
                            {exp.tone === "never"
                              ? "Never"
                              : exp.tone === "expired"
                                ? "Expired"
                                : exp.date}
                          </span>
                          {exp.note && (
                            <span
                              className={cn(
                                "text-xs leading-5",
                                exp.tone === "soon"
                                  ? "text-status-pending-fg"
                                  : "text-muted",
                              )}
                            >
                              {exp.note}
                            </span>
                          )}
                        </span>
                      </td>
                      <td
                        className={cn(
                          phoneTd,
                          "max-sm:col-span-2",
                          k.last_used_at ? "text-fg/85" : "text-muted",
                        )}
                      >
                        {k.last_used_at ? (
                          <span title={formatDateTime(k.last_used_at)}>
                            <span className={phoneLabel}>Last used </span>
                            {sinceShort(k.last_used_at, now)}
                          </span>
                        ) : (
                          "Never used"
                        )}
                      </td>
                      <td className={cn(phoneTd, "py-1.5 text-right max-sm:col-start-2 max-sm:row-start-1")}>
                        <button
                          type="button"
                          className={smallButton}
                          aria-label={`Revoke ${k.name}`}
                          onClick={() => setRevoking(k)}
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
        </div>
        <p className="text-xs leading-[17px] text-muted">
          Full account keys act in the workspace you last switched to, unless
          a request names one with the <code className="font-mono">X-Toft-Org</code>{" "}
          header. A project key stays bound to its project.
        </p>
      </section>

      <div className="flex flex-wrap items-start gap-6">
        <UseAKey org={user?.orgId ?? "<workspace-id>"} />
        <section
          aria-labelledby="scope-h"
          className={cn(
            card,
            "flex min-w-0 flex-[1_1_420px] flex-col gap-2.5 p-4",
          )}
        >
          <h2
            id="scope-h"
            className="text-[15px] font-semibold leading-[22px] text-fg"
          >
            What a project key may call
          </h2>
          {readFailed(git) && (
            <LoadFailed
              what="the code scopes"
              onRetry={() => void git.refetch()}
              error={git.error}
            />
          )}
          <dl className="flex flex-col text-[13px]">
            <div className="flex flex-wrap gap-x-3 gap-y-1 py-2">
              <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                deploy
              </dt>
              <dd className="flex-[1_1_220px] text-fg/85">
                Deploy the project&apos;s services, follow their logs and roll
                back.
              </dd>
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
              <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                variables
              </dt>
              <dd className="flex-[1_1_220px] text-fg/85">
                Read and change variables, and set secrets, on its services and
                environments. Secrets are never read back.
              </dd>
            </div>
            {git.data?.enabled && (
              <>
                <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
                  <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                    code_read
                  </dt>
                  <dd className="flex-[1_1_220px] text-fg/85">
                    Clone and fetch the project&apos;s repository over HTTPS,
                    with the key as the password.
                  </dd>
                </div>
                <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
                  <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                    code_write
                  </dt>
                  <dd className="flex-[1_1_220px] text-fg/85">
                    Push to it too. The key&apos;s account must be an owner or a
                    developer.
                  </dd>
                </div>
                <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
                  <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                    checks
                  </dt>
                  <dd className="flex-[1_1_220px] text-fg/85">
                    Report a commit&apos;s checks from CI, and read them.
                    Nothing else: it can&apos;t read the code or deploy.
                  </dd>
                </div>
              </>
            )}
            <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
              <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                issues_read
              </dt>
              <dd className="flex-[1_1_220px] text-fg/85">
                Read the project&apos;s issues, their history and comments.
              </dd>
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
              <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                issues_write
              </dt>
              <dd className="flex-[1_1_220px] text-fg/85">
                Open, change and comment on them too. Saved views need a person.
              </dd>
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
              <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                docs_read
              </dt>
              <dd className="flex-[1_1_220px] text-fg/85">
                Read the project&apos;s docs and their versions.
              </dd>
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-1 border-t border-subtle py-2">
              <dt className="w-24 flex-none font-mono text-xs leading-5 text-fg">
                docs_write
              </dt>
              <dd className="flex-[1_1_220px] text-fg/85">
                Write, move, delete and verify pages too. Running a page&apos;s commands needs a person.
              </dd>
            </div>
          </dl>
          <p className="text-[13px] leading-[19px] text-fg/85">
            A key for some of the project&apos;s services deploys and sets
            variables on those only: not the environment&apos;s shared
            variables, previews or the project&apos;s code.
          </p>
          <p className="text-xs leading-[17px] text-muted">
            Anything else answers 403, including another project&apos;s
            services. Viewers can&apos;t create project keys.
          </p>
        </section>
      </div>

      <CreateDialog
        open={creating}
        workspace={workspace}
        onClose={() => setCreating(false)}
        onCreated={(k) => {
          setCreating(false);
          setCreated(k);
        }}
      />
      <CreatedDialog created={created} now={now} onClose={() => setCreated(null)} />
      <RevokeDialog
        target={revoking}
        actsIn={revoking ? actsIn(revoking) : null}
        now={now}
        onClose={() => setRevoking(null)}
      />
    </SettingsFrame>
  );
}

function CopyButton({
  text,
  label = "Copy",
  className,
  icon = true,
}: {
  text: string;
  label?: string;
  className?: string;
  icon?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className={className ?? cancelButton}
      onClick={() =>
        navigator.clipboard.writeText(text).then(
          () => setCopied(true),
          () => toast.error("Couldn't copy: select the text and copy it"),
        )
      }
    >
      {icon &&
        (copied ? (
          <Check className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
        ) : (
          <Copy className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
        ))}
      {copied ? "Copied" : label}
    </button>
  );
}

/** The CI snippet names this workspace, so a full account key deploys here
 *  whichever workspace the account switches to later (D-52). */
function UseAKey({ org }: { org: string }) {
  const api = useApiOrigin();
  const snippet = `curl -X POST \\\n  -H "Authorization: Bearer $TOFT_API_KEY" \\\n  -H "X-Toft-Org: ${org}" \\\n  ${api}/api/v1/services/<service-id>/deployments`;
  return (
    <section
      aria-labelledby="use-h"
      className={cn(card, "flex min-w-0 flex-[1_1_520px] flex-col gap-3 p-4")}
    >
      <h2
        id="use-h"
        className="text-[15px] font-semibold leading-[22px] text-fg"
      >
        Use a key
      </h2>
      <div className="overflow-hidden rounded-lg border border-subtle bg-ground">
        <div className="flex items-center gap-2 border-b border-subtle bg-raised/60 py-1.5 pl-3 pr-1.5 text-xs text-muted">
          <span className="font-mono">Deploy a service from CI</span>
          <span className="flex-1" />
          <CopyButton
            text={snippet}
            icon={false}
            className={cn(smallButton, "h-7 rounded-md")}
          />
        </div>
        <pre role="group" tabIndex={0} aria-label="Command to deploy a service from CI" className="focus-inset overflow-x-auto px-3.5 py-3 font-mono text-xs leading-5 text-fg/90">
          {snippet}
        </pre>
      </div>
      <p className="text-[13px] leading-[19px] text-fg/70">
        Send the key as a bearer token and the workspace&apos;s ID as{" "}
        <code className="font-mono">X-Toft-Org</code>. Keep the key in your
        CI&apos;s secret store, never in the repository.
      </p>
    </section>
  );
}

function CreateDialog({
  open,
  workspace,
  onClose,
  onCreated,
}: {
  open: boolean;
  workspace: string;
  onClose: () => void;
  onCreated: (k: CreatedAPIKey) => void;
}) {
  const queryClient = useQueryClient();
  const role = useOrgRole();
  const canScope = role === "owner" || role === "developer";
  const projects = useQuery({ ...projectsQuery, enabled: open });
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"project" | "full">("project");
  // Viewers can only make full account keys.
  const mode = canScope ? kind : "full";
  const [projectId, setProjectId] = useState("");
  const [scopes, setScopes] = useState<APIKeyScope[]>(["deploy", "variables"]);
  const [some, setSome] = useState(false);
  const [serviceIds, setServiceIds] = useState<string[]>([]);
  const [expires, setExpires] = useState<ExpiryChoice>("90");
  const [expiryDate, setExpiryDate] = useState("");
  const [opened] = useState(() => Date.now());
  // The code scopes only where this instance hosts git (D-71).
  const git = useQuery(gitInfoQuery);
  const scopeChoices: APIKeyScope[] = git.data?.enabled
    ? ["deploy", "variables", "code_read", "code_write", "checks", "issues_read", "issues_write", "docs_read", "docs_write"]
    : ["deploy", "variables", "issues_read", "issues_write", "docs_read", "docs_write"];
  const project = projectId || projects.data?.[0]?.id || "";
  const picking = open && mode === "project" && some;
  const services = useQuery({ ...projectServicesQuery(project), enabled: picking && !!project });
  const envs = useQuery({ ...environmentsQuery(project), enabled: picking && !!project });
  // Workspace-shared services belong to no project, so no project key reaches them.
  const own = (services.data ?? []).filter((s) => s.scope !== "org");
  const groups = [
    ...(envs.data ?? []).map((e) => ({
      name: e.name,
      services: own.filter((s) => s.environment_id === e.id),
    })),
    { name: "Shared by the project", services: own.filter((s) => s.scope === "project") },
  ].filter((g) => g.services.length > 0);
  const keyScopes = some ? scopes.filter((sc) => SERVICE_SCOPES.includes(sc)) : scopes;
  const preset = EXPIRY_CHOICES.find((c) => c.value === expires)!;
  const expiresAt =
    expires === "never"
      ? null
      : expires === "date"
        ? expiryDate
          ? new Date(`${expiryDate}T23:59:59`).toISOString()
          : ""
        : new Date(opened + preset.days * 86_400_000).toISOString();
  const valid =
    name.trim() !== "" &&
    expiresAt !== "" &&
    (mode === "full" ||
      (project !== "" && keyScopes.length > 0 && (!some || serviceIds.length > 0)));
  const create = useMutation({
    mutationFn: () =>
      apiKeyService.create(
        name.trim(),
        mode === "project"
          ? { id: project, scopes: keyScopes, serviceIds: some ? serviceIds : [] }
          : undefined,
        expiresAt,
      ),
    onSuccess: (k) => {
      queryClient.invalidateQueries({ queryKey: ["api-keys"] });
      reset();
      onCreated(k);
    },
  });
  const reset = () => {
    setName("");
    setKind("project");
    setProjectId("");
    setScopes(["deploy", "variables"]);
    setSome(false);
    setServiceIds([]);
    setExpires("90");
    setExpiryDate("");
    create.reset();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o && !create.isPending) {
          onClose();
          reset();
        }
      }}
    >
      <DialogContent className={cn(dialogContent, "max-w-[560px]")}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={dialogTitle}>New API key</DialogTitle>
            <DialogDescription className={dialogText}>
              The key acts as you, with your role in {workspace}
              {role ? ` (${role.charAt(0).toUpperCase()}${role.slice(1)})` : ""}
              . It is shown once, after you create it.
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="nk-name" className={fieldLabel}>
              Name
            </label>
            <input
              id="nk-name"
              required
              maxLength={64}
              value={name}
              onChange={(e) => setName(e.target.value)}
              aria-describedby="nk-name-help"
              className={cn(fieldInput, "font-mono")}
            />
            <span id="nk-name-help" className={help}>
              Name it after where it will live, such as the CI job. Up to 64
              characters.
            </span>
          </div>
          <fieldset className="mx-5 mt-4 flex flex-col gap-2">
            <legend className={cn(fieldLabel, "mb-2")}>Access</legend>
            <label
              className={cn(
                choice(mode === "project"),
                !canScope && "cursor-not-allowed opacity-60",
              )}
            >
              <input
                type="radio"
                name="nk-access"
                checked={mode === "project"}
                disabled={!canScope}
                onChange={() => setKind("project")}
                className="mt-[3px] h-4 w-4 flex-none accent-[var(--link)]"
              />
              <span className="flex flex-col gap-0.5">
                <span className="text-[13px] font-medium text-fg">
                  One project
                </span>
                <span className="text-xs leading-[17px] text-fg/70">
                  {canScope
                    ? "Deploys, variables or code in one project, nothing else. Stays in that project whichever workspace you have open. Best for CI."
                    : "Owners and developers only: viewers can't deploy or change variables."}
                </span>
              </span>
            </label>
            <label className={choice(mode === "full")}>
              <input
                type="radio"
                name="nk-access"
                checked={mode === "full"}
                onChange={() => setKind("full")}
                className="mt-[3px] h-4 w-4 flex-none accent-[var(--link)]"
              />
              <span className="flex flex-col gap-0.5">
                <span className="text-[13px] font-medium text-fg">
                  Full account
                </span>
                <span className="text-xs leading-[17px] text-fg/70">
                  Everything you can do, in the workspace you have open in the
                  panel (now {workspace}). If you switch workspaces, the key
                  follows.
                </span>
              </span>
            </label>
          </fieldset>
          {mode === "project" && (
            <div className="mx-5 mt-3.5 flex flex-wrap gap-x-6 gap-y-4 rounded-lg border border-subtle p-3">
              <div className="flex min-w-[200px] flex-[1_1_220px] flex-col gap-3.5">
                <div className="flex flex-col gap-1.5">
                  <label htmlFor="nk-project" className={fieldLabel}>
                    Project
                  </label>
                  <select
                    id="nk-project"
                    value={project}
                    onChange={(e) => {
                      setProjectId(e.target.value);
                      setServiceIds([]);
                    }}
                    className={fieldInput}
                  >
                    {(projects.data ?? []).map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </select>
                </div>
                <fieldset className="flex flex-col gap-1.5">
                  <legend className={cn(fieldLabel, "mb-1.5")}>Services</legend>
                  <label className="flex min-h-6 items-center gap-2 text-[13px] text-fg">
                    <input
                      type="radio"
                      name="nk-services"
                      checked={!some}
                      onChange={() => setSome(false)}
                      className="h-4 w-4 accent-[var(--link)]"
                    />
                    Every service in the project
                  </label>
                  <label className="flex min-h-6 items-center gap-2 text-[13px] text-fg">
                    <input
                      type="radio"
                      name="nk-services"
                      checked={some}
                      onChange={() => setSome(true)}
                      className="h-4 w-4 accent-[var(--link)]"
                    />
                    Only these
                  </label>
                  {some && (
                    <div className="ml-[22px] flex flex-col gap-0.5 rounded-lg border border-subtle bg-ground px-2.5 py-2">
                      {services.isLoading || envs.isLoading ? (
                        <span className="text-xs text-muted" aria-busy="true">
                          Loading services…
                        </span>
                      ) : services.error || envs.error ? (
                        <span role="alert" className="text-xs text-status-failed-fg">
                          Could not load the project&apos;s services.
                        </span>
                      ) : groups.length === 0 ? (
                        <span className="text-xs text-muted">
                          This project has no services yet.
                        </span>
                      ) : (
                        groups.map((g, i) => (
                          <div
                            key={g.name}
                            role="group"
                            aria-labelledby={`nk-env-${i}`}
                            className="flex flex-col gap-0.5"
                          >
                            <span id={`nk-env-${i}`} className={cn("text-xs text-muted", i > 0 && "mt-1")}>
                              {g.name}
                            </span>
                            {g.services.map((sv) => (
                              <label
                                key={sv.id}
                                className="flex min-h-6 items-center gap-2 text-[13px] text-fg"
                              >
                                <input
                                  type="checkbox"
                                  checked={serviceIds.includes(sv.id)}
                                  onChange={(e) =>
                                    setServiceIds(
                                      e.target.checked
                                        ? [...serviceIds, sv.id]
                                        : serviceIds.filter((x) => x !== sv.id),
                                    )
                                  }
                                  className="h-4 w-4 accent-[var(--link)]"
                                />
                                <span className="font-mono text-xs">{sv.name}</span>
                              </label>
                            ))}
                          </div>
                        ))
                      )}
                    </div>
                  )}
                </fieldset>
              </div>
              <fieldset
                aria-describedby={some ? "nk-allows-note" : undefined}
                className="flex flex-[1_1_220px] flex-col gap-1.5"
              >
                <legend className={cn(fieldLabel, "mb-1.5")}>Allows</legend>
                {scopeChoices.map((sc) => {
                  const off = some && !SERVICE_SCOPES.includes(sc);
                  return (
                    <label
                      key={sc}
                      className={cn(
                        "flex min-h-6 items-center gap-2 text-[13px]",
                        off ? "text-muted" : "text-fg",
                      )}
                    >
                      <input
                        type="checkbox"
                        checked={!off && scopes.includes(sc)}
                        disabled={off}
                        onChange={(e) =>
                          setScopes(
                            e.target.checked
                              ? [...scopes, sc]
                              : scopes.filter((x) => x !== sc),
                          )
                        }
                        className="h-4 w-4 accent-[var(--link)]"
                      />
                      {SCOPE_LABEL[sc]}
                    </label>
                  );
                })}
                {readFailed(git) && (
                  <LoadFailed
                    what="the code scopes"
                    onRetry={() => void git.refetch()}
                    error={git.error}
                  />
                )}
                {some && (
                  <span id="nk-allows-note" className="text-xs leading-[17px] text-muted">
                    {projectWide(scopeChoices)} reach the whole project, so a
                    key for some services can only deploy and set their
                    variables.
                  </span>
                )}
              </fieldset>
            </div>
          )}
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="nk-expires" className={fieldLabel}>
              Expires
            </label>
            <div className="flex flex-wrap gap-2">
              <select
                id="nk-expires"
                value={expires}
                aria-describedby="nk-expires-help"
                onChange={(e) => setExpires(e.target.value as ExpiryChoice)}
                className={cn(fieldInput, "flex-[0_1_220px]")}
              >
                {EXPIRY_CHOICES.map((c) => (
                  <option key={c.value} value={c.value}>
                    {c.label}
                  </option>
                ))}
              </select>
              {expires === "date" && (
                <input
                  type="date"
                  aria-label="Expiry date"
                  aria-describedby="nk-expires-help"
                  required
                  value={expiryDate}
                  min={localDate(opened + 86_400_000)}
                  onChange={(e) => setExpiryDate(e.target.value)}
                  className={cn(fieldInput, "font-mono")}
                />
              )}
            </div>
            {expires === "never" ? (
              <p
                id="nk-expires-help"
                role="status"
                className="flex items-start gap-2 rounded-lg border border-status-pending/40 bg-status-pending-soft px-3 py-2.5 text-[13px] text-fg"
              >
                <AlertTriangle
                  className="mt-0.5 h-[15px] w-[15px] flex-none text-status-pending-fg"
                  aria-hidden="true"
                />
                <span>
                  A key that never expires works until you revoke it. Prefer a
                  date, and make a new key before it arrives.
                </span>
              </p>
            ) : (
              <span id="nk-expires-help" className={help}>
                {expires === "date"
                  ? "At the end of that day."
                  : `On ${expiresAt ? localDate(new Date(expiresAt).getTime()) : ""}.`}{" "}
                Make a new key before then; the list shows when it is close.
              </span>
            )}
          </div>
          {create.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {create.error instanceof Error
                ? create.error.message
                : "The key was not created."}
            </p>
          )}
          <div className={dialogFooter}>
            <button
              type="button"
              className={cancelButton}
              onClick={() => {
                onClose();
                reset();
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              className={primaryButton}
              disabled={!valid || create.isPending}
            >
              {create.isPending ? "Creating…" : "Create key"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CreatedDialog({
  created,
  now,
  onClose,
}: {
  created: CreatedAPIKey | null;
  now: number;
  onClose: () => void;
}) {
  return (
    <Dialog open={created !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        className={cn(dialogContent, "max-w-[560px]")}
        showClose={false}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Copy the key for {created?.name}
          </DialogTitle>
          <DialogDescription className={dialogText}>
            This is the only time it is shown. If it is lost, revoke it and
            create another.
          </DialogDescription>
        </DialogHeader>
        <div className="mx-5 mt-4 flex flex-col gap-1.5">
          <label htmlFor="ck-key" className={fieldLabel}>
            Key
          </label>
          <div className="flex gap-2">
            <input
              id="ck-key"
              readOnly
              value={created?.key ?? ""}
              onFocus={(e) => e.target.select()}
              className={cn(fieldInput, "min-w-0 flex-1 font-mono text-xs")}
            />
            {created && <CopyButton text={created.key} />}
          </div>
        </div>
        <dl className="mx-5 mt-3.5 rounded-lg border border-subtle text-[13px] leading-[19px]">
          <div className="flex gap-3 px-3 py-2">
            <dt className="w-24 flex-none text-muted">Access</dt>
            <dd className="text-fg/85">
              {created?.project_id ? (
                <>
                  {created.project_name}
                  {created.services.length > 0 &&
                    ` · ${created.services
                      .map((s) => (s.environment_name ? `${s.name} (${s.environment_name})` : s.name))
                      .join(", ")}`}{" "}
                  ·{" "}
                  <span className="font-mono text-xs">
                    {created.scopes.join(", ")}
                  </span>
                </>
              ) : (
                "Full account"
              )}
            </dd>
          </div>
          <div className="flex gap-3 border-t border-subtle px-3 py-2">
            <dt className="w-24 flex-none text-muted">Expires</dt>
            <dd className="text-fg/85">
              {created?.expires_at ? (
                <>
                  <span className="font-mono text-xs text-fg">
                    {formatDateTime(created.expires_at).slice(0, 10)}
                  </span>{" "}
                  ({untilHint(created.expires_at, now)})
                </>
              ) : (
                "Never"
              )}
            </dd>
          </div>
          <div className="flex gap-3 border-t border-subtle px-3 py-2">
            <dt className="w-24 flex-none text-muted">Use it as</dt>
            <dd className="font-mono text-xs leading-[19px] text-fg/85">
              Authorization: Bearer paas_…
            </dd>
          </div>
        </dl>
        <div className={dialogFooter}>
          <button type="button" className={primaryButton} onClick={onClose}>
            Done, I stored it
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function RevokeDialog({
  target,
  actsIn,
  now,
  onClose,
}: {
  target: APIKey | null;
  actsIn: { main: string; note: string } | null;
  now: number;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const revoke = useMutation({
    mutationFn: () => apiKeyService.delete(target!.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["api-keys"] });
      toast.success(`${target?.name} revoked`);
      close();
    },
  });
  const close = () => {
    onClose();
    revoke.reset();
  };
  const exp = target ? expiry(target.expires_at, now) : null;
  const used =
    exp?.tone === "expired"
      ? target?.last_used_at
        ? `It expired ${exp.note} and was last used ${sinceShort(target.last_used_at, now)}.`
        : `It expired ${exp.note} and was never used.`
      : target?.last_used_at
        ? `It was last used ${sinceShort(target.last_used_at, now)}.`
        : "It has never been used.";
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
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Revoke {target?.name}?
          </DialogTitle>
          <DialogDescription className={dialogText}>
            Anything using{" "}
            <span className="font-mono text-xs text-fg">{target?.prefix}…</span>{" "}
            gets 401 from now on. {used} This can&apos;t be undone; create a new
            key if you need one again.
          </DialogDescription>
        </DialogHeader>
        {target && actsIn && (
          <dl className="mx-5 mt-3.5 rounded-lg border border-subtle text-[13px] leading-[19px]">
            <div className="flex gap-3 px-3 py-2">
              <dt className="w-24 flex-none text-muted">Access</dt>
              <dd className="text-fg/85">
                {access(target)}{" "}
                {target.scopes.length > 0 && (
                  <span className="font-mono text-xs">
                    {target.scopes.join(", ")}
                  </span>
                )}
              </dd>
            </div>
            <div className="flex gap-3 border-t border-subtle px-3 py-2">
              <dt className="w-24 flex-none text-muted">Acts in</dt>
              <dd className="text-fg/85">
                {target.project_id
                  ? `${actsIn.main} (${target.org_name ?? ""})`
                  : `Your active workspace, ${actsIn.note}`}
              </dd>
            </div>
            <div className="flex gap-3 border-t border-subtle px-3 py-2">
              <dt className="w-24 flex-none text-muted">Expires</dt>
              <dd className="text-fg/85">
                {exp?.tone === "never"
                  ? "Never."
                  : exp?.tone === "expired"
                    ? `Expired ${exp.note}.`
                    : `On ${exp?.date}, ${exp?.note}.`}
              </dd>
            </div>
          </dl>
        )}
        {revoke.error && (
          <p
            role="alert"
            className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
          >
            {revoke.error instanceof Error
              ? revoke.error.message
              : "The key was not revoked."}
          </p>
        )}
        <div className={dialogFooter}>
          <button
            type="button"
            className={cancelButton}
            disabled={revoke.isPending}
            onClick={close}
          >
            Cancel
          </button>
          <button
            type="button"
            className={dangerButton}
            disabled={revoke.isPending}
            onClick={() => revoke.mutate()}
          >
            {revoke.isPending ? "Revoking…" : "Revoke key"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
