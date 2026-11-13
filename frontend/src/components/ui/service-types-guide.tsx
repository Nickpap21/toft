"use client";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { GitBranch, Box, Layers, Package } from "lucide-react";

export interface ServiceTypesGuideProps {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}

const SERVICE_TYPES = [
  {
    icon: GitBranch,
    iconColor: "text-muted",
    iconBg: "bg-raised",
    title: "Git application",
    tagline: "Source code → build pipeline → container",
    bullets: [
      "Clones your repo and builds from source",
      "Dockerfile or Nixpacks buildpack",
      "Slowest deploys (build takes time)",
      "Full CI alternative — PaaS handles the pipeline",
    ],
    example: "git_repo_url: https://github.com/org/app",
  },
  {
    icon: Box,
    iconColor: "text-muted",
    iconBg: "bg-raised",
    title: "Docker image",
    tagline: "Pre-built image → instant deploys",
    bullets: [
      "Pulls from any registry (Docker Hub, ghcr.io, ECR…)",
      "No build step — fastest deploy path",
      "You own the CI pipeline that builds the image",
      "Supports private registries with credentials",
    ],
    example: "docker_image: ghcr.io/org/app:latest",
  },
  {
    icon: Layers,
    iconColor: "text-muted",
    iconBg: "bg-raised",
    title: "Compose stack",
    tagline: "Multi-container from docker-compose.yml",
    bullets: [
      "Deploys a full docker-compose.yml stack",
      "Pull from a repo branch or paste inline YAML",
      "Tight container coupling (sidecars, proxies)",
      "Env vars injected as .env file at deploy time",
    ],
    example: "compose_repo_url: https://github.com/org/infra",
  },
  {
    icon: Package,
    iconColor: "text-muted",
    iconBg: "bg-raised",
    title: "Catalog add-on",
    tagline: "One-click pre-configured services",
    bullets: [
      "postgres, redis, mysql, mongodb, rabbitmq…",
      "Pre-configured with sane defaults",
      "Accessible by alias within its environment",
      "Other services connect via hostname:port",
    ],
    example: "network_alias: postgres  →  postgres:5432",
  },
];

function TypeCard({
  icon: Icon,
  iconColor,
  iconBg,
  title,
  tagline,
  bullets,
  example,
}: (typeof SERVICE_TYPES)[number]) {
  return (
    <div className="rounded-lg border border-subtle p-4 space-y-3">
      <div className={`inline-flex items-center justify-center w-10 h-10 rounded-lg ${iconBg}`}>
        <Icon className={`h-5 w-5 ${iconColor}`} />
      </div>
      <div>
        <p className="font-semibold text-sm text-fg">{title}</p>
        <p className="text-xs text-muted mt-0.5">{tagline}</p>
      </div>
      <ul className="space-y-1.5">
        {bullets.map((b) => (
          <li key={b} className="flex items-start gap-2 text-xs text-fg/85">
            <span className="mt-0.5 h-1.5 w-1.5 rounded-full bg-muted shrink-0" />
            {b}
          </li>
        ))}
      </ul>
      <code className="block text-[11px] font-mono text-muted bg-raised px-2 py-1.5 rounded">
        {example}
      </code>
    </div>
  );
}

export function ServiceTypesGuide({ open, onOpenChange }: ServiceTypesGuideProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-5xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Service types</DialogTitle>
          <DialogDescription>
            Understand the four ways to deploy a service and how credentials differ from environment variables.
          </DialogDescription>
        </DialogHeader>

        {/* Section 1 — Service types */}
        <div className="space-y-3">
          <h3 className="text-sm font-semibold text-fg">
            Deployment types
          </h3>
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
            {SERVICE_TYPES.map((t) => (
              <TypeCard key={t.title} {...t} />
            ))}
          </div>
        </div>

        {/* Section 2 — Variables vs Secrets */}
        <div className="space-y-3 pt-2">
          <h3 className="text-sm font-semibold text-fg">
            Variables and secrets
          </h3>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="rounded-lg border border-subtle p-4 space-y-2">
              <p className="font-semibold text-sm text-fg">Environment variables</p>
              <p className="text-xs text-muted">
                Plain text values stored in the database. Visible to any team member with access.
              </p>
              <ul className="space-y-1">
                {["Readable and editable any time", "Good for config: REDIS_URL, PORT, NODE_ENV", "Bulk-editable in the Variables tab", "Applied on next deployment"].map((b) => (
                  <li key={b} className="flex items-start gap-2 text-xs text-fg/85">
                    <span className="mt-0.5 h-1.5 w-1.5 rounded-full bg-muted shrink-0" />
                    {b}
                  </li>
                ))}
              </ul>
              <code className="block text-[11px] font-mono text-muted bg-raised px-2 py-1.5 rounded">
                REDIS_URL=redis://redis:6379{"\n"}NODE_ENV=production
              </code>
            </div>

            <div className="rounded-lg border border-subtle p-4 space-y-2">
              <p className="font-semibold text-sm text-fg">Secrets</p>
              <p className="text-xs text-muted">
                Encrypted at rest. Write-once — values are never returned by the API after creation.
              </p>
              <ul className="space-y-1">
                {["Encrypted at rest (AES-256)", "Write-once: value never readable after save", "Use for credentials, API keys, tokens", "Injected alongside env vars at deploy time"].map((b) => (
                  <li key={b} className="flex items-start gap-2 text-xs text-fg/85">
                    <span className="mt-0.5 h-1.5 w-1.5 rounded-full bg-muted shrink-0" />
                    {b}
                  </li>
                ))}
              </ul>
              <code className="block text-[11px] font-mono text-muted bg-raised px-2 py-1.5 rounded">
                DATABASE_URL=postgres://…{"\n"}SECRET_KEY=••••••••••
              </code>
            </div>
          </div>
        </div>

        {/* Section 3 — Deployment Pipelines */}
        <div className="space-y-3 pt-2">
          <h3 className="text-sm font-semibold text-fg">
            Deployment pipelines
          </h3>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            {[
              {
                label: "Git app",
                color: "text-muted",
                steps: ["Clone repo", "Install deps", "Build image", "Run container", "Health check"],
              },
              {
                label: "Docker image",
                color: "text-muted",
                steps: ["Pull image", "Run container", "Health check"],
              },
              {
                label: "Compose stack",
                color: "text-muted",
                steps: ["Clone / use inline YAML", "docker compose up -d", "Done"],
              },
            ].map(({ label, color, steps }) => (
              <div key={label} className="rounded-lg border border-subtle p-4 space-y-2">
                <p className={`font-semibold text-sm ${color}`}>{label}</p>
                <div className="flex flex-col gap-1">
                  {steps.map((step, i) => (
                    <div key={step} className="flex items-center gap-2">
                      <span className="text-xs font-mono text-muted w-4 text-right shrink-0">{i + 1}</span>
                      <span className="text-xs text-fg/85">{step}</span>
                      {i < steps.length - 1 && (
                        <span className="text-muted/60 ml-auto text-xs">↓</span>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
