import type { DeployStrategy, Service } from "@/lib/api/service.service";
import { cn } from "@/lib/utils";
import type { ConfigForm } from "./settings-form";

const mono = "font-mono text-xs";

const ROLLING = "The new container starts and passes its health check while the previous one serves: no downtime, but both run for a moment, which a database's files do not survive.";
const RECREATE = "The previous container stops before the new one starts, and starts again if the new one fails: a few seconds of downtime, never two containers on the same data.";

/** The sentence under Settings → Deploy strategy for the chosen value. What
 *  Automatic comes to is known only for the saved choice (the server decides
 *  it from the image or catalog item). */
export function deployStrategyHint(value: DeployStrategy, service: Pick<Service, "deploy_strategy" | "effective_deploy_strategy">): string {
  if (value === "rolling") return ROLLING;
  if (value === "recreate") return RECREATE;
  if ((service.deploy_strategy ?? "auto") === "auto") {
    if (service.effective_deploy_strategy === "recreate") {
      return "Stop then start, as for every database or cache: a few seconds of downtime per deploy, never two containers on the same data.";
    }
    if (service.effective_deploy_strategy === "rolling") {
      return "Zero downtime, as for every service that is not a database or cache.";
    }
  }
  return "Stop then start for a database or cache, zero downtime for anything else.";
}

const STRATEGIES: { id: DeployStrategy; label: string }[] = [
  { id: "auto", label: "Automatic" },
  { id: "rolling", label: "Zero downtime" },
  { id: "recreate", label: "Stop then start" },
];

/** Settings → Deploy's strategy, for a single-container service (D-155).
 *  The field is the form's; the card saves it. */
export function DeployStrategyField({
  service,
  form,
  set,
  canEdit,
}: {
  service: Service;
  form: ConfigForm;
  set: (patch: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1.5 text-[13px] text-fg">Deploy strategy</legend>
      <div
        role="radiogroup"
        aria-label="Deploy strategy"
        className="inline-flex flex-wrap gap-0.5 self-start rounded-lg border border-subtle bg-ground p-0.5"
      >
        {STRATEGIES.map((st) => (
          <label
            key={st.id}
            className={cn(
              "inline-flex h-8 cursor-pointer items-center gap-2 rounded-md px-2.5 text-[13px] pointer-coarse:min-h-11",
              form.deploy_strategy === st.id
                ? "bg-raised text-fg"
                : "text-muted hover:text-fg",
            )}
          >
            <input
              type="radio"
              name="deploy-strategy"
              value={st.id}
              checked={form.deploy_strategy === st.id}
              disabled={!canEdit}
              onChange={() => set({ deploy_strategy: st.id })}
              className="h-3.5 w-3.5 accent-[var(--link)]"
            />
            {st.label}
          </label>
        ))}
      </div>
      <p className="text-xs leading-[17px] text-muted">
        {deployStrategyHint(form.deploy_strategy, service)} Applies from the
        next deploy.
      </p>
      <details>
        <summary className="cursor-pointer text-xs text-muted hover:text-fg pointer-coarse:min-h-11">
          How each strategy deploys
        </summary>
        <dl className="mt-2 overflow-hidden rounded-lg border border-subtle text-[13px]">
          <div className="grid gap-1 px-3 py-2.5 sm:grid-cols-[120px_1fr] sm:gap-3">
            <dt className="text-muted">Zero downtime</dt>
            <dd className="text-fg/85">
              The new container starts and passes its health check while the
              previous one serves. Both run for a moment, which a
              database&apos;s files don&apos;t survive.
            </dd>
          </div>
          <div className="grid gap-1 border-t border-subtle px-3 py-2.5 sm:grid-cols-[120px_1fr] sm:gap-3">
            <dt className="text-muted">Stop then start</dt>
            <dd className="text-fg/85">
              The previous container stops before the new one starts, and starts
              again if the new one fails: a few seconds of downtime, never two
              containers on the same data. Automatic picks it for databases and
              caches, such as <span className={mono}>postgres</span> and{" "}
              <span className={mono}>redis</span>.
            </dd>
          </div>
        </dl>
      </details>
    </fieldset>
  );
}
