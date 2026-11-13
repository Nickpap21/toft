"use client";

import { Fragment, Suspense, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Plus } from "lucide-react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@/lib/api/client";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { deployState } from "@/components/home/home";
import {
  projectService,
  type CreateProjectInput,
} from "@/lib/api/project.service";
import { dashboardQuery } from "@/lib/api/dashboard.service";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import {
  projectsQuery,
  serversQuery,
  workspaceEnvironmentsQuery,
  workspaceServicesQuery,
} from "@/lib/queries";
import { TONE_CLASSES } from "@/lib/status";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { ago, projectsTable } from "./_lib/projects-view";
import {
  card,
  field,
  h1,
  help,
  label,
  mono,
  outline,
  primary,
  th,
} from "./_lib/ui";
import { EnvChip, envChip } from "@/components/ui/env-chip";
import { PROJECT_LETTER } from "@/components/shell/sidebar-projects";
import { LoadFailed, Read, readFailed } from "@/components/shell/unreachable";

// All projects (board: private/design/canvas/ReworkProjects.dc.html).

/** "1 failed · 4 running", "11 issues · 1 open PR": wraps between its
 *  parts, never inside one. */
function Parts({ text }: { text: string }) {
  const parts = text.split(" · ");
  return parts.map((part, i) => (
    <Fragment key={part}>
      <span className="whitespace-nowrap">
        {part}
        {i < parts.length - 1 && " ·"}
      </span>
      {i < parts.length - 1 && " "}
    </Fragment>
  ));
}

/** D-46: relative under a day, else the ISO time in mono. */
function When({ iso, now }: { iso: string; now: number }) {
  const s = ago(iso, now);
  return <span className={/^\d{4}-/.test(s) ? mono : undefined}>{s}</span>;
}

export default function ProjectsPage() {
  // useSearchParams needs a boundary on a page Next prerenders.
  return (
    <Suspense>
      <Projects />
    </Suspense>
  );
}

