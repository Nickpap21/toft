"use client";

import { ProjectIssuesCard } from "@/components/issues/settings";
import { PreviewsSettingsCard } from "@/components/previews/settings-card";
import { ProjectAgentsCard } from "@/components/agents/project-card";
import { useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ExternalLink } from "lucide-react";
import { ApiError, notFound } from "@/lib/api/client";
import { Read, ReadFailed } from "@/components/shell/unreachable";
import { apiKeyService, type APIKeyScope } from "@/lib/api/apikey.service";
import { observabilityService } from "@/lib/api/observability.service";
import { orgService } from "@/lib/api/org.service";
import { previewService } from "@/lib/api/preview.service";
import { projectService } from "@/lib/api/project.service";
import { serviceService, type Service } from "@/lib/api/service.service";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { useReauth } from "@/components/reauth";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  environmentsQuery,
  projectOverviewQuery,
  projectQuery,
  serversQuery,
  workspaceServicesQuery,
} from "@/lib/queries";
import { safeHref } from "@/lib/routes";
import { ago } from "@/lib/time";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { inProse, serverName } from "../../_lib/projects-view";
import { StatusText } from "../../_lib/service-actions";
import { ServiceMark } from "../../_lib/service-table";
import { DeleteData, useDeleteData } from "../../_lib/delete-data";
import {
  btnSm,
  card,
  field,
  ghostSm,
  h1,
  h2,
  help,
  label,
  outline,
  primarySm,
  saveBar,
  sum,
} from "../../_lib/ui";

// A project's settings (board: private/design/canvas/ReworkProjectSettings.dc.html,
// D-136): one scroll of cards, each saved on its own, with an "On this
// page" index from 1200 px and a Jump to select below it. Its name, its
// previews (D-97), what agents may do in it (D-104), deploys on push, its
// issues (D-83; kept by the founders, 2026-11-05), logs and metrics, the
// API keys scoped to it (D-46) and deleting it.

