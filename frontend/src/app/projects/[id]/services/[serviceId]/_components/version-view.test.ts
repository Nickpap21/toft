import { describe, expect, it } from "vitest";
import type { TagEntry } from "@/lib/api/version.service";
import {
  branchesFirst,
  byPushTime,
  deployBlocked,
  shortHash,
  splitImage,
} from "./version-view";

const tag = (name: string, pushed_at: string): TagEntry => ({
  name,
  pushed_at,
  digest: "",
  is_latest: false,
});

describe("byPushTime", () => {
  it("puts the newest push first and tags without a time last, by name", () => {
    const tags = [
      tag("1.7.11", "2015-04-07T07:44:35Z"),
      tag("zeta", "0001-01-01T00:00:00Z"),
      tag("latest", "2026-10-06T04:53:45Z"),
      tag("alpha", ""),
      tag("1.30.5", "2026-10-08T13:51:01Z"),
    ];
    expect(byPushTime(tags).map((t) => t.name)).toEqual([
      "1.30.5",
      "latest",
      "1.7.11",
      "alpha",
      "zeta",
    ]);
  });
});

describe("shortHash", () => {
  it("drops sha256: and keeps 12 characters", () => {
    expect(shortHash("sha256:f9ea18bfa4fad859e1ed38259d711da7")).toBe(
      "f9ea18bfa4fa",
    );
    expect(shortHash("3f2a9c1d8e7b6a5f4e3d2c1b")).toBe("3f2a9c1d8e7b");
  });
});

describe("splitImage", () => {
  it("splits the tag after the last slash, not a registry port", () => {
    expect(splitImage("ghcr.io:5000/acme/api:v1")).toEqual({
      base: "ghcr.io:5000/acme/api",
      tag: "v1",
    });
    expect(splitImage("ghcr.io:5000/acme/api")).toEqual({
      base: "ghcr.io:5000/acme/api",
      tag: null,
    });
    expect(splitImage("nginx:1.27-alpine")).toEqual({
      base: "nginx",
      tag: "1.27-alpine",
    });
  });
});

describe("deployBlocked", () => {
  const base = {
    canDeploy: true,
    busy: false,
    picked: "1.28",
    running: "1.27",
    label: "nginx:1.28",
  };
  it("is empty when the picked version can be deployed", () => {
    expect(deployBlocked(base)).toBe("");
  });
  it("says why, in the order that matters", () => {
    expect(deployBlocked({ ...base, canDeploy: false })).toBe(
      "Only owners and developers deploy.",
    );
    expect(deployBlocked({ ...base, busy: true })).toBe("A deploy is running.");
    expect(deployBlocked({ ...base, picked: "" })).toBe("Pick a version.");
    expect(
      deployBlocked({ ...base, picked: "1.27", label: "nginx:1.27" }),
    ).toBe("nginx:1.27 is running.");
  });
});

describe("branchesFirst", () => {
  const commit = (sha: string, timestamp: string) => ({
    sha,
    message: "",
    author: "",
    timestamp,
  });
  it("puts the service's branch first and the rest by newest commit", () => {
    const got = branchesFirst(
      [
        { name: "a-old", latest_commit: commit("1", "2026-01-01T00:00:00Z") },
        { name: "main", latest_commit: commit("2", "2025-01-01T00:00:00Z") },
        { name: "b-new", latest_commit: commit("3", "2026-10-01T00:00:00Z") },
      ],
      "main",
    );
    expect(got.map((b) => b.name)).toEqual(["main", "b-new", "a-old"]);
  });
  it("adds the service's branch when the host's page left it out", () => {
    const latest = commit("1d1012f32e22", "2026-10-14T00:00:00Z");
    const got = branchesFirst(
      [{ name: "zz", latest_commit: commit("9", "2026-01-01T00:00:00Z") }],
      "main",
      latest,
    );
    expect(got[0]).toEqual({ name: "main", latest_commit: latest });
    expect(got).toHaveLength(2);
  });
});
