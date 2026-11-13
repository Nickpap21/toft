"use client";

import { useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Plus, Trash2 } from "lucide-react";
import { EnvChip, envChip } from "@/components/ui/env-chip";
import { rememberEnv } from "@/components/shell/env-context";
import { DeleteData, useDeleteData } from "../../../_lib/delete-data";
import { NewEnvironmentDialog } from "../../../_lib/new-environment-dialog";
import { ServiceMark } from "../../../_lib/service-table";
import { envState, inProse, serverName } from "../../../_lib/projects-view";
import { card as projectCard, field, label } from "../../../_lib/ui";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { projectService } from "@/lib/api/project.service";
import { ENV_COLORS, environmentService, type EnvColor } from "@/lib/api/environment.service";
import {
  changedSinceRead,
  envvarService,
  environmentScope,
  VALID_KEY,
  type EnvVar,
} from "@/lib/api/envvar.service";
import { secretService } from "@/lib/api/secret.service";
import { serviceService } from "@/lib/api/service.service";
import { serverService } from "@/lib/api/server.service";
import { fetchAPI, ApiError, notFound } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";
import { Read, ReadFailed } from "@/components/shell/unreachable";
import { serviceHref } from "@/components/home/home";
import { useOrgRole } from "@/context/AuthContext";
import { useReauth } from "@/components/reauth";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { focusAfterReread } from "@/lib/focus-after-reread";
import { cn } from "@/lib/utils";
import { statusLabel, TONE_CLASSES } from "@/lib/status";
import {
  CopyFromDialog,
  CopyKeysDialog,
  ReplaceSecretDialog,
  when,
} from "../../services/[serviceId]/_components/tabs/service-variables";
import { rebaseEdits } from "../../services/[serviceId]/_components/variables-view";

// An environment (board: private/design/canvas/ReworkEnvironment.dc.html):
// its shared variables and secrets, which every service in it inherits on
// its next deploy, what each service sets itself, and its settings.

type Overview = components["schemas"]["Overview"];

const card = cn(projectCard, "min-w-0");
const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const sum = "text-xs text-muted";
const help = "text-xs leading-[18px] text-muted";
const mono = "font-mono text-xs";
const focus = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
const btnSm = `touch-target inline-flex h-[30px] items-center gap-1.5 whitespace-nowrap rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:bg-raised/70 disabled:opacity-50 ${focus}`;
const ghost = `touch-target inline-flex h-[30px] items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 text-xs font-medium text-fg/80 hover:bg-raised hover:text-fg disabled:opacity-50 ${focus}`;
const priSm = `touch-target inline-flex h-[30px] items-center whitespace-nowrap rounded-md bg-accent px-2.5 text-xs font-medium text-on-accent hover:bg-accent/90 disabled:opacity-50 ${focus}`;
const link = "text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center";
const input =
  "h-8 w-full rounded-md border border-strong/70 bg-ground px-2.5 font-mono text-xs text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
// List tables: header row on wide screens, a card per row below 640 px.
const table = "w-full border-collapse text-[13px] max-sm:block sm:table-fixed [&>tbody]:max-sm:block";
const th = "border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted";
const tr = "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0";
const td = "border-t border-subtle px-4 py-2.5 align-middle [tr:first-child>&]:border-t-0 max-sm:border-0 max-sm:p-0";
// Rows with 30 px buttons: the board's 43 px rows.
const tdBtn = `${td} sm:py-1.5`;
const badge = "mr-2 text-xs text-status-pending-fg";

interface Row {
  id: number;
  key: string;
  value: string;
  saved?: { key: string; value: string };
  removed?: boolean;
  /** A saved row opened with Edit. */
  editing?: boolean;
}
let rowSeq = 0;
const newRowId = () => ++rowSeq;
const toRows = (list: EnvVar[] | undefined): Row[] =>
  (list ?? []).map((v) => ({
    id: newRowId(),
    key: v.key,
    value: v.value,
    saved: { key: v.key, value: v.value },
  }));
const isDirty = (r: Row) =>
  !r.saved || !!r.removed || r.key !== r.saved.key || r.value !== r.saved.value;

/** "13 variables, 5 secrets"; a zero part is left out. */
const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;
const keysText = (variables: number, secrets: number, sep: string) =>
  [variables && plural(variables, "variable"), secrets && plural(secrets, "secret")].filter(Boolean).join(sep);

