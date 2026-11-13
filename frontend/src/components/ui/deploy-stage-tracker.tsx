import { CheckCircle2, RotateCw, XCircle } from "lucide-react";

export type DeployStage = "clone" | "pull" | "build" | "deploy" | "health" | "done" | "failed";

// Matches service.source_type values from the backend.
export type PipelineType = "git" | "docker-image" | "docker-compose";

export interface StageDef {
  key: DeployStage;
  label: string;
}

// Stages shown per pipeline type.
// docker-compose with a repo URL shows Clone; inline compose skips it.
const STAGE_SETS: Record<PipelineType, StageDef[]> = {
  "git": [
    { key: "clone",  label: "Clone"  },
    { key: "build",  label: "Build"  },
    { key: "deploy", label: "Deploy" },
    { key: "done",   label: "Live"   },
  ],
  "docker-image": [
    { key: "pull",   label: "Pull"   },
    { key: "deploy", label: "Deploy" },
    { key: "done",   label: "Live"   },
  ],
  "docker-compose": [
    { key: "clone",  label: "Clone"  },
    { key: "deploy", label: "Deploy" },
    { key: "done",   label: "Live"   },
  ],
};

// Inline compose (no repo URL) has no clone step.
const COMPOSE_INLINE_STAGES: StageDef[] = [
  { key: "deploy", label: "Deploy" },
  { key: "done",   label: "Live"   },
];

/** The pipeline's steps. A git or image service with a health check (retries
 *  above 0) has a Health check step before Live; compose stacks use their
 *  own file's checks (D-44). */
export function stageDefs(pipelineType: PipelineType, hasComposeRepo: boolean, hasHealthCheck = false): StageDef[] {
  if (pipelineType === "docker-compose") return hasComposeRepo ? STAGE_SETS[pipelineType] : COMPOSE_INLINE_STAGES;
  const stages = STAGE_SETS[pipelineType];
  if (!hasHealthCheck) return stages;
  return [...stages.slice(0, -1), { key: "health", label: "Health check" }, stages[stages.length - 1]];
}

// The lines the pipeline writes as it starts each step (internal/deployment
// and internal/docker). A step's lines run until the next step's marker.
const MARKERS: [RegExp, DeployStage][] = [
  [/^(\[1\/2\] )?cloning\b/i, "clone"],
  [/^\[1\/2\] pulling image\b/i, "pull"],
  [/^(\[2\/2\] building\b|building docker image\b|building \S+ with nixpacks|\[nixpacks\])/i, "build"],
  [/^(starting container\b|→ running migrate command|\[2\/2\] uploading repo|→ using inline compose)/i, "deploy"],
  [/^(→ health check:|→ waiting for the new container|→ checking that the new container)/i, "health"],
  [/^✓ deployment complete/i, "done"],
];

/** The step a deploy log line starts, or null for a line inside a step. */
export function lineStage(line: string): DeployStage | null {
  const text = line.trimStart();
  return MARKERS.find(([re]) => re.test(text))?.[1] ?? null;
}

/** How far a deploy's log got: the index in stages of the last step it
 *  started. Markers of steps not in stages are ignored. */
export function reachedIndex(lines: string[], stages: StageDef[]): number {
  let at = 0;
  for (const line of lines) {
    const i = stages.findIndex((s) => s.key === lineStage(line));
    if (i > at) at = i;
  }
  return at;
}

export function detectDeployStage(
  logs: string[],
  isDone: boolean,
  hasFailed: boolean,
  pipelineType: PipelineType = "git",
): DeployStage {
  if (hasFailed) return "failed";
  if (isDone) return "done";
  const stages = STAGE_SETS[pipelineType];
  return stages[reachedIndex(logs, stages)].key;
}

export function DeployStageTracker({
  stage,
  pipelineType = "git",
  hasComposeRepo = true,
}: {
  stage: DeployStage;
  pipelineType?: PipelineType;
  hasComposeRepo?: boolean;
}) {
  const stages = stageDefs(pipelineType, hasComposeRepo);
  const stageKeys = stages.map((s) => s.key);
  const currentIndex = stageKeys.indexOf(stage === "failed" ? stageKeys[stageKeys.length - 1] : stage);

  return (
    <div className="flex h-12 items-center justify-center gap-0 border-b border-subtle bg-surface px-6">
      {stages.map((s, i) => {
        const stageIdx = stageKeys.indexOf(s.key);
        const isPast   = stage !== "failed" && currentIndex > stageIdx;
        const isActive = stage !== "failed" && currentIndex === stageIdx && stage !== "done";
        const isAllDone = stage === "done";
        const isFailed  = stage === "failed" && i === stages.length - 1;

        return (
          <div key={s.key} className="flex items-center">
            <div className="flex flex-col items-center gap-0.5">
              {isAllDone || isPast ? (
                <CheckCircle2 className="h-4 w-4 text-status-running" aria-hidden="true" />
              ) : isActive ? (
                <RotateCw className="h-4 w-4 text-status-building motion-safe:animate-spin" aria-hidden="true" />
              ) : isFailed ? (
                <XCircle className="h-4 w-4 text-status-failed" aria-hidden="true" />
              ) : (
                <div className="h-4 w-4 rounded-full border-2 border-strong" aria-hidden="true" />
              )}
              <span className={`text-[10px] font-medium ${
                isAllDone || isPast ? "text-status-running-fg" :
                isActive             ? "text-status-building-fg" :
                isFailed             ? "text-status-failed-fg" :
                                       "text-muted"
              }`}>
                {s.label}
              </span>
            </div>
            {i < stages.length - 1 && (
              <div className={`w-16 h-px mx-2 mb-4 ${isAllDone || isPast ? "bg-status-running" : "bg-strong"}`} />
            )}
          </div>
        );
      })}
    </div>
  );
}
