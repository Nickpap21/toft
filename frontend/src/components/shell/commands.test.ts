import { describe, expect, it } from "vitest";
import type { Project } from "@/lib/api/project.service";
import type { CommandSources } from "./commands";
import type { Server } from "@/lib/api/server.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { buildCommands, deployFrom, filterCommands, GROUP_LIMIT } from "./commands";

const project = (id: string, name: string) => ({ id, name, description: "" }) as Project;
const service = (id: string, name: string, project_id: string, extra: Partial<WorkspaceService> = {}): WorkspaceService => ({
  id, name, project_id, environment_id: "", server_id: "", port: 3000, scope: "environment", runtime_status: "running", source_type: "git",
  addon_slug: "", docker_image: "", git_repo_url: "", git_branch: "main", environment_name: "production", ...extra,
});
const server = (id: string, name: string, ip_address: string) => ({ id, name, ip_address }) as Server;

const base = {
  projects: [project("p1", "storefront"), project("p2", "billing-api")],
  services: [
    service("s1", "web", "p1", { docker_image: "nginx:1.27" }),
    service("s2", "api", "p2"),
    service("s3", "postgres", "p1", { scope: "org", addon_slug: "postgres" }),
  ],
  servers: [server("v1", "fsn1-prod-01", "203.0.113.10")],
  orgs: [
    { org_id: "o1", name: "Founders", role: "owner" as const },
    { org_id: "o2", name: "Client A", role: "developer" as const },
  ],
  currentOrgId: "o1",
  pref: "system:graphite-paper" as const,
  theme: "graphite" as const,
} satisfies CommandSources;

describe("buildCommands", () => {
  it("lists projects, services and servers first when nothing is typed, and Go to and Theme last", () => {
    const groups = filterCommands(buildCommands(base), "").map((g) => g.group);
    expect(groups).toEqual(["Projects", "Services", "Servers", "Actions", "Go to", "Theme"]);
  });

  it("links projects, services and servers", () => {
    const cmds = buildCommands(base);
    expect(cmds.find((c) => c.id === "project:p1")?.href).toBe("/projects/p1");
    expect(cmds.find((c) => c.id === "service:s2")?.href).toBe("/projects/p2/services/s2");
    expect(cmds.find((c) => c.id === "server:v1")?.href).toBe("/servers/v1");
  });

  it("lets a service be found by its project, image or add-on", () => {
    const find = (q: string) => filterCommands(buildCommands(base), q).find((g) => g.group === "Services")?.items.map((i) => i.id);
    expect(find("storefront")).toContain("service:s1");
    expect(find("nginx")).toEqual(["service:s1"]);
    expect(find("postgres")).toEqual(["service:s3"]);
  });

  it("finds a server by IP", () => {
    const servers = filterCommands(buildCommands(base), "203.0.113").find((g) => g.group === "Servers");
    expect(servers?.items.map((i) => i.id)).toEqual(["server:v1"]);
  });

  it("offers only the other workspaces", () => {
    const labels = buildCommands(base).map((c) => c.label);
    expect(labels).toContain("Switch to Client A");
    expect(labels).not.toContain("Switch to Founders");
  });

  it("lists the eight theme choices and marks the current one", () => {
    const themes = buildCommands(base).filter((c) => c.id.startsWith("theme:system") || /^theme:[a-z]+$/.test(c.id));
    expect(themes.filter((c) => c.id !== "theme:other")).toHaveLength(8);
    expect(themes.find((c) => c.hint === "Current")?.id).toBe("theme:system:graphite-paper");
    expect(buildCommands(base).find((c) => c.id === "theme:other")?.label).toBe("Use Paper");
  });

  it("offers the deploy policy report to operators only", () => {
    expect(buildCommands(base).some((c) => c.id === "go:deploy-policy")).toBe(false);
    expect(buildCommands({ ...base, isOperator: true }).some((c) => c.id === "go:deploy-policy")).toBe(true);
  });

  it("offers Inbox and My issues once built (D-88)", () => {
    const labels = buildCommands(base).map((c) => c.label);
    for (const shown of ["Go to Inbox", "Go to My issues"]) {
      expect(labels).toContain(shown);
    }
  });
});