export default function EnvironmentPage() {
  const params = useParams();
  const router = useRouter();
  const queryClient = useQueryClient();
  const projectId = params.id as string;
  const envId = params.envId as string;
  const role = useOrgRole();
  const canDeploy = role === "owner" || role === "developer";
  const { set: setBreadcrumbs } = useBreadcrumbs();
  const scope = environmentScope(envId);

  const { data: project } = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => projectService.get(projectId),
  });
  const { data: environments, isLoading, error: envError, refetch: refetchEnvs } = useQuery({
    queryKey: ["environments", projectId],
    queryFn: () => environmentService.list(projectId),
  });
  const env = environments?.find((e) => e.id === envId);
  // Its own services, and the project and workspace services it reaches:
  // those are not its own, get none of its shared keys and outlive it.
  const reachedRead = useQuery({
    queryKey: ["env-services", envId],
    queryFn: () => serviceService.listForEnvironment(envId),
    enabled: !!env,
  });
  const reached = reachedRead.data;
  const services = reached
    ?.filter((s) => s.scope === "environment")
    .sort((a, b) => a.name.localeCompare(b.name));
  const projectServices = (reached ?? []).filter((s) => s.scope === "project");
  // Every environment's services: Other environments' servers and states.
  const { data: allServices } = useQuery({
    queryKey: ["project-services", projectId],
    queryFn: () => serviceService.listForProject(projectId),
    enabled: !!env,
  });
  // What Delete removes, with sizes, for the owner's Delete card.
  const deleteData = useDeleteData(
    "environments",
    envId,
    role === "owner" && (services ?? []).length > 0,
  );
  const { data: overview } = useQuery({
    queryKey: ["environment-overview", envId],
    queryFn: () => fetchAPI<Overview>(`/api/v1/environments/${envId}/overview`),
    enabled: !!env,
  });
  const { data: servers } = useQuery({
    queryKey: ["servers"],
    queryFn: () => serverService.list(),
    staleTime: 60_000,
  });
  const { data: fetched, dataUpdatedAt: varsAt, error: varsError, refetch: refetchVars } = useQuery({
    queryKey: ["envvars", scope.kind, scope.id],
    queryFn: () => envvarService.list(scope),
    enabled: !!env,
  });
  const vars = fetched?.data;
  const { data: secrets, error: secretsError, refetch: refetchSecrets } = useQuery({
    queryKey: ["secrets", scope.kind, scope.id],
    queryFn: () => secretService.list(scope),
    enabled: !!env,
  });
  const now = varsAt;

  useDocumentTitle(env && project ? `${env.name} · ${project.name} · Toft` : "Toft");

  // Opening an environment makes it the project's context (D-55).
  useEffect(() => {
    if (env) rememberEnv(projectId, env.id);
  }, [env, projectId]);

  useEffect(() => {
    setBreadcrumbs([
      { label: "Projects", href: "/projects" },
      {
        label: project?.name ?? "Project",
        href: `/projects/${projectId}?env=${envId}`,
      },
      { label: env?.name ?? "Environment", href: "" },
    ]);
  }, [project, env, projectId, envId, setBreadcrumbs]);

  // ── Shared variables: the editable list ────────────────────────────────────
  const [rows, setRows] = useState<Row[]>([]);
  const [syncedFrom, setSyncedFrom] = useState<EnvVar[] | undefined | null>(
    null,
  );
  // The version of the read these rows came from: what Save sends. A
  // background read doesn't move it while there are edits, so a save of rows
  // from before someone else's change is refused rather than undoing it.
  const [rowsVersion, setRowsVersion] = useState<string | undefined>();
  if (vars !== syncedFrom) {
    setSyncedFrom(vars);
    if (!rows.some(isDirty) || syncedFrom === null) {
      setRows(toRows(vars));
      setRowsVersion(fetched?.version);
    }
  }
  const live = rows.filter((r) => !(r.removed && !r.saved));
  const added = live.filter((r) => !r.saved && !r.removed);
  const removed = live.filter((r) => r.removed);
  const edited = live.filter(
    (r) =>
      r.saved &&
      !r.removed &&
      (r.key !== r.saved.key || r.value !== r.saved.value),
  );
  const changes = added.length + removed.length + edited.length;
  const keyCount = (k: string) =>
    rows.filter((r) => !r.removed && r.key === k).length;
  const keyError = (r: Row): string => {
    if (r.removed) return "";
    if (r.key === "") return "A name is required.";
    if (!VALID_KEY.test(r.key))
      return "Letters, digits and _, not starting with a digit.";
    if (keyCount(r.key) > 1) return "Another variable has this name.";
    if ((secrets ?? []).some((s) => s.key === r.key))
      return "A shared secret has this name.";
    return "";
  };
  const invalid = rows.filter((r) => keyError(r) !== "");
  useEffect(() => {
    if (changes === 0) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [changes]);
  const update = (id: number, patch: Partial<Row>) =>
    setRows((rs) => rs.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  const focusSoon = (id: string) =>
    requestAnimationFrame(() => document.getElementById(id)?.focus());
  const addRow = () => {
    const id = newRowId();
    setRows((rs) => [...rs, { id, key: "", value: "" }]);
    focusSoon(`env-key-${id}`);
  };
  // The Save refused because the variables changed since they were read.
  const [conflict, setConflict] = useState(false);
  const readAgain = async () => {
    const fresh = await refetchVars();
    if (!fresh.data || fresh.isError) {
      toast.error("Couldn’t read the variables. Try again.");
      return;
    }
    setRows((rs) => rebaseEdits(rs, toRows(fresh.data.data), isDirty));
    setRowsVersion(fresh.data.version);
    setConflict(false);
    focusAfterReread("sv-save", "sv-h");
  };
  const saveVars = useMutation({
    mutationFn: () =>
      envvarService.bulkUpsert(
        scope,
        rowsVersion!,
        rows
          .filter((r) => !r.removed)
          .map((r) => ({ key: r.key, value: r.value, is_secret: false, masked: false })),
      ),
    onSuccess: (saved) => {
      toast.success(
        "Shared variables saved: each service gets them at its next deploy",
      );
      setRows(toRows(saved.data));
      setRowsVersion(saved.version);
      setSyncedFrom(saved.data);
      setConflict(false);
      queryClient.setQueryData(["envvars", scope.kind, scope.id], saved);
      queryClient.invalidateQueries({
        queryKey: ["environment-overview", envId],
      });
    },
    onError: (err: unknown) => {
      if (changedSinceRead(err)) {
        setConflict(true);
        return;
      }
      toast.error(
        err instanceof ApiError
          ? err.message
          : "Failed to save the shared variables",
      );
    },
  });

  // ── What each service sets ─────────────────────────────────────────────────
  const byService = new Map(
    (overview?.services ?? []).map((o) => [o.service_id, o]),
  );
  const overriddenIn = (key: string) =>
    (services ?? [])
      .filter((s) => byService.get(s.id)?.overrides.includes(key))
      .map((s) => s.name);

  // ── Header facts ───────────────────────────────────────────────────────────
  const counts = (services ?? []).reduce<Record<string, number>>((acc, s) => {
    acc[s.runtime_status] = (acc[s.runtime_status] ?? 0) + 1;
    return acc;
  }, {});
  const serverIds = [
    ...new Set((services ?? []).map((s) => s.server_id ?? "")),
  ];
  const serverText = !reached
    ? "—"
    : serverIds.length === 0
      ? "none yet"
      : serverIds.length === 1
        ? (() => {
            const srv = servers?.find((x) => x.id === serverIds[0]);
            return !serverIds[0]
              ? "the panel host"
              : srv
                ? srv.shared
                  ? `${srv.name} · shared by ${srv.shared_by}`
                  : `${srv.name} · ${srv.ip_address}`
                : "…";
          })()
        : `${serverIds.length} servers`;

  const [copyingVars, setCopyingVars] = useState(false);
  const [copyingKeys, setCopyingKeys] = useState(false);
  const [replacing, setReplacing] = useState<string | null>(null);
  const [deletingSecret, setDeletingSecret] = useState<string | null>(null);
  const [newKey, setNewKey] = useState("");
  // The add form opens from an Add secret button.
  const [secretForm, setSecretForm] = useState(false);
  const [newValue, setNewValue] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [creatingEnv, setCreatingEnv] = useState(false);
  const refreshSecrets = () => {
    queryClient.invalidateQueries({
      queryKey: ["secrets", scope.kind, scope.id],
    });
    queryClient.invalidateQueries({
      queryKey: ["environment-overview", envId],
    });
  };

  const addSecret = useMutation({
    mutationFn: () => secretService.create(scope, newKey, newValue),
    onSuccess: () => {
      toast.success(`${newKey} added: each service gets it at its next deploy`);
      setNewKey("");
      setNewValue("");
      setSecretForm(false);
      refreshSecrets();
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof ApiError ? err.message : "Failed to add the secret",
      ),
  });
  const deleteSecret = useMutation({
    mutationFn: (key: string) => secretService.delete(scope, key),
    onSuccess: (_v, key) => {
      toast.success(`${key} deleted`);
      setDeletingSecret(null);
      refreshSecrets();
    },
    onError: () => toast.error("Failed to delete the secret"),
  });

  // ── Protection, colour and name: one form ──────────────────────────────────
  // Only what changed is sent, so toggling protection never resends a colour
  // the API no longer takes (red, D-142).
  const [draft, setDraft] = useState<{ name?: string; color?: EnvColor; protected?: boolean }>({});
  const confirmPassword = useReauth();
  const saveEnv = useMutation({
    mutationFn: async (patch: { name?: string; color?: EnvColor; protected?: boolean }) => {
      if (
        env?.protected &&
        patch.protected === false &&
        !(await confirmPassword(
          `Lifting ${env.name}’s protection lets deploys and agents act there without asking, so Toft asks for your password first.`,
        ))
      ) {
        throw new Error(`${env.name} stays protected without your password.`);
      }
      return environmentService.update(envId, patch);
    },
    onSuccess: () => {
      toast.success("Environment saved");
      setDraft({});
      queryClient.invalidateQueries({ queryKey: ["environments", projectId] });
      queryClient.invalidateQueries({ queryKey: ["workspace-environments"] });
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : "Failed to save the environment",
      ),
  });
  const deleteEnv = useMutation({
    mutationFn: async () => {
      if (
        !(await confirmPassword(
          `Deleting ${env?.name} can’t be undone, so Toft asks for your password first.`,
        ))
      ) {
        throw new Error("The environment stays without your password.");
      }
      return environmentService.delete(envId, confirm);
    },
    onSuccess: () => {
      toast.success(`${env?.name} deleted`);
      queryClient.invalidateQueries({ queryKey: ["environments", projectId] });
      router.push(`/projects/${projectId}`);
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : "Failed to delete the environment",
        { duration: 15_000 },
      ),
  });

  if (isLoading) {
    return (
      <div
        className="h-64 w-full max-w-[1600px] rounded-[10px] bg-surface motion-safe:animate-pulse"
        role="status"
        aria-label="Loading the environment"
      />
    );
  }
  if (!environments && envError && !notFound(envError)) {
    return <ReadFailed error={envError} onRetry={() => void refetchEnvs()} />;
  }
  if (!env) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-3 p-5")}>
        <h1 className={h2}>Environment not found</h1>
        <p className="text-[13px] leading-5 text-muted">
          {project?.name ?? "This project"} has no environment with this ID. It
          may have been deleted.
          {environments && environments.length > 0 && (
            <>
              {" "}
              Its environments are{" "}
              {environments.map((e, i) => (
                <span key={e.id}>
                  {i > 0 && (i === environments.length - 1 ? " and " : ", ")}
                  <Link
                    href={`/projects/${projectId}/environments/${e.id}`}
                    className="font-mono inline-link"
                  >
                    {e.name}
                  </Link>
                </span>
              ))}
              .
            </>
          )}
        </p>
        <Link
          href={`/projects/${projectId}`}
          className={cn(btnSm, "h-9 self-start px-3 text-[13px]")}
        >
          All environments
        </Link>
      </section>
    );
  }

  const secretKeyErr =
    newKey && !VALID_KEY.test(newKey)
      ? "Letters, digits and _, not starting with a digit."
      : newKey &&
          ((secrets ?? []).some((s) => s.key === newKey) ||
            (vars ?? []).some((v) => v.key === newKey))
        ? `${env.name} already shares ${newKey}.`
        : "";
  const hasSecrets = (secrets ?? []).length > 0;

  // The lede and the per-service table, from the overview's counts.
  const own = (services ?? []).map((s) => {
    const o = byService.get(s.id);
    return {
      s,
      variables: o?.own_variables ?? 0,
      secrets: o?.own_secrets ?? 0,
      overrides: o?.overrides ?? [],
    };
  });
  const setting = own.filter((o) => o.variables + o.secrets > 0);
  const overriding = own.filter((o) => o.overrides.length > 0).length;
  // Null until both reads have answered: what wasn't read isn't 0.
  const sharedCount = vars && secrets ? vars.length + secrets.length : null;

  const projectNames = projectServices.map((s) => s.name);
  const namesInProse = (names: string[]) =>
    names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
  const deleteServer =
    serverIds.length === 1 ? inProse(serverName(serverIds[0], servers) || "its server") : "their servers";

  // Protection and colour: the draft over the saved values.
  const dProt = draft.protected ?? env.protected;
  const dColor = draft.color ?? env.color;
  const dName = draft.name ?? env.name;
  const nameErr = dName.trim() === "" ? "A name is required." : "";
  const patch = {
    ...(draft.protected !== undefined && draft.protected !== env.protected && { protected: draft.protected }),
    ...(draft.color !== undefined && draft.color !== env.color && { color: draft.color }),
    ...(draft.name !== undefined && draft.name.trim() !== env.name && { name: draft.name.trim() }),
  };
  const envChanges = [
    patch.protected === false && "protection lifted",
    patch.protected === true && "protected",
    patch.color && `colour ${patch.color}`,
    patch.name && `renamed to ${patch.name}`,
  ].filter(Boolean);

  // Other environments of the project, previews after.
  const others = (environments ?? [])
    .filter((e) => e.id !== envId)
    .sort((a, b) => Number(!!a.preview) - Number(!!b.preview));
  const previews = others.filter((e) => e.preview).length;

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <header className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-3">
            <h1 className="min-w-0">
              <EnvChip
                env={env}
                size="md"
                className="h-7 max-w-full gap-1.5 px-[9px] text-[15px] [&>svg]:h-3.5 [&>svg]:w-3.5"
                nameClassName="truncate"
              />
            </h1>
            {Object.keys(counts).length > 0 && (
              <span className={sum}>
                {Object.entries(counts)
                  .map(([st, n]) => `${n} ${statusLabel(st).toLowerCase()}`)
                  .join(" · ")}
              </span>
            )}
          </div>
          {canDeploy && (
            <Link
              href={`/projects/${projectId}/services/new?env=${envId}`}
              className={cn(btnSm, "h-9 rounded-lg px-3 text-[13px] max-sm:flex-1 max-sm:justify-center")}
            >
              <Plus className="h-4 w-4" aria-hidden="true" />
              Add service
            </Link>
          )}
        </div>
        <dl className="flex flex-wrap gap-x-6 gap-y-1.5 text-[13px] max-sm:flex-col">
          <div className="flex min-w-0 gap-2">
            <dt className="text-muted">Server</dt>
            <dd className={cn(mono, "text-fg [overflow-wrap:anywhere]")}>{serverText}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-muted">Created</dt>
            <dd className={cn(mono, "text-fg")}>{env.created_at.slice(0, 10)}</dd>
          </div>
        </dl>
      </header>

      {/* Below lg one column: the main column's cards, the rail, then Delete last. */}
      <div className="grid items-start gap-5 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)] lg:gap-6">
        <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4 max-lg:contents">
          {setting.length > 0 && (
            <p className="text-[13px] leading-5 text-fg/70">
              Services also set their own:{" "}
              <span className="text-fg">
                {keysText(
                  setting.reduce((n, o) => n + o.variables, 0),
                  setting.reduce((n, o) => n + o.secrets, 0),
                  ", ",
                )}{" "}
                across {plural(setting.length, "service")}
              </span>
              {overriding > 0 &&
                `, ${overriding} of them overriding a shared key`}
              .{" "}
              <a href="#ps-h" className="inline-link">
                What each service sets
              </a>
            </p>
          )}

          {/* Shared variables */}
          <section className={card} aria-labelledby="sv-h">
            <div className={cardHead}>
              <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
                <h2 id="sv-h" tabIndex={-1} className={h2}>
                  Shared variables
                </h2>
                <span className={sum}>
                  {(vars ?? []).length > 0 && `${(vars ?? []).length} · `}every
                  service in {env.name} gets these, as plain text
                </span>
              </div>
              {canDeploy && vars && (
                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    className={ghost}
                    onClick={() => setCopyingVars(true)}
                  >
                    Copy from a service…
                  </button>
                  <button type="button" className={btnSm} onClick={addRow}>
                    Add variable
                  </button>
                </div>
              )}
            </div>
            {!vars ? (
              varsError ? (
                <div className="p-3">
                  <ReadFailed heading="h2" error={varsError} onRetry={() => void refetchVars()} />
                </div>
              ) : (
                <div className="mx-4 my-3.5 h-8 rounded-md bg-raised motion-safe:animate-pulse" role="status" aria-label="Loading the shared variables" />
              )
            ) : live.length === 0 ? (
              <p className="px-4 py-3.5 text-[13px] text-fg/70">
                No shared variables yet. A key added here reaches every service
                in {env.name} at its next deploy.
              </p>
            ) : (
              <table className={table}>
                <thead className="max-sm:hidden">
                  <tr>
                    <th scope="col" className={cn(th, "w-[34%]")}>
                      Key
                    </th>
                    <th scope="col" className={th}>
                      Value
                    </th>
                    <th scope="col" className={cn(th, "w-[190px]")}>
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {live.map((r) => {
                    const err = keyError(r);
                    const isNew = !r.saved;
                    const open = canDeploy && !r.removed && (isNew || r.editing);
                    const dirty =
                      !!r.saved &&
                      !r.removed &&
                      (r.key !== r.saved.key || r.value !== r.saved.value);
                    const valueDirty =
                      !!r.saved && !r.removed && r.value !== r.saved.value;
                    const name = r.key || "the new variable";
                    return (
                      <tr
                        key={r.id}
                        className={cn(
                          tr,
                          (isNew || dirty) && "bg-status-pending-soft/20",
                          r.removed && "opacity-60",
                        )}
                      >
                        <td className={cn(tdBtn, "max-sm:col-start-1 max-sm:row-start-1")}>
                          {open ? (
                            <div className="flex flex-col gap-1">
                              <label htmlFor={`env-key-${r.id}`} className="sr-only">
                                Key
                              </label>
                              <input
                                id={`env-key-${r.id}`}
                                value={r.key}
                                spellCheck={false}
                                placeholder="NAME"
                                aria-invalid={!!err}
                                onChange={(e) => update(r.id, { key: e.target.value })}
                                className={cn(input, err && "border-status-failed")}
                              />
                              {err && <p className="text-xs text-status-failed-fg">{err}</p>}
                            </div>
                          ) : (
                            <span
                              className={cn(
                                mono,
                                "text-fg [overflow-wrap:anywhere]",
                                r.removed && "line-through",
                              )}
                            >
                              {r.key}
                            </span>
                          )}
                        </td>
                        <td className={cn(tdBtn, "max-sm:col-span-2 max-sm:row-start-2")}>
                          {open ? (
                            <div className="flex flex-col gap-1">
                              <label htmlFor={`env-val-${r.id}`} className="sr-only">
                                Value of {name}
                              </label>
                              <input
                                id={`env-val-${r.id}`}
                                value={r.value}
                                spellCheck={false}
                                autoComplete="off"
                                onChange={(e) => update(r.id, { value: e.target.value })}
                                className={cn(input, valueDirty && "border-status-pending")}
                              />
                              {valueDirty && (
                                <p className="text-xs text-status-pending-fg">
                                  Saved value: {r.saved!.value || "empty"}
                                </p>
                              )}
                            </div>
                          ) : (
                            <span
                              className={cn(
                                mono,
                                "text-fg/85 [overflow-wrap:anywhere]",
                                r.removed && "line-through",
                              )}
                            >
                              {r.value || <span className="text-muted">empty</span>}
                            </span>
                          )}
                        </td>
                        <td className={cn(tdBtn, "whitespace-nowrap text-right max-sm:col-start-2 max-sm:row-start-1")}>
                          {r.removed ? (
                            <>
                              <span className={badge}>Removed</span>
                              <button
                                type="button"
                                className={ghost}
                                aria-label={`Undo removing ${r.key}`}
                                onClick={() => update(r.id, { removed: false })}
                              >
                                Undo
                              </button>
                            </>
                          ) : open ? (
                            <>
                              {(isNew || dirty) && <span className={badge}>Not saved</span>}
                              <button
                                type="button"
                                className={ghost}
                                aria-label={`Remove ${name}`}
                                onClick={() =>
                                  isNew
                                    ? setRows((rs) => rs.filter((x) => x.id !== r.id))
                                    : update(r.id, { removed: true })
                                }
                              >
                                Remove
                              </button>
                            </>
                          ) : canDeploy ? (
                            <button
                              type="button"
                              className={ghost}
                              aria-label={`Edit ${r.key}`}
                              onClick={() => {
                                update(r.id, { editing: true });
                                focusSoon(`env-val-${r.id}`);
                              }}
                            >
                              Edit
                            </button>
                          ) : null}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
            {conflict && (
              <div role="alert" className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle bg-status-failed-soft px-4 py-2.5 text-[13px] text-status-failed-fg">
                <p className="min-w-0 flex-[1_1_360px]">
                  These variables changed since you opened this page, so nothing was saved. What you typed is still here.
                </p>
                <button type="button" className={btnSm} onClick={() => void readAgain()}>
                  Read them again
                </button>
              </div>
            )}
            {canDeploy && vars && rowsVersion && changes > 0 && (
              <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-fg/70">
                <p className="min-w-0 flex-[1_1_360px]">
                  <span className="font-medium text-fg">
                    {changes === 1 ? "1 unsaved change:" : `${changes} unsaved changes:`}
                  </span>{" "}
                  {[
                    ...added.map((r) => `${r.key || "a new variable"} added`),
                    ...edited.map((r) => `${r.saved!.key} changed`),
                    ...removed.map((r) => `${r.saved!.key} removed`),
                  ].join(", ")}
                  .{" "}
                  {invalid.length > 0
                    ? `Can’t save until ${invalid.map((r) => r.key || "the unnamed row").join(", ")} ${invalid.length === 1 ? "is" : "are"} fixed.`
                    : (() => {
                        const keeping = [
                          ...new Set(
                            live
                              .filter((r) => !r.removed)
                              .flatMap((r) => overriddenIn(r.key)),
                          ),
                        ];
                        return `Each service gets ${changes === 1 ? "it" : "them"} on its next deploy${keeping.length ? `; ${namesInProse(keeping)} ${keeping.length === 1 ? "keeps its own value" : "keep their own values"} where ${keeping.length === 1 ? "it sets" : "they set"} the key` : ""}.`;
                      })()}
                </p>
                <span className="flex items-center gap-2">
                  <button
                    type="button"
                    className={ghost}
                    onClick={() => {
                      setRows(toRows(vars));
                      setRowsVersion(fetched?.version);
                      setConflict(false);
                    }}
                    disabled={saveVars.isPending}
                  >
                    Cancel
                  </button>
                  <button
                    id="sv-save"
                    type="button"
                    className={priSm}
                    onClick={() => saveVars.mutate()}
                    disabled={invalid.length > 0 || saveVars.isPending || conflict}
                  >
                    {saveVars.isPending ? "Saving…" : "Save variables"}
                  </button>
                </span>
              </div>
            )}
          </section>

          {/* Shared secrets */}
          <section className={card} aria-labelledby="ss-h">
            <div className={cardHead}>
              <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
                <h2 id="ss-h" className={h2}>
                  Shared secrets
                </h2>
                <span className={sum}>
                  {hasSecrets && `${(secrets ?? []).length} · `}every service in {env.name} gets these{hasSecrets && "; nobody can read one back"}
                </span>
              </div>
              {canDeploy && secrets && (
                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    className={ghost}
                    onClick={() => setCopyingKeys(true)}
                  >
                    Copy keys from a service…
                  </button>
                  {hasSecrets && !secretForm && (
                    <button type="button" className={btnSm} onClick={() => setSecretForm(true)}>
                      Add secret
                    </button>
                  )}
                </div>
              )}
            </div>
            {!secrets &&
              (secretsError ? (
                <div className="p-3">
                  <ReadFailed heading="h2" error={secretsError} onRetry={() => void refetchSecrets()} />
                </div>
              ) : (
                <div className="mx-4 my-3.5 h-8 rounded-md bg-raised motion-safe:animate-pulse" role="status" aria-label="Loading the shared secrets" />
              ))}
            {hasSecrets && (
              <table className={table}>
                <thead className="max-sm:hidden">
                  <tr>
                    <th scope="col" className={cn(th, "w-[34%]")}>
                      Key
                    </th>
                    <th scope="col" className={th}>
                      Set
                    </th>
                    <th scope="col" className={cn(th, "w-[190px]")}>
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {(secrets ?? []).map((s) => (
                    <tr key={s.key} className={tr}>
                      <td className={cn(tdBtn, mono, "text-fg [overflow-wrap:anywhere] max-sm:col-start-1 max-sm:row-start-1")}>
                        {s.key}
                      </td>
                      <td className={cn(tdBtn, "text-[13px] text-fg/70 max-sm:col-span-2 max-sm:row-start-2")}>
                        {when(s.updated_at, now)}
                        {s.updated_by && ` · ${s.updated_by}`}
                      </td>
                      <td className={cn(tdBtn, "whitespace-nowrap text-right max-sm:col-start-2 max-sm:row-start-1")}>
                        {canDeploy && (
                          <span className="inline-flex items-center gap-1">
                            <button
                              type="button"
                              className={ghost}
                              aria-label={`Replace ${s.key}`}
                              onClick={() => setReplacing(s.key)}
                            >
                              Replace…
                            </button>
                            <button
                              type="button"
                              aria-label={`Delete ${s.key}`}
                              onClick={() => setDeletingSecret(s.key)}
                              className={cn(ghost, "w-[30px] justify-center px-0 text-muted hover:text-status-failed-fg pointer-coarse:min-w-11")}
                            >
                              <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                            </button>
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {secrets && !hasSecrets && !secretForm && (
              <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 px-4 py-3.5 text-[13px] text-fg/70">
                <span>
                  No shared secrets yet. A key added here reaches every service
                  in {env.name} at its next deploy; nobody can read it back.
                </span>
                {canDeploy && (
                  <button type="button" className={priSm} onClick={() => setSecretForm(true)}>
                    Add secret
                  </button>
                )}
              </div>
            )}
            {canDeploy && secretForm && (
              <form
                className={cn(
                  "flex flex-wrap items-end gap-3 px-4 py-3",
                  hasSecrets && "border-t border-subtle",
                )}
                onSubmit={(e) => {
                  e.preventDefault();
                  if (newKey && newValue && !secretKeyErr) addSecret.mutate();
                }}
              >
                <label className="flex min-w-[180px] flex-[1_1_240px] flex-col gap-1 text-xs text-muted">
                  Key
                  <input
                    value={newKey}
                    onChange={(e) => setNewKey(e.target.value)}
                    autoFocus
                    placeholder="SENTRY_AUTH_TOKEN"
                    spellCheck={false}
                    aria-invalid={!!secretKeyErr}
                    className={cn(input, secretKeyErr && "border-status-failed")}
                  />
                </label>
                <label className="flex min-w-[200px] flex-[2_1_320px] flex-col gap-1 text-xs text-muted">
                  Value
                  <input
                    type="password"
                    autoComplete="off"
                    value={newValue}
                    onChange={(e) => setNewValue(e.target.value)}
                    placeholder="Pasted once, never shown again"
                    className={input}
                  />
                </label>
                <span className="flex items-center gap-2">
                  <button
                    type="button"
                    className={ghost}
                    onClick={() => {
                      setSecretForm(false);
                      setNewKey("");
                      setNewValue("");
                    }}
                    disabled={addSecret.isPending}
                  >
                    Cancel
                  </button>
                  <button
                    type="submit"
                    className={priSm}
                    disabled={!newKey || !newValue || !!secretKeyErr || addSecret.isPending}
                  >
                    {addSecret.isPending ? "Adding…" : "Add secret"}
                  </button>
                </span>
                {secretKeyErr && (
                  <p className="basis-full text-xs text-status-failed-fg">{secretKeyErr}</p>
                )}
              </form>
            )}
          </section>

          {/* What each service sets */}
          <section className={card} aria-labelledby="ps-h">
            <div className={cardHead}>
              <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
                <h2 id="ps-h" className={h2}>
                  What each service sets
                </h2>
                <span className={sum}>a service’s own key wins over a shared one with its name</span>
              </div>
            </div>
            <Read
              query={reachedRead}
              what="the services"
              isEmpty={() => own.length === 0}
              empty={
                <p className="px-4 py-3.5 text-[13px] text-fg/70">
                  No services in {env.name} yet.
                  {sharedCount !== null && (
                    <>
                      {" "}A service added here gets its{" "}
                      {sharedCount === 0
                        ? "shared keys once it has some"
                        : `${keysText((vars ?? []).length, (secrets ?? []).length, " and ")}`}
                      .
                    </>
                  )}
                </p>
              }
            >
              {() => (
              <table className={table}>
                <thead className="max-sm:hidden">
                  <tr>
                    <th scope="col" className={th}>
                      Service
                    </th>
                    <th scope="col" className={cn(th, "w-[110px]")}>
                      Shared
                    </th>
                    <th scope="col" className={cn(th, "w-[190px]")}>
                      Own
                    </th>
                    <th scope="col" className={th}>
                      Overrides
                    </th>
                    <th scope="col" className={cn(th, "w-[100px]")}>
                      <span className="sr-only">Variables</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {own.map((o) => (
                    <tr key={o.s.id} className={tr}>
                      <td className={cn(td, "max-sm:col-start-1 max-sm:row-start-1")}>
                        <span className="flex min-w-0 items-start gap-2.5">
                          <ServiceMark slug={o.s.addon_slug} />
                          <Link
                            href={serviceHref(projectId, o.s.id)}
                            className="min-w-0 font-medium text-fg [overflow-wrap:anywhere] hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center"
                          >
                            {o.s.name}
                          </Link>
                        </span>
                      </td>
                      <td className={cn(td, "text-xs text-fg/80 max-sm:col-span-2 max-sm:row-start-2")}>
                        <span className="text-muted sm:hidden">Shared </span>
                        {sharedCount === null ? <span className="text-muted">—</span> : sharedCount - o.overrides.length}
                      </td>
                      <td className={cn(td, "text-xs text-fg/80 max-sm:col-span-2 max-sm:row-start-3")}>
                        <span className="text-muted sm:hidden">Own </span>
                        {keysText(o.variables, o.secrets, " · ") || <span className="text-muted">none</span>}
                      </td>
                      <td
                        className={cn(
                          td,
                          mono,
                          "text-fg/80 [overflow-wrap:anywhere] max-sm:col-span-2 max-sm:row-start-4",
                          o.overrides.length === 0 && "max-sm:hidden",
                        )}
                      >
                        <span className="font-sans text-muted sm:hidden">Overrides </span>
                        {o.overrides.length ? o.overrides.join(", ") : <span className="text-muted">—</span>}
                      </td>
                      <td className={cn(td, "text-right max-sm:col-start-2 max-sm:row-start-1")}>
                        <Link
                          href={`${serviceHref(projectId, o.s.id)}?tab=variables`}
                          className={link}
                          aria-label={`${o.s.name}’s variables`}
                        >
                          Variables
                        </Link>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              )}
            </Read>
            {projectNames.length > 0 && (
              <p className="border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                {namesInProse(projectNames)}{" "}
                {projectNames.length === 1 ? "is a project service: it gets" : "are project services: they get"}{" "}
                none of {env.name}’s shared keys
              </p>
            )}
          </section>

          {role === "owner" && (
            <section className={cn(card, "border-status-failed/40 max-lg:order-last")} aria-labelledby="ed-h">
              <div className="flex flex-wrap items-center gap-x-4 gap-y-3 p-4">
                <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-1">
                  <h2 id="ed-h" className={h2}>
                    Delete {env.name}
                  </h2>
                  <p className="text-[13px] leading-[19px] text-muted">
                    {!services
                      ? `Removes ${env.name} and every service in it.`
                      : services.length === 0
                      ? `Removes ${env.name} with its shared variables and secrets.`
                      : `Removes its ${plural((services ?? []).length, "service")} from ${deleteServer} with their containers, images and config files, then its deployments, shared variables, secrets and domains.`}{" "}
                    {projectNames.length > 0
                      ? `${namesInProse(projectNames)} and the workspace services stay.`
                      : "Project and workspace services stay."}
                    {(services ?? []).length > 0 && " The data that goes with it:"}
                  </p>
                  {(services ?? []).length > 0 && (
                    <div className="mt-2 rounded-lg border border-subtle px-3 py-2 text-[13px]">
                      <DeleteData {...deleteData} perService />
                    </div>
                  )}
                </div>
                <button
                  type="button"
                  className="touch-target inline-flex min-h-9 max-w-full items-center rounded-lg bg-danger px-3 py-1.5 text-left text-[13px] font-medium text-on-danger [overflow-wrap:anywhere] hover:bg-danger/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                  onClick={() => setDeleting(true)}
                >
                  Delete {env.name}…
                </button>
              </div>
            </section>
          )}
        </div>

        <aside className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
          {canDeploy && (
            <form
              className={card}
              aria-labelledby="pc-h"
              onSubmit={(e) => {
                e.preventDefault();
                if (envChanges.length > 0 && !nameErr) saveEnv.mutate(patch);
              }}
            >
              <div className={cardHead}>
                <h2 id="pc-h" className={h2}>
                  Protection and colour
                </h2>
              </div>
              <div className="flex flex-col gap-3.5 p-4">
                <div className="flex items-center justify-between gap-4">
                  <div>
                    <div id="prot-l" className="text-[13px] font-medium text-fg">
                      Protected
                    </div>
                    <p id="prot-d" className={help}>
                      Deploy, Stop, Restart and Delete confirm first and name{" "}
                      {env.name}. Lifting it asks for your password; only owners
                      can.
                    </p>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={dProt}
                    aria-labelledby="prot-l"
                    aria-describedby="prot-d"
                    disabled={env.protected && role !== "owner"}
                    onClick={() => setDraft((d) => ({ ...d, protected: !dProt }))}
                    className={cn(
                      "relative h-5 w-9 flex-none rounded-full transition-colors disabled:opacity-50 before:absolute before:-inset-x-1 before:-inset-y-3 before:content-['']",
                      focus,
                      dProt ? "bg-accent" : "bg-strong",
                    )}
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        "absolute left-[3px] top-[3px] h-3.5 w-3.5 rounded-full bg-on-accent",
                        dProt && "translate-x-4",
                      )}
                    />
                  </button>
                </div>
                {dProt ? (
                  <div className="flex flex-wrap items-center gap-x-2.5 text-[13px]">
                    <span className="text-muted">Colour</span>
                    <span className="text-fg">
                      {dColor} <span className="text-muted">· shows when protection is off</span>
                    </span>
                  </div>
                ) : (
                  <fieldset className="flex min-w-0 flex-col gap-2">
                    <legend className="mb-2 text-[13px] font-medium text-fg">Colour</legend>
                    <div className="flex flex-wrap gap-2">
                      {ENV_COLORS.map((c) => (
                        <label key={c} className="relative flex flex-col items-center gap-1 text-[11px] text-muted">
                          <input
                            type="radio"
                            name="env-colour"
                            value={c}
                            checked={dColor === c}
                            onChange={() => setDraft((d) => ({ ...d, color: c }))}
                            className="peer sr-only"
                          />
                          <span
                            aria-hidden="true"
                            className={cn(
                              "block h-7 w-7 cursor-pointer rounded-md pointer-coarse:h-11 pointer-coarse:w-11 peer-focus-visible:ring-2 peer-focus-visible:ring-focus",
                              envChip(c).swatch,
                              dColor === c && "ring-2 ring-fg ring-offset-2 ring-offset-surface",
                            )}
                          />
                          <span className={cn(dColor === c && "text-fg")}>{c}</span>
                        </label>
                      ))}
                    </div>
                  </fieldset>
                )}
                <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[13px]">
                  <span className="text-muted">Shows as</span>
                  <EnvChip
                    env={{ name: dName.trim() || env.name, color: dColor, protected: dProt }}
                    size="md"
                  />
                </div>
                <div className="flex flex-col gap-1.5">
                  <label htmlFor="e-name" className={label}>
                    Name
                  </label>
                  <input
                    id="e-name"
                    value={dName}
                    maxLength={100}
                    spellCheck={false}
                    aria-invalid={!!nameErr}
                    aria-describedby="e-name-h"
                    onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
                    className={cn(field, "font-mono text-[13px]", nameErr && "border-status-failed")}
                  />
                  <p id="e-name-h" className={cn(help, nameErr && "text-status-failed-fg")}>
                    {nameErr || "Shown in the switcher and on every chip."}
                  </p>
                </div>
              </div>
              {envChanges.length > 0 && (
                <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                  <span>
                    {envChanges.length === 1 ? "Unsaved change" : "Unsaved changes"}:{" "}
                    {envChanges.join(", ")}
                  </span>
                  <span className="flex items-center gap-2">
                    <button
                      type="button"
                      className={ghost}
                      onClick={() => setDraft({})}
                      disabled={saveEnv.isPending}
                    >
                      Cancel
                    </button>
                    <button
                      type="submit"
                      className={priSm}
                      disabled={!!nameErr || saveEnv.isPending}
                    >
                      {saveEnv.isPending ? "Saving…" : patch.protected === false ? "Save…" : "Save"}
                    </button>
                  </span>
                </div>
              )}
            </form>
          )}

          {(others.length > 0 || canDeploy) && (
            <section className={card} aria-labelledby="oe-h">
              <div className={cardHead}>
                <h2 id="oe-h" className={h2}>
                  Other environments
                </h2>
                {others.length > 0 && (
                  <span className={sum}>
                    {others.length - previews}
                    {previews > 0 && ` · ${plural(previews, "preview")}`}
                  </span>
                )}
              </div>
              {others.length > 0 && (
                <ul className="py-1">
                  {others.map((e) => {
                    const svcs = (allServices ?? []).filter((s) => s.environment_id === e.id);
                    const ids = [...new Set(svcs.map((s) => s.server_id ?? ""))];
                    const st = envState(svcs);
                    return (
                      <li key={e.id}>
                        <Link
                          href={`/projects/${projectId}/environments/${e.id}`}
                          className="flex min-h-11 items-center gap-3 px-4 py-2 text-[13px] hover:bg-raised/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus"
                        >
                          <span className="flex min-w-0 flex-1 flex-col items-start gap-1">
                            <EnvChip env={e} size="md" />
                            <span className="text-xs text-fg/80 [overflow-wrap:anywhere]">
                              {ids.length === 1 && `${serverName(ids[0], servers)} · `}
                              {ids.length > 1 && `${ids.length} servers · `}
                              {e.preview && `preview of !${e.preview.pull_number} · `}
                              {allServices && <span className={TONE_CLASSES[st.tone].text}>{st.label}</span>}
                            </span>
                          </span>
                          <span className="text-link">Switch</span>
                        </Link>
                      </li>
                    );
                  })}
                </ul>
              )}
              {canDeploy && (
                <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                  <span>Its name sets a new one’s colour and protection</span>
                  <button type="button" className={cn(link, "font-medium")} onClick={() => setCreatingEnv(true)}>
                    New environment
                  </button>
                </div>
              )}
            </section>
          )}
        </aside>
      </div>

      <CopyFromDialog
        open={copyingVars}
        onClose={() => setCopyingVars(false)}
        name={env.name}
        projectId={projectId}
        serviceId=""
        current={
          new Map(live.filter((r) => !r.removed).map((r) => [r.key, r.value]))
        }
        onMerge={(incoming) => {
          setRows((rs) => {
            const next = [...rs];
            for (const v of incoming) {
              const at = next.findIndex((r) => !r.removed && r.key === v.key);
              if (at >= 0) next[at] = { ...next[at], value: v.value, editing: true };
              else next.push({ id: newRowId(), key: v.key, value: v.value });
            }
            return next;
          });
          setCopyingVars(false);
          toast.success(
            `${incoming.length} ${incoming.length === 1 ? "variable" : "variables"} copied: review and save`,
          );
        }}
      />
      <CopyKeysDialog
        open={copyingKeys}
        onClose={() => setCopyingKeys(false)}
        name={env.name}
        projectId={projectId}
        serviceId=""
        target={scope}
        have={new Set((secrets ?? []).map((s) => s.key))}
        onDone={refreshSecrets}
      />
      <ReplaceSecretDialog
        keyName={replacing}
        onClose={() => setReplacing(null)}
        name={env.name}
        serviceId=""
        target={scope}
        onDone={refreshSecrets}
      />
      <NewEnvironmentDialog
        projectId={projectId}
        open={creatingEnv}
        onOpenChange={setCreatingEnv}
        onCreated={(e) => router.push(`/projects/${projectId}/environments/${e.id}`)}
      />
      <Dialog
        open={!!deletingSecret}
        onOpenChange={(o) => {
          if (!o && !deleteSecret.isPending) setDeletingSecret(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {deletingSecret}?</DialogTitle>
            <DialogDescription>
              It is removed from {env.name} now. Its services keep the value
              they run with until their next deploy, which starts them without
              it. The value can’t be recovered.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <button
              type="button"
              className={cn(btnSm, "h-9 px-3 text-[13px]")}
              onClick={() => setDeletingSecret(null)}
              disabled={deleteSecret.isPending}
            >
              Cancel
            </button>
            <button
              type="button"
              className="touch-target inline-flex h-9 items-center rounded-lg bg-danger px-3 text-[13px] font-medium text-on-danger hover:bg-danger/90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
              onClick={() =>
                deletingSecret && deleteSecret.mutate(deletingSecret)
              }
              disabled={deleteSecret.isPending}
            >
              {deleteSecret.isPending ? "Deleting…" : "Delete secret"}
            </button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={deleting}
        onOpenChange={(o) => {
          if (!o && !deleteEnv.isPending) {
            setDeleting(false);
            setConfirm("");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {env.name}?</DialogTitle>
            <DialogDescription>
              Its services, their data, deployments, shared variables, secrets
              and domains go. This can’t be undone; Toft asks for your
              password next.
            </DialogDescription>
          </DialogHeader>
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              if (confirm === env.name) deleteEnv.mutate();
            }}
          >
            <div className="flex flex-col gap-1.5">
              <label htmlFor="del-confirm" className={label}>
                Type <span className="font-mono">{env.name}</span> to confirm
              </label>
              <input
                id="del-confirm"
                value={confirm}
                autoFocus
                onChange={(e) => setConfirm(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                className={cn(field, "font-mono text-[13px]")}
              />
            </div>
            <DialogFooter>
              <button
                type="button"
                className={cn(btnSm, "h-9 px-3 text-[13px]")}
                onClick={() => {
                  setDeleting(false);
                  setConfirm("");
                }}
                disabled={deleteEnv.isPending}
              >
                Cancel
              </button>
              <button
                type="submit"
                className="touch-target inline-flex h-9 items-center rounded-lg bg-danger px-3 text-[13px] font-medium text-on-danger hover:bg-danger/90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                disabled={confirm !== env.name || deleteEnv.isPending}
              >
                {deleteEnv.isPending ? "Deleting…" : `Delete ${env.name}`}
              </button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
