import { describe, expect, it } from "vitest";
import { chipText, dataNote, envHints, loosenAsk, ownLede } from "./project-card-view";

const ws = [
  { action: "deploy" as const, env_class: "preview" as const, decision: "allow" as const },
  { action: "deploy" as const, env_class: "other" as const, decision: "allow" as const },
  { action: "deploy" as const, env_class: "protected" as const, decision: "ask" as const },
  { action: "code" as const, env_class: "any" as const, decision: "allow" as const },
];
const envs = [
  { id: "e1", name: "staging", protected: false },
  { id: "e2", name: "production", protected: true },
  { id: "e3", name: "pr-1", preview: { pull_number: 1, repository_id: "r1" }, protected: false },
];

describe("a project's Agents card (board ProjectAgents)", () => {
  it("names the classes' environments", () => {
    expect(envHints(envs)).toEqual({ preview: "pr-1", other: "staging", protected: "production" });
    expect(envHints([]).other).toBe("none yet");
  });

  it("says whether the project follows the workspace, and how it differs", () => {
    expect(chipText([], "Northwind")).toBe("Follows Northwind");
    const own = [
      { action: "deploy" as const, env_class: "preview" as const, decision: "never" as const },
      { action: "deploy" as const, env_class: "other" as const, decision: "never" as const },
    ];
    expect(chipText(own, "Northwind")).toBe("2 rules of its own");
    expect(ownLede(own, ws, "Northwind")).toBe(
      "Deploy or roll back is Never in Previews and Other environments here, where Northwind says Allow. Everything else follows Northwind.",
    );
    expect(ownLede([{ action: "code", env_class: "any", decision: "ask" }], ws, "Northwind")).toBe(
      "Branches and pull requests is Ask a person here, where Northwind says Allow. Everything else follows Northwind.",
    );
  });

  it("names the project's databases, and where they count as protected", () => {
    const svc = (name: string, environment_id: string, reason: "database" | "app", docker_image = "postgres:16") =>
      ({ name, environment_id, docker_image, addon_slug: "", isolation_advice: { level: "standard" as const, reason } });
    expect(dataNote([svc("pg", "e1", "database"), svc("web", "e1", "app"), svc("pg", "e3", "database")], envs)).toBe(
      "pg is a database: its deploys, variables, secrets and runbook steps follow the Protected column, in staging too. A preview’s copy follows Previews.",
    );
    expect(dataNote([svc("redis", "e2", "database", "redis:7")], envs)).toMatch(/^redis is a cache: its .* Protected column\. A preview/);
    expect(dataNote([], envs)).toMatch(/^Databases and caches count as Protected/);
  });

  it("says what a loosening does before the password", () => {
    expect(loosenAsk("toft-actions-check", "Northwind", "deploy", "other", "never", "", "allow", "staging")).toEqual({
      title: "Let agents deploy or roll back in toft-actions-check’s other environments?",
      why: "Deploy or roll back, Other environments: Never → As Northwind (Allow). Agents connected to this project deploy or roll back in staging without asking. Confirm with your password.",
      button: "Confirm and loosen",
    });
    expect(loosenAsk("shop", "Northwind", "code", "any", "never", "ask", "allow", "").title).toBe("Let agents ask a person to push and open pull requests in shop?");
  });
});
