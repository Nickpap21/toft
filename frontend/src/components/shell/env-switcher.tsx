"use client";

import { Fragment, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { ChevronDown, Plus } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { envLook, EnvChip } from "@/components/ui/env-chip";
import { NewEnvironmentDialog } from "@/app/projects/_lib/new-environment-dialog";
import type { Environment } from "@/lib/api/environment.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { useOrgRole } from "@/context/AuthContext";
import { statusLabel } from "@/lib/status";
import { cn } from "@/lib/utils";
import { ALL, type useEnvContext } from "./env-context";
import { BoxesIcon } from "./nav-icons";

/**
 * The project's environment switcher (D-55), in the project sidebar and the
 * phone's top bar (D-136): the environment the project is looked at in, or
 * All environments. On a service page it is pinned to the service's
 * environment and each other environment offers the service of the same
 * name there, or Add it.
 */
export function EnvSwitcher({
  projectId,
  environments,
  services,
  current,
  shown,
  ctx,
  variant,
  meta,
}: {
  projectId: string;
  environments: Environment[] | undefined;
  services: WorkspaceService[] | undefined;
  /** The service whose page this is, which pins the switcher. */
  current: WorkspaceService | undefined;
  /** The environment shown: the pinned one, or the chosen one. */
  shown: Environment | undefined;
  ctx: ReturnType<typeof useEnvContext>;
  /** "side": the sidebar's row, with meta beside the chip; "top": the
   *  phone's top bar, the chip alone. */
  variant: "side" | "top";
  /** Beside the chip in the sidebar, such as "4 running". */
  meta?: string;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const role = useOrgRole();
  const canManage = role === "owner" || role === "developer";
  const [creating, setCreating] = useState(false);
  const envHref = (id: string) => `/projects/${projectId}/environments/${id}`;

  const countsFor = (id: string) => {
    if (!services) return "";
    const own = services.filter((s) => s.environment_id === id);
    if (own.length === 0) return "No services";
    const by = own.reduce<Record<string, number>>((acc, s) => ({ ...acc, [s.runtime_status]: (acc[s.runtime_status] ?? 0) + 1 }), {});
    return Object.entries(by).map(([st, n]) => `${n} ${statusLabel(st).toLowerCase()}`).join(" · ");
  };
  const sibling = (id: string) => (current ? (services ?? []).find((s) => s.environment_id === id && s.name === current.name) : undefined);

  const choose = (id: string) => {
    ctx.set(id);
    if (current) {
      const twin = sibling(id);
      router.push(twin ? `/projects/${projectId}/services/${twin.id}` : `/projects/${projectId}/services`);
    } else if (pathname.startsWith(`${`/projects/${projectId}`}/environments/`)) {
      router.push(id === ALL ? `/projects/${projectId}` : envHref(id));
    }
  };

  if (!environments) return null;
  const label = shown ? shown.name : "All environments";
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          title={variant === "side" ? label : undefined}
          className={cn(
            "flex min-w-0 items-center gap-2 rounded-md text-xs text-muted transition-colors hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus",
            variant === "side"
              ? "h-8 w-full px-2 pointer-coarse:min-h-11 rail-collapsed:justify-center rail-collapsed:px-0"
              : "h-11 max-w-[140px] px-1",
          )}
        >
          {shown ? (
            <EnvChip
              env={shown}
              size="md"
              className="min-w-0 shrink"
              nameClassName={cn("min-w-0 truncate", variant === "side" && "rail-collapsed:sr-only")}
            />
          ) : (
            <>
              <BoxesIcon aria-hidden="true" className="hidden h-4 w-4 shrink-0 rail-collapsed:block" />
              <span className="truncate font-medium text-fg/80 rail-collapsed:sr-only">{label}</span>
            </>
          )}
          {variant === "side" && meta && <span className="flex-1 whitespace-nowrap text-left rail-collapsed:hidden">{meta}</span>}
          {variant === "side" && !meta && <span className="flex-1 rail-collapsed:hidden" />}
          {/* The name is what the button shows, then what it does (WCAG 2.5.3). */}
          <span className="sr-only">, switch environment</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 rail-collapsed:hidden" aria-hidden="true" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-[300px]">
          <DropdownMenuLabel>{current ? `${current.name} in other environments` : "Environment"}</DropdownMenuLabel>
          <DropdownMenuRadioGroup value={shown?.id ?? ALL} onValueChange={choose}>
            {[...environments.filter((e) => !e.preview), ...environments.filter((e) => e.preview)].map((e, i, all) => {
              const twin = current ? sibling(e.id) : undefined;
              const missing = !!current && !twin && e.id !== current.environment_id;
              // Previews follow the project's own environments (D-97).
              const firstPreview = !!e.preview && !all[i - 1]?.preview;
              return (
                <Fragment key={e.id}>
                {firstPreview && <DropdownMenuLabel className="pt-2 text-xs font-normal text-muted">Previews</DropdownMenuLabel>}
                <DropdownMenuRadioItem value={e.id} disabled={missing} className="gap-2.5">
                  <EnvChip env={e} />
                  <span className="min-w-0 flex-1 truncate text-xs text-muted">
                    {current ? (e.id === current.environment_id ? "this one" : twin ? statusLabel(twin.runtime_status) : `No ${current.name} here`) : countsFor(e.id)}
                  </span>
                  {missing && canManage && !e.preview && (
                    <Link
                      href={`/projects/${projectId}/services/new?env=${e.id}`}
                      className="pointer-events-auto text-xs text-link hover:underline"
                      onClick={(ev) => ev.stopPropagation()}
                    >
                      Add it
                    </Link>
                  )}
                </DropdownMenuRadioItem>
                </Fragment>
              );
            })}
            <DropdownMenuSeparator />
            {current ? (
              <DropdownMenuItem asChild>
                <Link href={`/projects/${projectId}/services`}>All services</Link>
              </DropdownMenuItem>
            ) : (
              <DropdownMenuRadioItem value={ALL} className="gap-2.5">
                <span className="text-[13px]">All environments</span>
                <span className="flex-1 text-xs text-muted">{services && `${services.length} services`}</span>
              </DropdownMenuRadioItem>
            )}
          </DropdownMenuRadioGroup>
          {canManage && (
            <DropdownMenuItem onSelect={() => setCreating(true)} className="text-fg/80">
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
              New environment…
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <NewEnvironmentDialog projectId={projectId} open={creating} onOpenChange={setCreating} onCreated={(e) => choose(e.id)} />
    </>
  );
}

/** The 2px neutral line along the top of a protected environment's pages
 *  (D-136, D-142). */
export function EnvHairline({ env }: { env: Pick<Environment, "color" | "protected"> | undefined }) {
  if (!env?.protected) return null;
  return <div aria-hidden="true" className={cn("h-0.5 w-full opacity-85", envLook(env).swatch)} />;
}
