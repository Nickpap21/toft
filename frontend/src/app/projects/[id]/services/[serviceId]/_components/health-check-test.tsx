"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { AlertTriangle, Check, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/lib/api/client";
import { serviceService, type HealthTestResult } from "@/lib/api/service.service";

/** Ports whose services do not speak HTTP: an HTTP health check on them
 *  cannot pass. */
const NON_HTTP_PORTS: Record<number, string> = {
  3306: "MySQL",
  5432: "Postgres",
  6379: "Redis",
  27017: "MongoDB",
  5672: "RabbitMQ",
  11211: "Memcached",
  1025: "SMTP",
  25: "SMTP",
};

/** The warning for a port that does not speak HTTP, or null. */
export function nonHttpPortWarning(port: number): string | null {
  const what = NON_HTTP_PORTS[port];
  return what
    ? `Port ${port} is ${what}'s, which does not answer HTTP, so this check cannot pass. Leave retries at 0: a crashed container still shows as down.`
    : null;
}

/** "Test" for Settings → Health Check: asks the running container for the
 *  form's path, once, as the deploy will, before the check is saved. */
export function HealthCheckTest({ serviceId, path, port, timeout, running }: {
  serviceId: string;
  path: string;
  port: number;
  timeout: number;
  running: boolean;
}) {
  // A result stands for the values it was tested with: editing the path,
  // port or timeout hides it.
  const tested = `${path.trim()}|${port}|${timeout}`;
  const [shown, setShown] = useState<{ key: string; ok: boolean; text: string } | null>(null);
  const result = shown?.key === tested ? shown : null;
  const test = useMutation({
    mutationFn: () => serviceService.testHealthCheck(serviceId, { path: path.trim(), port, timeout }),
    onSuccess: (r: HealthTestResult) =>
      setShown({
        key: tested,
        ...(r.status === 0
          ? { ok: false, text: r.detail || "No answer" }
          : r.status < 400
            ? { ok: true, text: `HTTP ${r.status} in ${r.ms} ms: the check passes` }
            : { ok: false, text: `HTTP ${r.status} in ${r.ms} ms: the check fails (400 or more)` }),
      }),
    onError: (err: unknown) =>
      setShown({ key: tested, ok: false, text: err instanceof ApiError || err instanceof Error ? err.message : "Could not test" }),
  });
  const warning = nonHttpPortWarning(port);

  return (
    <div className="flex flex-col gap-2">
      {warning && (
        <p className="flex items-start gap-1.5 text-xs text-status-pending-fg">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 flex-none" aria-hidden="true" />
          {warning}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!running || !path.trim().startsWith("/") || test.isPending}
          onClick={() => { setShown(null); test.mutate(); }}
        >
          {test.isPending ? "Testing…" : "Test"}
        </Button>
        {!running && <span className="text-xs text-muted">Testing needs the service running.</span>}
        {result && (
          <span
            role="status"
            className={`inline-flex items-center gap-1.5 text-xs ${result.ok ? "text-status-running-fg" : "text-status-failed-fg"}`}
          >
            {result.ok ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <X className="h-3.5 w-3.5" aria-hidden="true" />}
            {result.text}
          </span>
        )}
      </div>
    </div>
  );
}
