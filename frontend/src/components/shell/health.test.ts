import { describe, expect, it } from "vitest";
import { healthLabel, projectHealth, runningIn } from "./health";

const svc = (...statuses: string[]) => statuses.map((runtime_status) => ({ runtime_status }) as never);

describe("projectHealth", () => {
  it("ranks failing over deploying over running over idle", () => {
    expect(projectHealth(svc("running", "deploying", "error")).health).toBe("failed");
    expect(projectHealth(svc("running", "deploying", "stopped")).health).toBe("building");
    expect(projectHealth(svc("running", "unhealthy", "deploying")).health).toBe("building");
    expect(projectHealth(svc("running", "unhealthy")).health).toBe("unhealthy");
    expect(projectHealth(svc("running", "idle", "stopped")).health).toBe("running");
    expect(projectHealth(svc("idle", "stopped")).health).toBe("idle");
  });

  it("is idle with no services or no data", () => {
    expect(projectHealth([]).health).toBe("idle");
    expect(projectHealth(undefined).health).toBe("idle");
  });

  it("counts each state", () => {
    expect(projectHealth(svc("running", "running", "deploying", "unhealthy", "error", "error"))).toEqual({
      health: "failed",
      running: 2,
      deploying: 1,
      unhealthy: 1,
      failing: 2,
    });
  });
});

describe("healthLabel", () => {
  it("says what the dot means", () => {
    expect(healthLabel(projectHealth(svc("error")))).toBe("1 service failing");
    expect(healthLabel(projectHealth(svc("error", "error")))).toBe("2 services failing");
    expect(healthLabel(projectHealth(svc("deploying")))).toBe("deploying");
    expect(healthLabel(projectHealth(svc("unhealthy", "running")))).toBe("1 service unhealthy");
    expect(healthLabel(projectHealth(svc("running")))).toBe("running");
    expect(healthLabel(projectHealth([]))).toBe("nothing running");
  });
});

describe("runningIn", () => {
  it("counts the environment's own running services, not the project services", () => {
    const list = [
      { runtime_status: "running", environment_id: "staging" },
      { runtime_status: "running", environment_id: "staging" },
      { runtime_status: "stopped", environment_id: "staging" },
      { runtime_status: "running", environment_id: "production" },
      { runtime_status: "running", environment_id: "" }, // a project service
    ] as never[];
    expect(runningIn(list, "staging")).toBe(2);
    expect(runningIn(list, "all")).toBe(4);
  });
});
