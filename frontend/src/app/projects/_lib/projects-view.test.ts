import { describe, expect, it } from "vitest";
import { addonName, ago, envState, imageRef, inStatus, kindOf, matchesText, projectsTable, reachFrom, sourceOf } from "./projects-view";
import type { Service, WorkspaceService } from "@/lib/api/service.service";
import type { WorkspaceEnvironment } from "@/lib/api/environment.service";

const svc = (p: Partial<WorkspaceService>): WorkspaceService => ({
  id: "s",
  name: "s",
  scope: "environment",
  project_id: "p1",
  environment_id: "e1",
  server_id: "",
  port: 3000,
  runtime_status: "running",
  source_type: "git",
  addon_slug: "",
  docker_image: "",
  git_repo_url: "",
  git_branch: "main",
  environment_name: "production",
  ...p,
});

describe("projectsTable", () => {
  const envs: WorkspaceEnvironment[] = [
    { id: "e1", project_id: "p1", name: "production", color: "gray", protected: true, preview: false },
    { id: "e2", project_id: "p1", name: "staging", color: "amber", protected: false, preview: false },
    { id: "e3", project_id: "p2", name: "production", color: "gray", protected: true, preview: false },
    { id: "e4", project_id: "p1", name: "pr-12", color: "gray", protected: false, preview: true },
    { id: "e5", project_id: "p1", name: "docs-pr-3", color: "gray", protected: false, preview: true },
    { id: "e6", project_id: "p3", name: "production", color: "gray", protected: true, preview: false },
  ];
  const servers = [
    { id: "srv-1", name: "fsn1-prod-01" },
    { id: "srv-2", name: "hel1-prod-03" },
  ];

  it("names where each environment runs, production's server first (board ReworkProjects)", () => {
    const [row, empty] = projectsTable(
      [
        { id: "p1", name: "storefront", description: "Shop" },
        { id: "p2", name: "handbook", description: "", doc_pages: 18 },
      ],
      [
        svc({ id: "a", server_id: "srv-1" }),
        svc({ id: "b", server_id: "srv-1", runtime_status: "deploying" }),
        svc({ id: "c", server_id: "srv-2", environment_id: "e2" }),
        svc({ id: "d", server_id: "srv-2", environment_id: "e4" }),
      ],
      envs,
      [],
      servers,
    );
    expect(row.environments.map((e) => e.name)).toEqual(["production", "staging"]);
    expect(row.previews).toEqual(["!12", "docs!3"]);
    expect(row.where).toBe("fsn1-prod-01 · staging and previews on hel1-prod-03");
    expect(row.services).toBe(4);
    // Production's own services, not staging's or the previews'.
    expect(row.production.servicesText).toBe("1 running · 1 deploying");
    expect(empty.where).toBe("no services");
    expect(empty.production.servicesText).toBe("Docs only");
  });

  it("puts what needs the person first: failing, deploying, then the rest by name", () => {
    const rows = projectsTable(
      [
        { id: "p1", name: "storefront", description: "" },
        { id: "p2", name: "handbook", description: "" },
        { id: "p3", name: "billing-api", description: "" },
      ],
      [
        svc({ id: "a", project_id: "p1", runtime_status: "deploying" }),
        svc({ id: "b", project_id: "p3", environment_id: "e6", runtime_status: "error" }),
        svc({ id: "c", project_id: "p3", environment_id: "e6" }),
      ],
      envs,
      [],
      servers,
    );
    expect(rows.map((r) => r.name)).toEqual(["billing-api", "storefront", "handbook"]);
    expect(rows[0].production.servicesText).toBe("1 failed · 1 running");
    expect(rows[2].production.servicesText).toBe("No services");
  });

  it("says when production has nothing but another environment does", () => {
    const [row] = projectsTable([{ id: "p1", name: "storefront", description: "" }], [svc({ environment_id: "e2" })], envs, [], servers);
    expect(row.production).toEqual({ bar: [], servicesText: "Nothing in production", servicesTone: "neutral" });
    expect(row.where).toBe("staging on panel host");
  });
});

describe("sourceOf", () => {
  it("names the source by type", () => {
    expect(
      sourceOf({
        source_type: "git",
        git_branch: "main",
        base_directory: "/services/web/",
      }),
    ).toBe("main · services/web");
    expect(sourceOf({ source_type: "git" })).toBe("main");
    expect(
      sourceOf({ source_type: "docker-image", docker_image: "postgres:16.4" }),
    ).toBe("postgres:16.4");
    expect(sourceOf({ source_type: "docker-compose" })).toBe("compose");
  });
});

describe("ago", () => {
  it("is relative within a day, then a date", () => {
    const now = Date.parse("2026-10-15T12:00:00Z");
    expect(ago("2026-10-15T11:58:00Z", now)).toBe("2m ago");
    expect(ago("2026-10-15T09:00:00Z", now)).toBe("3h ago");
    expect(ago("2026-10-12T12:00:00Z", now)).toMatch(/^2026-10-12 \d\d:\d\d$/);
  });
});

