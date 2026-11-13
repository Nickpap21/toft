import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { connectTo, defaultNetworkAlias } from "./network-alias";

// The backend's own vectors (internal/service/connect_to_test.go reads the
// same file): the two rules fail together if they drift.
const vectors: { name: string; environment: string; alias: string }[] = JSON.parse(
  readFileSync(join("..", "backend", "internal", "service", "testdata", "network_alias.json"), "utf8"),
);

describe("defaultNetworkAlias", () => {
  it("names the alias a new service gets, as the backend does", () => {
    expect(vectors.length).toBeGreaterThan(0);
    for (const v of vectors) expect(defaultNetworkAlias(v.name, v.environment), `${v.name} in ${v.environment}`).toBe(v.alias);
  });

  // QA #2: New service → Image, whoami in qa, port 80.
  it("gives a service in an environment the environment's name", () => {
    expect(`${defaultNetworkAlias("whoami", "qa")}:80`).toBe("whoami-qa:80");
  });
});

describe("connectTo", () => {
  it("is the address the API names, without a port when it has none", () => {
    expect(connectTo({ connect_to: { host: "umami", port: 3000 } })).toBe("umami:3000");
    expect(connectTo({ connect_to: { host: "jobs-production", port: 0 } })).toBe("jobs-production");
    expect(connectTo({})).toBeNull();
  });
});