const join = (xs: string[]) =>
  xs.length <= 1
    ? xs.join("")
    : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`;
const link =
  "text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center";
const danger =
  "touch-target inline-flex min-h-9 max-w-full items-center rounded-lg bg-danger px-3 py-1.5 text-left text-[13px] [overflow-wrap:anywhere] font-medium text-on-danger hover:bg-danger/90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
/** A card the index jumps to. */
const section = cn(card, "focus:outline-none");

/** What a project key may do, in a few words. */
const CAN: Record<APIKeyScope, string> = {
  deploy: "deploy",
  variables: "change variables",
  code_read: "read code",
  code_write: "push code",
  checks: "report checks",
  issues_read: "read issues",
  issues_write: "change issues",
  docs_read: "read docs",
  docs_write: "write docs",
};
const can = (scopes: string[]) => {
  const words = scopes
    .filter((s) => !(s === "code_read" && scopes.includes("code_write")))
    .filter((s) => !(s === "issues_read" && scopes.includes("issues_write")))
    .filter((s) => !(s === "docs_read" && scopes.includes("docs_write")))
    .map((s) => CAN[s as APIKeyScope] ?? s);
  const t = join(words);
  return t.charAt(0).toUpperCase() + t.slice(1);
};

export default function ProjectSettingsPage() {
  const params = useParams();
  const router = useRouter();
  const projectId = params.id as string;
  const queryClient = useQueryClient();
  const role = useOrgRole();
  const { user } = useAuth();
  const canManage = role === "owner" || role === "developer";
  const isOwner = role === "owner";
  const { set: setBreadcrumbs } = useBreadcrumbs();

  const { data: project, isLoading, error: projectError, refetch: refetchProject } = useQuery(projectQuery(projectId));
  const environmentsRead = useQuery(environmentsQuery(projectId));
  const environments = environmentsRead.data;
  const overviewRead = useQuery(projectOverviewQuery(projectId));
  const overview = overviewRead.data;
  const { data: servers } = useQuery(serversQuery);
  const { data: envServices } = useQuery({
    queryKey: ["project-services", projectId],
    queryFn: () => serviceService.listForProject(projectId),
    enabled: !!project,
  });
  const { data: projectServices } = useQuery({
    queryKey: ["project-scoped-services", projectId],
    queryFn: () => serviceService.listProjectScoped(projectId),
    enabled: !!project,
  });
  // Every service of the workspace: the Loki, Prometheus or Grafana set may
  // be one in any project's environment, as on Observability.
  const { data: workspaceServices } = useQuery({
    ...workspaceServicesQuery,
    enabled: !!project,
  });
  const obsRead = useQuery({
    queryKey: ["observability-project", projectId],
    queryFn: () => observabilityService.getForProject(projectId),
    enabled: !!project,
  });
  const obs = obsRead.data;
  const webhookRead = useQuery({
    queryKey: ["webhook", projectId],
    queryFn: () => projectService.getWebhook(projectId),
    enabled: !!project && canManage,
  });
  const { data: webhook, dataUpdatedAt: webhookAt } = webhookRead;
  const { data: members } = useQuery({
    queryKey: ["org-members"],
    queryFn: () => orgService.listMembers(),
  });
  const keysRead = useQuery({
    queryKey: ["project-api-keys", projectId],
    queryFn: () => apiKeyService.listForProject(projectId),
    enabled: !!project,
  });
  const keys = keysRead.data;
  // The same list the Previews card reads, for the index's count.
  const { data: previews } = useQuery({
    queryKey: ["project-previews", projectId],
    queryFn: () => previewService.list(projectId).then((p) => p.data),
    enabled: !!project,
  });
  // What Delete removes, with sizes, for the owner.
  const deleteData = useDeleteData("projects", projectId, isOwner);

  useDocumentTitle(project ? `Settings · ${project.name} · Toft` : "Toft");
  useEffect(() => {
    if (!project) return;
    setBreadcrumbs([
      { label: "Projects", href: "/projects" },
      { label: project.name, href: `/projects/${projectId}` },
      { label: "Settings", href: "" },
    ]);
  }, [project, projectId, setBreadcrumbs]);

  // General: name and description, saved together.
  const [draft, setDraft] = useState<{
    name: string;
    description: string;
  } | null>(null);
  const name = draft?.name ?? project?.name ?? "";
  const description = draft?.description ?? project?.description ?? "";
  const generalChanges = project
    ? [
        name !== project.name ? "Name" : "",
        description !== project.description ? "Description" : "",
      ].filter(Boolean)
    : [];
  const save = useMutation({
    mutationFn: () =>
      projectService.update(projectId, {
        name: name.trim(),
        description: description.trim(),
      }),
    onSuccess: (p) => {
      queryClient.setQueryData(["project", projectId], p);
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      setDraft(null);
      toast.success("Project saved");
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof ApiError ? err.message : "Failed to save the project",
      ),
  });

  const [rotateArmed, setRotateArmed] = useState(false);
  const rotate = useMutation({
    mutationFn: () => projectService.rotateWebhook(projectId),
    onSuccess: (info) => {
      queryClient.setQueryData(["webhook", projectId], info);
      setRotateArmed(false);
      requestAnimationFrame(() => document.getElementById("ps-rotate")?.focus());
      toast.success(
        "New secret: paste it into each repository's webhook in GitHub",
      );
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof Error
          ? `Could not rotate the secret: ${err.message}`
          : "Could not rotate the secret",
      ),
  });

  const [deleting, setDeleting] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [deleteError, setDeleteError] = useState("");
  const confirmPassword = useReauth();
  const remove = useMutation({
    mutationFn: async () => {
      if (
        !(await confirmPassword(
          `Deleting ${project?.name} can’t be undone, so Toft asks for your password first.`,
        ))
      ) {
        throw new Error("The project stays without your password.");
      }
      return projectService.delete(projectId, confirm);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      toast.success(`${project?.name} deleted`);
      router.push("/projects");
    },
    onError: (err: unknown) =>
      setDeleteError(
        err instanceof Error && err.message
          ? err.message
          : "Failed to delete the project",
      ),
  });

  // ── Moving around the page ─────────────────────────────────────────────────
  const [current, setCurrent] = useState("general");
  const ownKeys = keys?.data ?? [];
  const openPreviews = (previews ?? []).filter((p) => p.status !== "removing").length;
  const toc: { id: string; label: string; count?: string }[] = project
    ? [
        { id: "general", label: "General" },
        { id: "previews", label: "Previews", count: openPreviews ? String(openPreviews) : undefined },
        { id: "agents", label: "Agents" },
        ...(canManage ? [{ id: "hook", label: "Deploy on push" }] : []),
        { id: "issues", label: "Issues" },
        { id: "obs", label: "Logs and metrics" },
        { id: "keys", label: "API keys", count: ownKeys.length ? `${ownKeys.length}${keys?.next ? "+" : ""}` : undefined },
        ...(isOwner ? [{ id: "delete", label: `Delete ${project.name}` }] : []),
      ]
    : [];
  // An instant jump (only overlays move, DESIGN.md → Motion); the section
  // takes focus, so Tab goes on from there.
  // From the Jump to select, focus stays there: arrow keys on a closed
  // select fire change on Windows and Linux.
  const jump = (id: string, focus = true) => {
    requestAnimationFrame(() => {
      const el = document.getElementById(`s-${id}`);
      if (!el) return;
      setCurrent(id);
      el.scrollIntoView({ block: "start" });
      if (focus) el.focus({ preventScroll: true });
    });
  };
  // The section in view: the first in the band below the phone's top bar
  // and the Jump to bar, down to 45% of the window.
  const tocIds = toc.map((t) => t.id).join();
  useEffect(() => {
    if (!tocIds) return;
    const ids = tocIds.split(",");
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) seen.set(e.target.id, e.isIntersecting);
        // At the page's end the last sections never reach the band: the
        // one jumped to stays marked.
        const main = document.getElementById("main");
        if (main && main.scrollTop > 0 && main.scrollTop + main.clientHeight >= main.scrollHeight - 2) return;
        const first = ids.find((id) => seen.get(`s-${id}`));
        if (first) setCurrent(first);
      },
      { rootMargin: "-128px 0px -55% 0px" },
    );
    for (const id of ids) {
      const el = document.getElementById(`s-${id}`);
      if (el) io.observe(el);
    }
    return () => io.disconnect();
  }, [tocIds]);

  if (isLoading)
    return (
      <div
        className="h-64 w-full max-w-[1600px] rounded-[10px] bg-surface motion-safe:animate-pulse"
        role="status"
        aria-label="Loading"
      />
    );
  if (!project && projectError && !notFound(projectError)) {
    return <ReadFailed error={projectError} onRetry={() => void refetchProject()} />;
  }
  if (!project) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className={h2}>Project not found</h1>
        <Link href="/projects" className={cn(outline, "self-start")}>
          All projects
        </Link>
      </section>
    );
  }

  const copy = (text: string, what: string) => {
    navigator.clipboard.writeText(text).then(
      () => toast.success(`${what[0].toUpperCase()}${what.slice(1)} copied`),
      () => toast.error(`Couldn't copy the ${what}`),
    );
  };

  // Where its services run, for the delete card.
  const all: Service[] = [...(envServices ?? []), ...(projectServices ?? [])];
  const serverNames = [
    ...new Set(
      all.map((s) => inProse(serverName(s.server_id, servers))).filter(Boolean),
    ),
  ];
  const creator = members?.find((m) => m.user_id === project.user_id);
  const nameErr = !name.trim() ? "A project needs a name." : "";

  // Logs and metrics: the services the project's Loki, Prometheus and Grafana are.
  const cfg = obs && "id" in obs ? obs : null;
  const shared = workspaceServices ?? [];
  const obsRows = cfg
    ? [
        { id: cfg.loki_service_id, name: "loki", slug: "", does: `Keeps the logs of ${project.name}’s containers` },
        { id: cfg.prometheus_service_id, name: "prometheus", slug: "prometheus", does: "Collects what its own configuration scrapes" },
        { id: cfg.grafana_service_id, name: "grafana", slug: "grafana", does: "Dashboards for the logs and metrics" },
      ].filter((r) => r.id)
    : [];
  const obsServer = cfg?.loki_service_id
    ? inProse(
        serverName(
          shared.find((s) => s.id === cfg.loki_service_id)?.server_id,
          servers,
        ),
      )
    : "";
  const grafana = cfg ? safeHref(cfg.grafana_url) : "";

  // What Delete removes, named in its dialog.
  const domains = (overview?.services ?? []).flatMap((o) => o.domains);
  const envCount = (environments ?? []).filter((e) => !e.preview).length;
  const projectNames = (projectServices ?? []).map((s) => s.name);

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h1 className={h1}>Settings</h1>
        <span className={sum}>
          {project.name} · each environment’s own are on its page
        </span>
      </div>

      {/* Sticks at <main>'s top edge, above its padding (p-4, md:p-8). */}
      <label data-jump-bar className="sticky -top-4 z-10 -my-2 flex items-center gap-2.5 border-b border-subtle bg-ground py-2 text-[13px] text-muted md:-top-8 min-[1200px]:hidden">
        Jump to
        <select
          aria-label="Jump to a settings section"
          className={cn(field, "h-9 max-w-[320px] flex-1 text-[13px] pointer-coarse:h-11")}
          value={current}
          onChange={(e) => jump(e.target.value, false)}
        >
          {toc.map((t) => (
            <option key={t.id} value={t.id}>
              {t.count ? `${t.label} (${t.count})` : t.label}
            </option>
          ))}
        </select>
      </label>

      <div className="grid items-start gap-6 min-[1200px]:grid-cols-[minmax(0,1fr)_240px]">
        <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
          <form
            id="s-general"
            tabIndex={-1}
            aria-labelledby="gen-h"
            className={section}
            onSubmit={(e) => {
              e.preventDefault();
              if (generalChanges.length && !nameErr) save.mutate();
            }}
          >
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
              <span className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
                <h2 id="gen-h" className={h2}>
                  General
                </h2>
                <span className={sum}>
                  created {project.created_at.slice(0, 10)}
                  {creator ? ` by ${creator.user_name || creator.user_email}` : ""}
                </span>
              </span>
              <button
                type="button"
                className={btnSm}
                onClick={() => copy(project.id, "project ID")}
              >
                Copy project ID
              </button>
            </div>
            <div className="grid gap-3.5 p-4 sm:grid-cols-2">
              <div className="flex min-w-0 flex-col gap-1.5">
                <label htmlFor="ps-name" className={label}>
                  Name
                </label>
                <input
                  id="ps-name"
                  value={name}
                  disabled={!canManage}
                  aria-invalid={!!nameErr || undefined}
                  aria-describedby="ps-name-help"
                  onChange={(e) =>
                    setDraft({ name: e.target.value, description })
                  }
                  className={cn(field, "font-mono text-[13px]", nameErr && "border-status-failed")}
                />
                <p id="ps-name-help" className={cn(help, nameErr && "text-status-failed-fg")}>
                  {nameErr || "Shown in the sidebar and the project switcher."}
                </p>
              </div>
              <div className="flex min-w-0 flex-col gap-1.5">
                <label htmlFor="ps-desc" className={label}>
                  Description{" "}
                  <span className="font-normal text-muted">optional</span>
                </label>
                <input
                  id="ps-desc"
                  value={description}
                  disabled={!canManage}
                  aria-describedby="ps-desc-help"
                  onChange={(e) =>
                    setDraft({ name, description: e.target.value })
                  }
                  className={cn(field, "text-[13px]")}
                />
                <p id="ps-desc-help" className={help}>
                  Shown under the name on Projects and the Overview.
                </p>
              </div>
            </div>
            {!canManage && (
              <p className={saveBar}>Owners and developers change the name and description.</p>
            )}
            {canManage && generalChanges.length > 0 && (
              <div className={saveBar}>
                <span>
                  Unsaved {generalChanges.length === 1 ? "change" : "changes"} to{" "}
                  {join(generalChanges)}
                </span>
                <span className="flex items-center gap-2">
                  <button type="button" className={ghostSm} onClick={() => setDraft(null)} disabled={save.isPending}>
                    Cancel
                  </button>
                  <button type="submit" className={primarySm} disabled={!!nameErr || save.isPending}>
                    {save.isPending ? "Saving…" : "Save"}
                  </button>
                </span>
              </div>
            )}
          </form>

          <PreviewsSettingsCard
            projectId={projectId}
            projectName={project.name}
            environments={environments}
            services={envServices ?? []}
            servers={servers ?? []}
            isOwner={isOwner}
          />

          <ProjectAgentsCard
            projectId={projectId}
            projectName={project.name}
            workspace={user?.orgName ?? "the workspace"}
            isOwner={isOwner}
            environments={environments ?? []}
            services={envServices ?? []}
          />

          {canManage && (
            <section id="s-hook" tabIndex={-1} aria-labelledby="hook-h" className={section}>
              <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-subtle px-4 py-3">
                <h2 id="hook-h" className={h2}>
                  Deploy on push
                </h2>
                {webhook && (
                  <span className={cn(sum, "min-w-0 [overflow-wrap:anywhere]")}>
                    {webhook.last_push_at
                      ? `last push ${ago(webhook.last_push_at, webhookAt)}${webhook.last_push_repo ? ` · ${webhook.last_push_repo}` : ""}`
                      : "no push yet"}
                  </span>
                )}
              </div>
              <Read
                query={webhookRead}
                what="the webhook"
                loading={<div className="h-28 motion-safe:animate-pulse" role="status" aria-label="Loading the webhook" />}
              >
                {(webhook) => (
                <div className="flex flex-col gap-3.5 p-4">
                  <p className="text-[13px] leading-[19px] text-fg/70">
                    Add this webhook once to each GitHub repository a service
                    builds from: a push to a service’s branch deploys that
                    service. Content type{" "}
                    <span className="font-mono text-xs">application/json</span>,
                    push events only.
                  </p>
                  <div className="flex min-w-0 flex-col gap-1.5">
                    <label htmlFor="ps-hook-url" className={label}>
                      Payload URL
                    </label>
                    <div className="flex gap-2">
                      <input
                        id="ps-hook-url"
                        readOnly
                        value={webhook.url}
                        className={cn(field, "min-w-0 flex-1 font-mono text-[13px]")}
                      />
                      <button
                        type="button"
                        className={cn(btnSm, "h-9")}
                        onClick={() => copy(webhook.url, "URL")}
                      >
                        Copy
                      </button>
                    </div>
                  </div>
                  <div className="flex min-w-0 flex-col gap-1.5">
                    <label htmlFor="ps-hook-secret" className={label}>
                      Secret
                    </label>
                    <div className="flex gap-2">
                      <input
                        id="ps-hook-secret"
                        type="password"
                        readOnly
                        value={webhook.secret}
                        className={cn(field, "min-w-0 flex-1 font-mono text-[13px]")}
                      />
                      <button
                        type="button"
                        className={cn(btnSm, "h-9")}
                        onClick={() => copy(webhook.secret, "secret")}
                      >
                        Copy
                      </button>
                    </div>
                  </div>
                  {rotateArmed ? (
                    <div
                      role="alert"
                      className="flex flex-wrap items-center gap-x-3 gap-y-2.5 rounded-lg border border-status-failed/40 bg-status-failed/5 px-3 py-2.5"
                    >
                      <span className="min-w-0 flex-[1_1_360px] text-[13px] leading-[19px] text-fg/80">
                        Pushes signed with the old secret stop deploying at
                        once. Paste the new secret into every GitHub
                        repository that uses this webhook.
                      </span>
                      <button
                        type="button"
                        className={ghostSm}
                        onClick={() => {
                          setRotateArmed(false);
                          requestAnimationFrame(() => document.getElementById("ps-rotate")?.focus());
                        }}
                        disabled={rotate.isPending}
                        autoFocus
                      >
                        Cancel
                      </button>
                      <button
                        type="button"
                        className="touch-target inline-flex h-[30px] items-center rounded-md bg-danger px-2.5 text-xs font-medium text-on-danger hover:bg-danger/90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                        onClick={() => rotate.mutate()}
                        disabled={rotate.isPending}
                      >
                        {rotate.isPending ? "Rotating…" : "Rotate secret"}
                      </button>
                    </div>
                  ) : (
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <button
                        type="button"
                        id="ps-rotate"
                        className={cn(ghostSm, "-ml-2.5")}
                        onClick={() => setRotateArmed(true)}
                      >
                        Rotate secret…
                      </button>
                      <span className={help}>
                        If it may have leaked. Pushes signed with the old one
                        stop deploying at once.
                      </span>
                    </div>
                  )}
                </div>
                )}
              </Read>
            </section>
          )}

          <ProjectIssuesCard projectId={projectId} isOwner={isOwner} />

          <section id="s-obs" tabIndex={-1} aria-labelledby="obs-h" className={section}>
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-subtle px-4 py-3">
              <h2 id="obs-h" className={h2}>
                Logs and metrics
              </h2>
              {obs && (
                <span className={sum}>
                  {!cfg || obsRows.length === 0
                    ? "none yet"
                    : `${cfg.project_id ? "its own" : "the workspace’s"}${obsServer ? `, on ${obsServer}` : ""}`}
                </span>
              )}
            </div>
            <Read
              query={obsRead}
              what="the logs and metrics services"
              isEmpty={() => obsRows.length === 0}
              empty={
                <p className="px-4 py-3.5 text-[13px] leading-[19px] text-fg/70">
                  Add Loki, Prometheus and Grafana from the catalog, as workspace
                  services, and every project’s logs and metrics go there.
                </p>
              }
            >
              {() => (
              <ul aria-label="Logs and metrics services" className="flex flex-col pb-2 pt-1">
                {obsRows.map((r) => {
                  const s = shared.find((x) => x.id === r.id);
                  return (
                    <li key={r.name} className="flex min-h-9 items-center gap-2.5 px-4 py-1 text-[13px]">
                      <ServiceMark slug={s?.addon_slug ?? r.slug} className="mt-0 self-center" />
                      <span className="flex min-w-0 flex-1 flex-col">
                        <span className="font-medium text-fg">{s?.name ?? r.name}</span>
                        <span className="text-xs text-muted">{r.does}</span>
                      </span>
                      {s ? (
                        <StatusText status={s.runtime_status} className="text-xs" />
                      ) : workspaceServices ? (
                        <Link href="/shared?tab=logs" className={cn(link, "text-xs")}>
                          Not found: set it on Logs and metrics
                        </Link>
                      ) : null}
                    </li>
                  );
                })}
              </ul>
              )}
            </Read>
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-t border-subtle px-4 py-2.5">
              <Link href="/shared" className={link}>
                Workspace services
              </Link>
              {grafana && (
                <a href={grafana} target="_blank" rel="noopener noreferrer" className={cn(link, "items-center gap-1")}>
                  Open Grafana
                  <ExternalLink className="h-3 w-3" aria-hidden="true" />
                  <span className="sr-only">(opens in a new tab)</span>
                </a>
              )}
            </div>
          </section>

          <section id="s-keys" tabIndex={-1} aria-labelledby="keys-h" className={section}>
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-subtle px-4 py-3">
              <span className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
                <h2 id="keys-h" className={h2}>
                  API keys
                </h2>
                {keys && (
                  <span className={sum}>
                    {ownKeys.length}
                    {keys.next ? "+" : ""} scoped to {project.name}
                    {!isOwner && ownKeys.length > 0 ? ", yours" : ""}
                  </span>
                )}
              </span>
              <Link href="/settings/api-keys" className={link}>
                All API keys
              </Link>
            </div>
            <Read
              query={keysRead}
              what="the API keys"
              isEmpty={() => ownKeys.length === 0}
              empty={
                <p className="px-4 py-3.5 text-[13px] text-fg/70">
                  {isOwner
                    ? `No key is scoped to ${project.name} yet.`
                    : `You have no key scoped to ${project.name}.`}
                </p>
              }
            >
              {() => (
              <table aria-label={`API keys scoped to ${project.name}`} className="w-full border-collapse text-[13px] max-sm:block">
                <thead className="max-sm:hidden">
                  <tr>
                    <th scope="col" className="border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted">Key</th>
                    <th scope="col" className="border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted">Can</th>
                    {isOwner && <th scope="col" className="border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted">Owner</th>}
                  </tr>
                </thead>
                <tbody className="max-sm:block">
                  {ownKeys.map((k) => (
                    <tr key={k.id} className="max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-0.5 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-2.5 max-sm:first:border-t-0">
                      <td className="border-t border-subtle px-4 py-2.5 font-mono text-xs text-fg [overflow-wrap:anywhere] [tr:first-child>&]:border-t-0 max-sm:border-0 max-sm:p-0">
                        {k.name}
                      </td>
                      <td className="border-t border-subtle px-4 py-2.5 text-fg [tr:first-child>&]:border-t-0 max-sm:col-span-2 max-sm:row-start-2 max-sm:border-0 max-sm:p-0">
                        {can(k.scopes)}
                      </td>
                      {isOwner && (
                        <td className="border-t border-subtle px-4 py-2.5 text-muted [overflow-wrap:anywhere] [tr:first-child>&]:border-t-0 max-sm:col-start-2 max-sm:row-start-1 max-sm:border-0 max-sm:p-0 max-sm:text-right">
                          {k.owner_name}
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
              )}
            </Read>
            <p className="border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
              A key scoped to {project.name} works only here, with its
              owner’s role.
              {!isOwner && " Owners see every member’s."}
            </p>
          </section>

          {isOwner && (
            <section
              id="s-delete"
              tabIndex={-1}
              aria-labelledby="del-h"
              className={cn(section, "border-status-failed/40")}
            >
              <div className="flex flex-wrap items-center gap-x-4 gap-y-3 p-4">
                <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-1">
                  <h2 id="del-h" className={h2}>
                    Delete {project.name}
                  </h2>
                  <p className="text-[13px] leading-[19px] text-muted">
                    Removes every service{serverNames.length ? ` from ${join(serverNames)}` : ""} with
                    its containers, volumes and data, images and config files.
                    Then the project with its{" "}
                    {[
                      environments ? `${envCount} environment${envCount === 1 ? "" : "s"}` : "environments",
                      ...projectNames,
                      "deployments, variables, secrets, domains and API keys",
                    ].join(", ")}
                    . Workspace services stay.
                  </p>
                </div>
                <button type="button" className={danger} onClick={() => setDeleting(true)}>
                  Delete {project.name}…
                </button>
              </div>
            </section>
          )}
        </div>

        <aside className="sticky top-5 hidden min-[1200px]:block">
          <nav aria-labelledby="toc-h" className={card}>
            <div className="border-b border-subtle px-4 py-3">
              <h2 id="toc-h" className={h2}>
                On this page
              </h2>
            </div>
            <ul className="flex flex-col p-1.5">
              {toc.map((t) => (
                <li
                  key={t.id}
                  className={cn(t.id === "delete" && "mt-1.5 border-t border-subtle pt-1.5")}
                >
                  <button
                    type="button"
                    aria-current={current === t.id ? "true" : undefined}
                    onClick={() => jump(t.id)}
                    className={cn(
                      "flex min-h-[34px] w-full items-center gap-2 rounded-lg px-2.5 text-left text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:min-h-11",
                      current === t.id ? "bg-selected" : "hover:bg-raised",
                      t.id === "delete"
                        ? "text-status-failed-fg"
                        : current === t.id
                          ? "text-fg"
                          : "text-fg/85",
                    )}
                  >
                    {t.label}
                    {t.count && (
                      <span className="ml-auto font-mono text-xs text-muted">
                        {t.count}
                      </span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          </nav>
        </aside>
      </div>

      <Dialog
        open={deleting}
        onOpenChange={(o) => {
          if (!o && !remove.isPending) {
            setDeleting(false);
            setConfirm("");
            setDeleteError("");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {project.name}?</DialogTitle>
            <DialogDescription>
              Its services, their data, deployments, variables, secrets,
              domains and API keys go. This can’t be undone; Toft asks for
              your password next.
            </DialogDescription>
          </DialogHeader>
          <form
            className="flex min-w-0 flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              if (confirm === project.name) remove.mutate();
            }}
          >
            <dl className="flex flex-col rounded-lg border border-subtle text-[13px]">
              {[
                { k: "Environments", v: <Read query={environmentsRead} what="the environments" loading="—">{(list) => join(list.filter((e) => !e.preview).map((e) => e.name)) || "none"}</Read> },
                { k: "Data", v: <DeleteData {...deleteData} perService /> },
                { k: "Domains", v: <Read query={overviewRead} what="the domains" loading="—">{() => (domains.length ? join(domains) : "none")}</Read> },
                { k: "API keys", v: <Read query={keysRead} what="the API keys" loading="—">{() => (ownKeys.length ? join(ownKeys.map((k) => k.name)) : "none")}</Read> },
              ].map((d, i) => (
                <div
                  key={d.k}
                  className={cn("grid gap-x-3 gap-y-0.5 px-3 py-2 sm:grid-cols-[110px_minmax(0,1fr)]", i > 0 && "border-t border-subtle")}
                >
                  <dt className="text-muted">{d.k}</dt>
                  <dd className="min-w-0 text-fg [overflow-wrap:anywhere]">{d.v}</dd>
                </div>
              ))}
            </dl>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="ps-confirm" className={label}>
                Type <span className="font-mono">{project.name}</span> to confirm
              </label>
              <input
                id="ps-confirm"
                value={confirm}
                autoFocus
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => {
                  setConfirm(e.target.value);
                  setDeleteError("");
                }}
                className={cn(field, "font-mono text-[13px]")}
              />
            </div>
            {deleteError && (
              <p role="alert" className="text-[13px] text-status-failed-fg">
                {deleteError}
              </p>
            )}
            <DialogFooter>
              <button
                type="button"
                className={cn(btnSm, "h-9 px-3 text-[13px]")}
                onClick={() => {
                  setDeleting(false);
                  setConfirm("");
                  setDeleteError("");
                }}
                disabled={remove.isPending}
              >
                Cancel
              </button>
              <button
                type="submit"
                className={danger}
                disabled={confirm !== project.name || remove.isPending}
              >
                {remove.isPending ? "Deleting…" : `Delete ${project.name}`}
              </button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
