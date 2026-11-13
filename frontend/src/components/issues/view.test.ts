import { describe, expect, it } from "vitest";
import type { IssueCounts, IssueLink } from "@/lib/api/issue.service";
import { countQuery, filtersFrom, mergedClause, moreCount, panelLinks, shortBranch, sum } from "./view";

const sp = (s: string) => new URLSearchParams(s);

describe("the work chip's branch", () => {
  it("shortens to the key's part of the branch", () => {
    expect(shortBranch("fix/sto-118-webhook-retry", "STO-118")).toBe("sto-118");
    expect(shortBranch("feat/STO-112", "STO-112")).toBe("STO-112");
  });
  it("matches with or without zeros (D-83)", () => {
    expect(shortBranch("chore/sto-97-restore-drill", "STO-097")).toBe("sto-97");
    expect(shortBranch("chore/sto-097-restore-drill", "STO-97")).toBe("sto-097");
  });
  it("falls back to the full name when the branch doesn't name the key", () => {
    expect(shortBranch("fix/old-118-webhook", "STO-118")).toBe("fix/old-118-webhook");
    expect(shortBranch("fix/sto-1180-webhook", "STO-118")).toBe("fix/sto-1180-webhook");
    expect(shortBranch("fix/asto-118-webhook", "STO-118")).toBe("fix/asto-118-webhook");
  });
});

describe("the tabs' counts", () => {
  const counts: IssueCounts = { backlog: 8, todo: 5, in_progress: 3, in_review: 3, shipped: 4, cancelled: 0 };

  it("sums every status for All, and only the named ones for Active", () => {
    expect(sum(counts)).toBe(23);
    expect(sum(counts, ["in_progress", "in_review", "todo"])).toBe(11);
    expect(sum(undefined)).toBeUndefined();
  });
  it("doesn't inherit a tab's own view filters", () => {
    expect(countQuery(filtersFrom(sp("view=mine"), {}))).toEqual({});
  });
  it("keeps the cycle chip and the search, so they equal the group headers", () => {
    expect(countQuery(filtersFrom(sp("view=mine&cycle=14&q=login"), {}))).toEqual({ cycle: "14", q: "login" });
  });
  it("takes the URL over the view's, and the view's where the URL is silent", () => {
    const view = { assignee: "me", priority: ["high" as const] };
    expect(filtersFrom(sp("priority=low"), view)).toMatchObject({ assignee: "me", priority: ["low"] });
  });
  it("carries a saved view's type and parent, as the list does", () => {
    expect(countQuery({ type: ["bug"], parent: "STO-1", status: ["todo"], order: "due", group: "type" })).toEqual({ type: ["bug"], parent: "STO-1" });
  });
});

describe("Show N more", () => {
  it("is the status's count minus the rows loaded", () => {
    expect(moreCount(8, 3)).toBe(5);
  });
  it("is never negative, and is 0 before the counts arrive", () => {
    expect(moreCount(3, 3)).toBe(0);
    expect(moreCount(2, 3)).toBe(0);
    expect(moreCount(undefined, 3)).toBe(0);
  });
});

const pull = (repository: string, number: number, head_branch: string, deploys: IssueLink["deploys"] = []): IssueLink => ({
  kind: "pull_request",
  repository,
  repository_id: `r-${repository}`,
  number,
  head_branch,
  state: "merged",
  deploys,
});
const branch = (repository: string, name: string): IssueLink => ({ kind: "branch", repository, repository_id: `r-${repository}`, branch: name });
const deploy = (service: string, state: "live" | "building" | "failed" | "waiting") => ({ service_id: service, service, environment: "production", state });

describe("the merged row in the panel's History", () => {
  it("names the deploy of the pull request with that number in that repository", () => {
    const links = [pull("storefront", 41, "feat/a", [deploy("web", "building")])];
    expect(mergedClause({ number: 41, title: "t", repository: "storefront" }, links, "storefront")).toBe("; deploy of storefront / web started");
  });
  it("never claims another repository's deploy by number alone", () => {
    const links = [pull("billing", 41, "feat/a", [deploy("web", "building")])];
    expect(mergedClause({ number: 41, title: "t", repository: "storefront" }, links, "storefront")).toBe("");
  });
  it("leaves out deploys still waiting", () => {
    const links = [pull("storefront", 41, "feat/a", [deploy("web", "waiting"), deploy("api", "live")])];
    expect(mergedClause({ number: 41, title: "t", repository: "storefront" }, links, "storefront")).toBe("; deploy of storefront / api started");
    expect(mergedClause({ number: 41, title: "t", repository: "storefront" }, [pull("storefront", 41, "feat/a", [deploy("web", "waiting")])], "storefront")).toBe("");
  });
  it("joins several services and has nothing to say without a pull request", () => {
    const links = [pull("storefront", 41, "feat/a", [deploy("web", "building"), deploy("api", "failed")])];
    expect(mergedClause({ number: 41, title: "t", repository: "storefront" }, links, "storefront")).toBe("; deploy of storefront / web and storefront / api started");
    expect(mergedClause(undefined, links, "storefront")).toBe("");
  });
});

describe("the panel's Linked work", () => {
  it("leaves a branch out once its pull request is linked", () => {
    const pr = pull("storefront", 41, "feat/a");
    expect(panelLinks([branch("storefront", "feat/a"), pr])).toEqual([pr]);
  });
  it("keeps a branch with no pull request yet", () => {
    const b = branch("storefront", "feat/b");
    const pr = pull("storefront", 41, "feat/a");
    expect(panelLinks([b, pr])).toEqual([b, pr]);
    expect(panelLinks([b])).toEqual([b]);
  });
  it("doesn't let another repository's pull request hide a branch of the same name", () => {
    const b = branch("storefront", "feat/a");
    const pr = pull("billing", 41, "feat/a");
    expect(panelLinks([b, pr])).toEqual([b, pr]);
  });
});