function Projects() {
  const role = useOrgRole();
  const { user } = useAuth();
  const router = useRouter();
  const canManage = role === "owner" || role === "developer";
  const queryClient = useQueryClient();
  const workspace = user?.orgName ?? "this workspace";
  useDocumentTitle(
    `Projects · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`,
  );

  // /projects?new opens the form (Home's and the sidebar's New project),
  // also when the page is already open; closing it drops ?new again.
  const wantsNew = useSearchParams().has("new");
  const [showCreate, setShowCreate] = useState(wantsNew);
  const [seenNew, setSeenNew] = useState(wantsNew);
  const [form, setForm] = useState<CreateProjectInput>({
    name: "",
    description: "",
  });
  if (wantsNew !== seenNew) {
    setSeenNew(wantsNew);
    if (wantsNew) {
      setForm({ name: "", description: "" });
      setShowCreate(true);
    }
  }
  const closeCreate = () => {
    setShowCreate(false);
    if (wantsNew) router.replace("/projects", { scroll: false });
  };
  const [filter, setFilter] = useState("");

  const projectsRead = useQuery(projectsQuery);
  const projects = projectsRead.data;
  const servicesRead = useQuery(workspaceServicesQuery);
  const services = servicesRead.data;
  const environmentsRead = useQuery(workspaceEnvironmentsQuery);
  const environments = environmentsRead.data;
  const { data: servers } = useQuery(serversQuery);
  // Each project's latest deploy; deploy events refresh it (useEventStream).
  const { data: dashboard, dataUpdatedAt } = useQuery({
    ...dashboardQuery,
    refetchInterval: false,
  });
  // Times are relative to the last fetch of what they belong to.
  const now = dataUpdatedAt;

  const createMutation = useMutation({
    mutationFn: (input: CreateProjectInput) => projectService.create(input),
    onSuccess: (project) => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["workspace-environments"] });
      toast.success(`${project.name} created, with a production environment`);
      closeCreate();
      router.push(`/projects/${project.id}`);
    },
  });

  function openCreate() {
    setForm({ name: "", description: "" });
    setShowCreate(true);
  }

  function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!(form.name ?? "").trim()) return;
    createMutation.mutate({
      name: (form.name ?? "").trim(),
      description: form.description?.trim(),
    });
  }

  const rows = projectsTable(
    projects ?? [],
    services ?? [],
    environments ?? [],
    dashboard?.project_deploys ?? [],
    servers,
  );
  const q = filter.trim().toLowerCase();
  const shown = q ? rows.filter((r) => r.name.toLowerCase().includes(q)) : rows;
  const has = (projects?.length ?? 0) > 0;
  const inProjects = (services ?? []).filter((s) => s.project_id !== "").length;
  const shared = (services ?? []).filter((s) => s.project_id === "").map((s) => s.name);
  // What a row says of its services needs both reads.
  const known = !!services && !!environments;
  // Phone: each row a grid (the board's .pjt below 640 px).
  const cell = "border-t border-subtle px-4 py-3 align-middle max-sm:block max-sm:border-0 max-sm:p-0";

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 max-sm:flex-col max-sm:items-start max-sm:gap-0.5">
          <h1 className={h1}>Projects</h1>
          <span className="text-[13px] text-muted">
            {projects
              ? has
                ? `${projects.length} in ${workspace} · ${!services ? "— services" : inProjects === 1 ? "1 service" : `${inProjects} services`}`
                : `None in ${workspace} yet`
              : " "}
          </span>
        </div>
        {has && canManage && (
          <button type="button" className={cn(primary, "max-sm:w-full max-sm:justify-center")} onClick={openCreate}>
            <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
            New project
          </button>
        )}
      </div>

      <Read
        query={projectsRead}
        what="the projects"
        heading="h2"
        loading={
          <div
            className="h-48 rounded-[10px] bg-surface motion-safe:animate-pulse"
            aria-label="Loading projects"
          />
        }
        isEmpty={(list) => list.length === 0}
        empty={
        <section
          aria-labelledby="empty-h"
          className={cn(card, "flex max-w-[640px] flex-col items-start gap-3 p-8 max-sm:p-5")}
        >
          <h2 id="empty-h" className="text-[17px] font-semibold text-fg">
            Start with a project
          </h2>
          <p className="text-sm leading-[21px] text-fg/75">
            A project holds an app&apos;s services and the environments they run
            in, with its code, issues and docs beside them. It starts with
            production, protected; you add staging and previews when you want
            them.
          </p>
          {canManage ? (
            <button type="button" className={primary} onClick={openCreate}>
              <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
              New project
            </button>
          ) : (
            <p className="text-[13px] text-muted">
              Ask an owner or a developer of {workspace} to create one.
            </p>
          )}
        </section>
        }
      >
        {() => (
        <section aria-labelledby="pj-h" className={card}>
          <h2 id="pj-h" className="sr-only">All projects</h2>
          {readFailed(servicesRead) && (
            <div className="border-b border-subtle px-4 py-3">
              <LoadFailed what="the services" onRetry={() => void servicesRead.refetch()} />
            </div>
          )}
          {readFailed(environmentsRead) && (
            <div className="border-b border-subtle px-4 py-3">
              <LoadFailed what="the environments" onRetry={() => void environmentsRead.refetch()} />
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2 border-b border-subtle px-4 py-3">
            <input
              type="search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter projects"
              aria-label="Filter projects"
              className={cn(field, "h-8 max-w-[320px] flex-[1_1_220px] text-[13px] max-sm:h-11 max-sm:max-w-none")}
            />
            <span className={help}>Sorted by what needs you first</span>
          </div>
          <table
            aria-label={`Projects in ${workspace}`}
            className="w-full border-collapse text-[13px] max-sm:block sm:table-fixed"
          >
            <thead className="max-sm:hidden">
              <tr className="border-b border-subtle">
                <th scope="col" className={cn(th, "w-[27%] py-2")}>Project</th>
                <th scope="col" className={cn(th, "w-[24%] py-2")}>Environments</th>
                <th scope="col" className={cn(th, "w-[18%] py-2")}>Production</th>
                <th scope="col" className={cn(th, "py-2 sm:max-lg:hidden")}>Work</th>
                <th scope="col" className={cn(th, "w-[184px] py-2 text-right")}>Last deploy</th>
              </tr>
            </thead>
            <tbody className="max-sm:block">
              {shown.length === 0 && (
                <tr className="max-sm:block">
                  <td colSpan={5} className={cn(cell, "text-muted max-sm:px-4 max-sm:py-3")}>
                    No project matches &ldquo;{filter}&rdquo;.
                  </td>
                </tr>
              )}
              {shown.map((p, i) => {
                const state = p.last && deployState(p.last.status);
                const total = p.production.bar.reduce((n, b) => n + b.n, 0);
                return (
                  <tr
                    key={p.id}
                    className={cn(
                      "max-sm:relative max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3",
                      "[&:first-child>td]:border-t-0",
                      i > 0 && "max-sm:border-t max-sm:border-subtle",
                    )}
                  >
                    <td className={cn(cell, "max-sm:col-span-2")}>
                      <Link
                        href={`/projects/${p.id}`}
                        className="inline-flex max-w-full items-center gap-2.5 font-medium text-fg [overflow-wrap:anywhere] hover:underline max-sm:after:absolute max-sm:after:inset-0 max-sm:after:content-['']"
                      >
                        <span
                          aria-hidden="true"
                          className={cn("flex h-[18px] w-[18px] flex-none items-center justify-center rounded text-[11px] font-bold", PROJECT_LETTER[p.health])}
                        >
                          {p.name.trim().charAt(0).toUpperCase()}
                        </span>
                        <span className="min-w-0">{p.name}</span>
                      </Link>
                      {p.description && (
                        <p title={p.description} className={cn(help, "mt-0.5 line-clamp-2 pl-7 max-sm:pl-0")}>
                          {p.description}
                        </p>
                      )}
                    </td>
                    <td className={cn(cell, "max-sm:col-span-2")}>
                      <div className="flex flex-wrap items-center gap-1.5">
                        {p.environments.map((e) => (
                          <EnvChip key={e.id} env={e} size="md" className="max-w-full overflow-hidden" />
                        ))}
                        {p.previews.length > 0 && (
                          <span
                            title={p.previews.join(", ")}
                            className={cn("inline-flex h-[22px] items-center rounded px-2 font-mono text-xs", envChip("blue").chip)}
                          >
                            {p.previews.length === 1 ? "1 preview" : `${p.previews.length} previews`}
                          </span>
                        )}
                      </div>
                      {known && <p className={cn(help, "mt-1")}>{p.where}</p>}
                    </td>
                    <td className={cn(cell, "max-sm:col-start-1")}>
                      {total > 0 && (
                        <span
                          role="img"
                          aria-label={`${total === 1 ? "1 service" : `${total} services`} in ${p.environments[0]?.name ?? "production"}: ${p.production.servicesText}`}
                          className="mb-1.5 flex h-1.5 w-[120px] max-w-full overflow-hidden rounded-sm"
                        >
                          {p.production.bar.map((b) => (
                            <span key={b.tone} className={TONE_CLASSES[b.tone].dot} style={{ flex: b.n }} />
                          ))}
                        </span>
                      )}
                      <span className={p.production.servicesTone === "failed" ? TONE_CLASSES.failed.text : total ? "text-fg/80" : "text-muted"}>
                        {known ? <Parts text={p.production.servicesText} /> : "—"}
                      </span>
                    </td>
                    <td className={cn(cell, "text-fg/70 max-sm:col-span-2 max-sm:row-start-4 sm:max-lg:hidden", !p.work && "max-sm:hidden")}>
                      {p.work ? (
                        <Parts text={p.work} />
                      ) : (
                        <span className="text-muted">—</span>
                      )}
                    </td>
                    <td className={cn(cell, "text-right max-sm:col-start-2 max-sm:row-start-3 max-sm:self-end")}>
                      {p.last && state ? (
                        <>
                          <span className={cn("font-medium", TONE_CLASSES[state.tone].text)}>{state.label}</span>
                          <div className={cn(help, "[overflow-wrap:anywhere]")}>
                            {p.last.service_name} · <span className="whitespace-nowrap"><When iso={p.last.created_at} now={now} /></span>
                          </div>
                        </>
                      ) : (
                        <span className="text-muted">—</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {shared.length > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
              <span>
                {shared.length === 1 ? "1 workspace service runs" : `${shared.length} workspace services run`} beside the
                projects: {new Intl.ListFormat("en", { type: "conjunction" }).format(shared)}.
              </span>
              <Link href="/shared" className="text-link hover:underline">
                Workspace services
              </Link>
            </div>
          )}
        </section>
        )}
      </Read>

      <Dialog
        open={showCreate && canManage}
        onOpenChange={(o) => (o ? setShowCreate(true) : closeCreate())}
      >
        <DialogContent className="flex flex-col overflow-hidden p-0 sm:top-16 sm:max-h-[calc(100dvh-96px)] sm:translate-y-0 max-sm:inset-0 max-sm:h-full max-sm:max-h-none max-sm:w-full max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none max-sm:border-0 max-w-[560px]">
          <form onSubmit={handleCreate} className="flex min-h-0 flex-1 flex-col">
          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-5">
            <DialogHeader className="text-left">
              <DialogTitle className="text-[17px] font-semibold leading-6">New project</DialogTitle>
            </DialogHeader>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="np-name" className={label}>
                Name
              </label>
              <input
                id="np-name"
                autoFocus
                required
                placeholder="my-project"
                value={form.name}
                onChange={(e) =>
                  setForm((f) => ({ ...f, name: e.target.value }))
                }
                className={cn(field, "font-mono text-[13px]")}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="np-desc" className={label}>
                Description{" "}
                <span className="font-normal text-muted">optional</span>
              </label>
              <input
                id="np-desc"
                placeholder="What it is, in a few words"
                value={form.description ?? ""}
                onChange={(e) =>
                  setForm((f) => ({ ...f, description: e.target.value }))
                }
                className={field}
              />
            </div>
            <div className="flex flex-col gap-1.5 rounded-lg border border-subtle bg-ground px-3 py-2.5 text-[13px] text-muted">
              <span className="flex items-center gap-2 text-fg">
                Starts with <EnvChip env={{ name: "production", color: "gray", protected: true }} size="md" />
              </span>
              <DialogDescription className="text-[13px] leading-5 text-muted">
                Protected: deploys and deletes confirm first. Then you add its
                first service from a repository, an image, a compose file or
                the catalog.
              </DialogDescription>
            </div>
            {createMutation.error && (
              <p role="alert" className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
                {createMutation.error instanceof ApiError && createMutation.error.status < 500 ? createMutation.error.message : "The project was not created."}
              </p>
            )}
          </div>
            <div className="flex flex-none flex-wrap justify-end gap-2 border-t border-subtle px-4 py-2.5 max-sm:pb-[calc(10px+env(safe-area-inset-bottom))]">
              <button type="button" className={cn(outline, "justify-center max-sm:flex-1")} onClick={closeCreate}>
                Cancel
              </button>
              <button
                type="submit"
                className={cn(primary, "justify-center max-sm:order-first max-sm:basis-full")}
                disabled={createMutation.isPending || !(form.name ?? "").trim()}
              >
                {createMutation.isPending ? "Creating…" : "Create project"}
              </button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