describe("filterCommands", () => {
  const many = {
    ...base,
    projects: Array.from({ length: 20 }, (_, i) => project(`p${i}`, `project-${String(i).padStart(2, "0")}`)),
  };

  it(`caps projects, services and servers at ${GROUP_LIMIT} each`, () => {
    const projects = filterCommands(buildCommands(many), "").find((g) => g.group === "Projects");
    expect(projects?.items).toHaveLength(GROUP_LIMIT);
  });

  it("caps after filtering, so search reaches every project", () => {
    const projects = filterCommands(buildCommands(many), "project-19").find((g) => g.group === "Projects");
    expect(projects?.items[0].id).toBe("project:p19");
  });

  it("does not cap commands", () => {
    const go = filterCommands(buildCommands(many), "").find((g) => g.group === "Go to");
    expect(go!.items.length).toBeGreaterThan(GROUP_LIMIT);
    const actions = filterCommands(buildCommands(many), "").find((g) => g.group === "Actions");
    expect(actions!.items.map((i) => i.label)).toContain("Sign out");
  });

  it("returns nothing for a search that matches nothing", () => {
    expect(filterCommands(buildCommands(base), "zzzzqqq")).toEqual([]);
  });
});

describe("ranking (review, ⌘K)", () => {
  const northwind = {
    ...base,
    projects: [project("p1", "storefront"), project("p3", "initech-staging")],
    orgs: [...base.orgs, { org_id: "o3", name: "Northwind", role: "owner" as const }],
  };
  const first = (q: string) => filterCommands(buildCommands(northwind), q)[0]?.items[0]?.id;

  it("puts a project the query names before any action", () => {
    expect(first("initech")).toBe("project:p3");
  });

  it("keeps actions and themes that only match a keyword loosely out", () => {
    const groups = filterCommands(buildCommands(northwind), "initech").map((g) => g.group);
    expect(groups).toEqual(["Projects"]);
    const labels = filterCommands(buildCommands(northwind), "deploy").flatMap((g) => g.items.map((i) => i.label));
    expect(labels.some((l) => l.startsWith("Theme"))).toBe(false);
  });

  it("never pre-selects a state change the query only half names", () => {
    // "web" is a service; "Switch to …" matches neither.
    expect(first("web")).toBe("service:s1");
    // "s" starts "Sign out" and "storefront": the project goes first.
    expect(first("s")).toBe("project:p1");
    expect(first("sign")).toBe("sign-out");
  });

  it("searches past a shared server, which has no address", () => {
    const shared = { id: "v2", name: "shared-01", shared: true, shared_by: "Client A" } as Server;
    const cmds = buildCommands({ ...northwind, servers: [...base.servers, shared] });
    expect(filterCommands(cmds, "storefront")[0]?.items[0]?.id).toBe("project:p1");
  });

  it("still finds a workspace by name", () => {
    const actions = filterCommands(buildCommands(northwind), "northwind").find((g) => g.group === "Actions");
    expect(actions?.items.map((i) => i.id)).toEqual(["org:o3"]);
  });
});

describe("Settings commands", () => {
  it("go to every tab, and offer the owner-only and operator-only ones to them", () => {
    const ids = buildCommands({}).map((c) => c.id);
    for (const id of ["go:members", "go:api-keys", "go:alerts", "go:storage", "go:backups", "go:account", "go:appearance", "new-api-key"]) {
      expect(ids).toContain(id);
    }
    expect(ids).not.toContain("invite-member");
    expect(ids).not.toContain("go:deploy-policy");
    expect(buildCommands({ role: "owner" as const }).map((c) => c.id)).toContain("invite-member");
  });

  it("are found by what people call them", () => {
    const top = (q: string) => filterCommands(buildCommands({}), q)[0]?.items[0]?.id;
    expect(top("token")).toBe("go:api-keys");
    expect(top("webhook")).toBe("go:alerts");
    expect(top("members")).toBe("go:members");
  });
});

describe("create commands", () => {
  it("offers New project and New service in each project to owners and developers only", () => {
    const projects = [project("p1", "storefront")];
    const labels = (role: string) => buildCommands({ projects, role }).map((c) => c.label);
    expect(labels("developer")).toEqual(expect.arrayContaining(["New project", "New service in storefront", "New workspace service"]));
    expect(labels("viewer")).not.toContain("New project");
    expect(labels("viewer")).not.toContain("New service in storefront");
    const empty = filterCommands(buildCommands({ projects, role: "owner" as const }), "").flatMap((g) => g.items.map((i) => i.label));
    expect(empty).not.toContain("New service in storefront");
    const typed = filterCommands(buildCommands({ projects, role: "owner" as const }), "new serv").flatMap((g) => g.items.map((i) => i.label));
    expect(typed).toContain("New service in storefront");
  });
});

