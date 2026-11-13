import { describe, expect, it } from "vitest";
import { abilityDefault, consentInput, isLocal, listNames, possessive, sentence, until } from "./view";

describe("consent view", () => {
  it("knows a program on this computer", () => {
    expect(isLocal("localhost:53112")).toBe(true);
    expect(isLocal("127.0.0.1:8000")).toBe(true);
    expect(isLocal("[::1]:9000")).toBe(true);
    expect(isLocal("localhost.evil.example")).toBe(false);
    expect(isLocal("app.example.com")).toBe(false);
  });

  it("pre-checks abilities from the request and the role", () => {
    expect(abilityDefault("deploy", [], "owner")).toBe(true);
    expect(abilityDefault("code", [], "developer")).toBe(true);
    expect(abilityDefault("variables", [], "owner")).toBe(false);
    expect(abilityDefault("deploy", ["read"], "owner")).toBe(false);
    expect(abilityDefault("runbooks", ["read", "runbooks"], "developer")).toBe(true);
    expect(abilityDefault("deploy", ["deploy"], "viewer")).toBe(false);
    expect(abilityDefault("deploy", [], undefined)).toBe(false);
  });

  it("builds the answer with the request's fields", () => {
    const q = new URLSearchParams("response_type=code&client_id=c&redirect_uri=http%3A%2F%2Flocalhost%3A1%2Fcb&code_challenge=x&code_challenge_method=S256&state=s");
    expect(consentInput(q, { allow: true, orgId: "o", allProjects: false, projectIds: ["p1"], scopes: ["deploy", "code"] })).toEqual({
      response_type: "code",
      client_id: "c",
      redirect_uri: "http://localhost:1/cb",
      code_challenge: "x",
      code_challenge_method: "S256",
      state: "s",
      scope: "",
      resource: "",
      allow: true,
      org_id: "o",
      all_projects: false,
      project_ids: ["p1"],
      scopes: ["read", "deploy", "code"],
    });
    const all = consentInput(q, { allow: true, orgId: "o", allProjects: true, projectIds: ["p1"], scopes: [] });
    expect(all.project_ids).toEqual([]);
    expect(all.scopes).toEqual(["read"]);
  });

  it("formats names, dates and messages", () => {
    expect(possessive("Founders")).toBe("Founders’");
    expect(possessive("acme")).toBe("acme’s");
    expect(listNames(["a"])).toBe("a");
    expect(listNames(["a", "b", "c"])).toBe("a, b and c");
    expect(until(new Date("2026-10-21T12:00:00Z"))).toBe("2027-01-19");
    expect(sentence("the link has no client_id")).toBe("The link has no client_id.");
  });
});
