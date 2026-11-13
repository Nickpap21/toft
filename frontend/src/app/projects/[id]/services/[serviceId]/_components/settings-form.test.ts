import { describe, expect, it } from "vitest";
import type { Service } from "@/lib/api/service.service";
import {
  changedFields,
  containerCheckProblem,
  deployTimeoutProblem,
  initForm,
  onlyEdited,
  payloadOf,
  pidsLimitProblem,
  resync,
  sectionOf,
  withCard,
} from "./settings-form";

const service = {
  id: "svc-1",
  name: "web",
  source_type: "docker-image",
  docker_image: "nginx:1.27",
  memory_limit: "512m",
  health_check_path: "/health",
  health_check_retries: 3,
} as unknown as Service;

describe("saving one card", () => {
  it("sends the saved service with only that card's edits", () => {
    const saved = initForm(service);
    const form = { ...saved, name: "web-2", memory_limit: "1g" };
    expect(changedFields("general", form, saved)).toEqual(["name"]);
    expect(changedFields("resources", form, saved)).toEqual(["memory_limit"]);
    const sent = withCard("general", form, saved);
    expect(sent.name).toBe("web-2");
    expect(sent.memory_limit).toBe("512m");
  });

  it("sends only the card's edited fields, so the rest keep what's stored", () => {
    const saved = initForm({ ...service, build_pack: "auto" } as Service);
    const edited = { ...saved, name: "web-2", docker_image: "nginx:1.28" };
    const send = (c: "general" | "source", f = edited) =>
      JSON.parse(JSON.stringify(onlyEdited(c, f, saved, false, payloadOf(f, [], false))));
    expect(send("general")).toEqual({ name: "web-2" });
    // Not build_pack, which initForm shows as "dockerfile".
    expect(send("source")).toEqual({ docker_image: "nginx:1.28" });
    const access = { ...saved, basicAuthUser: "ops", ipAllowlist: "10.0.0.1" };
    expect(
      onlyEdited("access", access, saved, false, payloadOf(access, [], false)),
    ).toEqual({ basic_auth_user: "ops", ip_allowlist: ["10.0.0.1"] });
    const cmds = [{ container: "", command: "ls" }];
    expect(
      onlyEdited("hooks", saved, saved, true, payloadOf(saved, cmds, false)),
    ).toEqual({ post_deploy_commands: ":ls" });
  });

  it("keeps the other cards' edits when the service comes back saved", () => {
    const oldSaved = initForm(service);
    const prev = { ...oldSaved, name: "web-2", memory_limit: "1g" };
    const fresh = initForm({ ...service, name: "web-2" } as Service);
    const next = resync(prev, oldSaved, fresh, new Set(["name"]));
    expect(next.name).toBe("web-2");
    expect(next.memory_limit).toBe("1g");
  });

  it("clears a saved password field, which never comes back", () => {
    const oldSaved = initForm(service);
    const prev = {
      ...oldSaved,
      basicAuthUser: "ops",
      basicAuthPassword: "s3cret",
    };
    const next = resync(
      prev,
      oldSaved,
      initForm({ ...service, basic_auth_user: "ops" } as Service),
      new Set(["basicAuthUser", "basicAuthPassword", "ipAllowlist"]),
    );
    expect(next.basicAuthPassword).toBe("");
  });

  it("trims and splits what the API stores trimmed", () => {
    const p = payloadOf(
      {
        ...initForm(service),
        name: " web ",
        ipAllowlist: "1.2.3.4/32\n\n 10.0.0.0/8 ",
      },
      [],
      false,
    );
    expect(p.name).toBe("web");
    expect(p.ip_allowlist).toEqual(["1.2.3.4/32", "10.0.0.0/8"]);
    expect(p.basic_auth_password).toBeUndefined();
  });
});

describe("the process limit (D-93)", () => {
  it("shows the default, keeps it the default, and checks the range", () => {
    const saved = initForm(service);
    expect(saved.pids_limit).toBe("2048");
    expect(payloadOf(saved, [], false).pids_limit).toBe(0);
    expect(payloadOf({ ...saved, pids_limit: "4096" }, [], false).pids_limit).toBe(4096);
    expect(initForm({ ...service, pids_limit: 512 }).pids_limit).toBe("512");
    expect(changedFields("isolation", { ...saved, pids_limit: "512" }, saved)).toEqual(["pids_limit"]);
    for (const ok of ["256", "32768", " 2048 "]) expect(pidsLimitProblem(ok)).toBe("");
    for (const bad of ["255", "32769", "1e3", "", "2048.5", "-1"]) expect(pidsLimitProblem(bad)).not.toBe("");
  });
});

describe("the deploy time limit (D-125)", () => {
  it("leaves empty as the default, saves a number, and checks the range", () => {
    const saved = initForm(service);
    expect(saved.deploy_timeout_minutes).toBe("");
    expect(payloadOf(saved, [], false).deploy_timeout_minutes).toBe(0);
    expect(payloadOf({ ...saved, deploy_timeout_minutes: " 90 " }, [], true).deploy_timeout_minutes).toBe(90);
    expect(initForm({ ...service, deploy_timeout_minutes: 120 }).deploy_timeout_minutes).toBe("120");
    expect(changedFields("deploy", { ...saved, deploy_timeout_minutes: "90" }, saved)).toEqual(["deploy_timeout_minutes"]);
    for (const ok of ["", "5", "240", " 60 "]) expect(deployTimeoutProblem(ok)).toBe("");
    for (const bad of ["4", "241", "1e2", "60.5", "-5", "abc"]) expect(deployTimeoutProblem(bad)).not.toBe("");
  });
});

describe("the container check", () => {
  it("defaults to the image's and saves only what was edited", () => {
    const saved = initForm(service);
    expect(saved.container_health).toBe("image");
    const form = { ...saved, container_health: "command" as const, container_health_command: " pgrep -f worker " };
    expect(onlyEdited("health", form, saved, false, payloadOf(form, [], false))).toEqual({
      container_health: "command",
      container_health_command: "pgrep -f worker",
    });
    const off = { ...saved, container_health: "off" as const };
    expect(onlyEdited("health", off, saved, false, payloadOf(off, [], false))).toEqual({ container_health: "off" });
  });

  it("needs a one-line command in command mode", () => {
    expect(containerCheckProblem("image", "")).toBe("");
    expect(containerCheckProblem("off", "")).toBe("");
    expect(containerCheckProblem("command", "pgrep -f worker")).toBe("");
    expect(containerCheckProblem("command", "  ")).not.toBe("");
    expect(containerCheckProblem("command", "a\nb")).not.toBe("");
  });
});

describe("old section ids (D-155)", () => {
  it("land on the section that holds them now", () => {
    expect(sectionOf("routing")).toBe("domains");
    expect(sectionOf("creds")).toBe("source");
    expect(sectionOf("deps")).toBe("deploy");
    expect(sectionOf("volumes")).toBe("storage");
    expect(sectionOf("config-files")).toBe("storage");
    expect(sectionOf("access")).toBe("access");
  });
});