describe("service actions (D-50)", () => {
  const owner = { ...base, role: "owner" as const };
  const ids = (q: string, scope?: string, src = owner) => filterCommands(buildCommands(src), q, scope).flatMap((g) => g.items.map((i) => i.id));

  it("deploys from the branch or the image, and names the project and service", () => {
    const deploy = buildCommands(owner).find((c) => c.id === "deploy:s1");
    expect(deploy?.label).toBe("Deploy");
    expect(deploy?.detail).toBe("storefront / web from main");
    expect(deploy?.hint).toBe("production");
    expect(deploy?.action).toEqual({ type: "deploy", serviceId: "s1", projectId: "p1", name: "storefront / web" });
    expect(deployFrom({ source_type: "git", git_branch: "develop", docker_image: "" })).toBe("develop");
    expect(deployFrom({ source_type: "docker-image", git_branch: "", docker_image: "ghcr.io/acme/api:1.4" })).toBe("api:1.4");
    expect(deployFrom({ source_type: "docker-compose", git_branch: "main", docker_image: "" })).toBe("");
  });

  it("never deploys a protected environment's service at once: it opens the service's confirm", () => {
    const src = {
      ...owner,
      services: [service("s1", "web", "p1", { environment_id: "e1" }), service("s2", "api", "p2", { environment_id: "e2", environment_name: "staging" })],
      environments: [
        { id: "e1", project_id: "p1", name: "production", color: "gray" as const, protected: true, preview: false },
        { id: "e2", project_id: "p2", name: "staging", color: "teal" as const, protected: false, preview: false },
      ],
    };
    const cmds = buildCommands(src);
    const prod = cmds.find((c) => c.id === "deploy:s1");
    expect(prod?.action).toBeUndefined();
    expect(prod?.href).toBe("/projects/p1/services/s1?confirm=deploy");
    expect(prod?.detail).toBe("storefront / web from main…");
    expect(cmds.find((c) => c.id === "deploy:s2")?.action).toEqual({ type: "deploy", serviceId: "s2", projectId: "p2", name: "billing-api / api" });
  });

  it("opens the service's confirmation for the rest", () => {
    const cmds = buildCommands(owner);
    expect(cmds.find((c) => c.id === "rollback:s1")?.href).toBe("/projects/p1/services/s1?tab=deployments&rollback=1");
    expect(cmds.find((c) => c.id === "restart:s1")?.href).toBe("/projects/p1/services/s1?confirm=restart");
    expect(cmds.find((c) => c.id === "stop:s1")?.href).toBe("/projects/p1/services/s1?confirm=stop");
  });

  it("offers none of them to viewers, nor a deploy while one runs", () => {
    expect(buildCommands({ ...base, role: "viewer" as const }).some((c) => c.id.startsWith("deploy:"))).toBe(false);
    const deploying = { ...owner, services: [service("s9", "worker", "p1", { runtime_status: "deploying" })] };
    expect(buildCommands(deploying).some((c) => c.id === "deploy:s9")).toBe(false);
    const idle = { ...owner, services: [service("s8", "cron", "p1", { runtime_status: "idle" })] };
    expect(buildCommands(idle).map((c) => c.id)).toEqual(expect.arrayContaining(["deploy:s8"]));
    expect(buildCommands(idle).some((c) => c.id === "rollback:s8" || c.id === "stop:s8")).toBe(false);
  });

  it("puts deploy rows first for 'deploy', and the service first for its name", () => {
    expect(ids("deploy")[0]).toMatch(/^deploy:/);
    expect(ids("web")[0]).toBe("service:s1");
    expect(ids("deploy web")[0]).toBe("deploy:s1");
  });

  it("hides deploy rows until something is typed, unless scoped to their project", () => {
    expect(ids("")).not.toContain("deploy:s1");
    const scoped = ids("", "p1");
    expect(scoped.slice(0, 2)).toEqual(["deploy:s1", "deploy:s3"]);
    expect(scoped).toContain("service:s1");
    expect(scoped).not.toContain("deploy:s2");
    expect(scoped).not.toContain("rollback:s1");
    expect(scoped).not.toContain("go:home");
  });

  it("keeps a scope to its project, with the commands once something is typed", () => {
    const scoped = ids("go to", "p1");
    expect(scoped).toContain("go:home");
    expect(scoped).not.toContain("project:p2");
    expect(ids("api", "p1")).not.toContain("service:s2");
    const withShared = { ...owner, services: [...base.services, service("s4", "grafana", "", { scope: "org" })] };
    expect(ids("deploy", "p1", withShared)).not.toContain("deploy:s4");
    expect(ids("deploy", undefined, withShared)).toContain("deploy:s4");
  });

  it("lists only commands after >", () => {
    const groups = filterCommands(buildCommands(owner), ">").map((g) => g.group);
    expect(groups).toEqual(["Actions", "Go to", "Theme"]);
    expect(ids(">storefront")).not.toContain("project:p1");
  });

  it("offers no sidebar toggle on a phone", () => {
    expect(buildCommands({ ...base, touch: true }).some((c) => c.id === "sidebar")).toBe(false);
  });
});
