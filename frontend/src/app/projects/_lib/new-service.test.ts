import { describe, expect, it } from "vitest";
import { catalogRuns, catalogSettings, generatedSecret, pickedSub, protectedServed, serviceNameOf, uniqueName } from "./new-service";

describe("catalog entries", () => {
  it("generates a secret in the format its spec asks for (D-147)", () => {
    expect(generatedSecret()).toMatch(/^[A-HJ-NP-Za-km-z2-9]{24}$/);
    expect(generatedSecret("long")).toMatch(/^[A-HJ-NP-Za-km-z2-9]{64}$/);
    expect(generatedSecret("hex64")).toMatch(/^[0-9a-f]{64}$/);
    expect(generatedSecret("hex64")).not.toBe(generatedSecret("hex64"));
  });

  it("says what an entry runs: its image, or its compose stack", () => {
    expect(catalogRuns({ kind: "image", docker_image: "postgres:16", default_port: 5432 })).toBe("postgres:16 · 5432");
    expect(catalogRuns({ kind: "compose", docker_image: "", default_port: 8000, compose_services: 3 })).toBe("Compose · 3 services");
    expect(catalogRuns({ kind: "compose", docker_image: "", default_port: 3001, compose_services: 1 })).toBe("Compose · 1 service");
  });

  it("drops the port on a catalog tile", () => {
    expect(catalogRuns({ kind: "image", docker_image: "postgres:16", default_port: 5432 }, false)).toBe("postgres:16");
    expect(catalogRuns({ kind: "compose", docker_image: "", default_port: 3001, compose_services: 1 }, false)).toBe("Compose · 1 service");
  });

  it("says what a picked entry runs, its port and its volume (D-154)", () => {
    expect(pickedSub({ kind: "image", docker_image: "postgres:16", default_port: 5432, volumes: [{ name: "pg-data", target: "/var/lib/postgresql/data" }] })).toBe(
      "postgres:16 · port 5432 · a volume for its data",
    );
    expect(pickedSub({ kind: "compose", docker_image: "", default_port: 3001, compose_services: 1, volumes: [] })).toBe("Compose · 1 service · port 3001");
  });
});

describe("a new service's name", () => {
  it("is the entry's or repository's name, as a service name", () => {
    expect(serviceNameOf("Uptime Kuma")).toBe("uptime-kuma");
    expect(serviceNameOf("MinIO")).toBe("minio");
    expect(serviceNameOf("--My App!")).toBe("my-app");
  });

  it("takes the next free number when the scope has it", () => {
    expect(uniqueName("postgres", new Set())).toBe("postgres");
    expect(uniqueName("postgres", new Set(["postgres"]))).toBe("postgres-2");
    expect(uniqueName("postgres", new Set(["postgres", "postgres-2"]))).toBe("postgres-3");
    expect(uniqueName("", new Set([""]))).toBe("");
  });
});

describe("Create and deploy in a protected environment (D-154)", () => {
  const production = { id: "p", name: "production", protected: true };
  const staging = { id: "s", name: "staging", protected: false };
  const envs = [production, staging];

  it("confirms for a service in a protected environment, not in another", () => {
    expect(protectedServed("environment", production, envs, false)).toEqual([production]);
    expect(protectedServed("environment", staging, envs, false)).toEqual([]);
  });

  it("confirms for a project service when any environment it serves is protected", () => {
    expect(protectedServed("project", staging, envs, false)).toEqual([production]);
    expect(protectedServed("project", staging, [staging], false)).toEqual([]);
  });

  it("doesn't confirm a new project's first service", () => {
    expect(protectedServed("environment", production, envs, true)).toEqual([]);
  });
});

describe("a catalog entry's settings in the create (D-164)", () => {
  it("sends a cleared setting empty, so it stays empty, and leaves an empty secret to be generated", () => {
    expect(
      catalogSettings([
        { key: "POSTGRES_USER", value: "", secret: false },
        { key: "POSTGRES_DB", value: "shop", secret: false },
        { key: "POSTGRES_PASSWORD", value: "s3cret", secret: true },
        { key: "REPLICATION_PASSWORD", value: "", secret: true },
      ]),
    ).toEqual({
      secrets: [{ key: "POSTGRES_PASSWORD", value: "s3cret" }],
      variables: [
        { key: "POSTGRES_USER", value: "" },
        { key: "POSTGRES_DB", value: "shop" },
      ],
    });
  });
});
