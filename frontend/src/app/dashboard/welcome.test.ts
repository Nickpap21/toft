import { describe, expect, it } from "vitest";
import type { WorkspaceService } from "@/lib/api/service.service";
import { firstLive } from "./welcome";

const svc = (name: string, runtime_status: string, project_id = "p1") => ({ id: name, name, project_id, runtime_status }) as WorkspaceService;

describe("firstLive", () => {
  it("is the first project service that serves, never a workspace service", () => {
    expect(firstLive([])).toBeUndefined();
    expect(firstLive([svc("db", "error"), svc("web", "stopped")])).toBeUndefined();
    expect(firstLive([svc("grafana", "running", ""), svc("web", "unhealthy"), svc("api", "running")])?.name).toBe("web");
  });
});