describe("reachFrom", () => {
  it("names the environments with services on the same server", () => {
    const envs = [
      { id: "e1", project_id: "p1", name: "production", color: "gray" as const, protected: false, preview: false },
      { id: "e2", project_id: "p1", name: "staging", color: "gray" as const, protected: false, preview: false },
      { id: "e3", project_id: "p2", name: "production", color: "gray" as const, protected: false, preview: false },
      { id: "e4", project_id: "p2", name: "staging", color: "gray" as const, protected: false, preview: false },
    ];
    const projects = [
      { id: "p1", name: "storefront" },
      { id: "p2", name: "billing-api" },
    ];
    const services = [
      svc({ environment_id: "e1", server_id: "srv-1" }),
      svc({ environment_id: "e3", project_id: "p2", server_id: "srv-1" }),
      svc({ environment_id: "e4", project_id: "p2", server_id: "srv-1" }),
      svc({ environment_id: "e2", server_id: "srv-2" }),
      svc({ scope: "project", environment_id: "", server_id: "srv-1" }),
    ];
    expect(reachFrom("srv-1", services, envs, projects)).toBe(
      "billing-api all environments; storefront production",
    );
    expect(reachFrom("srv-2", services, envs, projects)).toBe(
      "storefront staging",
    );
    expect(reachFrom("srv-3", services, envs, projects)).toBe("");
  });
});

describe("kindOf", () => {
  const k = (p: Partial<Pick<Service, "addon_slug" | "source_type" | "type" | "kind">>) =>
    kindOf({ source_type: "docker-image", type: "web", kind: "app", ...p });
  it("says Database and Tool by what the service is, catalog or image", () => {
    expect(k({ kind: "database" })).toBe("Database");
    expect(k({ kind: "cache" })).toBe("Database");
    expect(k({ kind: "tool" })).toBe("Tool");
    expect(k({ kind: "database", addon_slug: "postgres" })).toBe("Database");
  });
  it("keeps the rest as before", () => {
    expect(k({})).toBe("Web");
    expect(k({ type: "worker" })).toBe("Worker");
    expect(k({ addon_slug: "uptime-kuma" })).toBe("Uptime Kuma");
    expect(k({ source_type: "docker-compose" })).toBe("Compose");
  });
  it("names Mailpit, and MailHog services made before it replaced MailHog", () => {
    expect(addonName("mailpit")).toBe("Mailpit");
    expect(addonName("mailhog")).toBe("MailHog");
  });
});

describe("imageRef", () => {
  it("drops the digest of a tagged image", () => {
    expect(imageRef("mysql:8.0@sha256:0123456789abcdef")).toBe("mysql:8.0");
    expect(imageRef("localhost:5000/team/app:v2@sha256:0123456789abcdef")).toBe("localhost:5000/team/app:v2");
  });
  it("keeps a short digest when there is no tag", () => {
    expect(imageRef("mailhog/mailhog@sha256:0123456789abcdef")).toBe("mailhog/mailhog@sha256:0123456");
    expect(imageRef("localhost:5000/app@sha256:0123456789abcdef")).toBe("localhost:5000/app@sha256:0123456");
  });
  it("leaves an unpinned image alone", () => {
    expect(imageRef("redis:7-alpine")).toBe("redis:7-alpine");
  });
});

describe("envState", () => {
  const st = (...xs: Service["runtime_status"][]) => envState(xs.map((runtime_status) => ({ runtime_status })));
  it("says the worst first", () => {
    expect(st()).toEqual({ tone: "neutral", label: "No services" });
    expect(st("running", "deploying", "error")).toEqual({ tone: "building", label: "1 deploying" });
    expect(st("running", "error", "unhealthy")).toEqual({ tone: "failed", label: "2 failing" });
    expect(st("running", "stopped")).toEqual({ tone: "running", label: "Running" });
  });
  it("tells nothing deployed yet from stopped", () => {
    expect(st("idle", "idle")).toEqual({ tone: "neutral", label: "Idle" });
    expect(st("idle", "stopped")).toEqual({ tone: "neutral", label: "Stopped" });
  });
});

describe("the Services filters", () => {
  const svc = (p: Partial<Service>) => ({ name: "web", runtime_status: "running", source_type: "git", git_branch: "main", docker_image: "", ...p }) as Service;
  it("matches name, image, source and domain, ignoring case", () => {
    expect(matchesText(svc({}), [], " ")).toBe(true);
    expect(matchesText(svc({ name: "Search" }), [], "sea")).toBe(true);
    expect(matchesText(svc({ source_type: "docker-image", docker_image: "getmeili/meilisearch:v1.9@sha256:abc" }), [], "meili")).toBe(true);
    expect(matchesText(svc({ base_directory: "apps/web" }), [], "apps/")).toBe(true);
    expect(matchesText(svc({}), ["shop.example.com"], "SHOP.")).toBe(true);
    expect(matchesText(svc({}), ["shop.example.com"], "api")).toBe(false);
  });
  it("Failing is error or unhealthy; Stopped is stopped or never deployed", () => {
    const st = (runtime_status: Service["runtime_status"]) => ({ runtime_status });
    expect(["error", "unhealthy", "running", "stopped"].map((r) => inStatus(st(r as Service["runtime_status"]), "failing"))).toEqual([true, true, false, false]);
    expect(["stopped", "idle", "running", "deploying"].map((r) => inStatus(st(r as Service["runtime_status"]), "stopped"))).toEqual([true, true, false, false]);
    expect(inStatus(st("error"), "")).toBe(true);
  });
});
