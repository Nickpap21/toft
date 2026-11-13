import { describe, expect, it } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { deleteList, hasUnsavedChanges, initForm, parsePostDeployCmds } from "./settings-tab";

const service = {
    id: "svc-1",
    name: "web",
    source_type: "docker-image",
    docker_image: "nginx:1.27",
    memory_limit: "512m",
    post_deploy_commands: "web:php artisan migrate",
    volumes: [{ name: "data", target: "/data" }],
} as unknown as Service;

const savedCmds = () => parsePostDeployCmds(service.post_deploy_commands ?? "");

describe("hasUnsavedChanges", () => {
    it("is false for a form equal to the saved service, whatever the references", () => {
        expect(hasUnsavedChanges(initForm(service), savedCmds(), service)).toBe(false);
    });

    it("is true once a field changes", () => {
        expect(hasUnsavedChanges({ ...initForm(service), docker_image: "nginx:1.28" }, savedCmds(), service)).toBe(true);
    });

    it("is true once a post-deploy command changes", () => {
        const cmds = [{ container: "web", command: "php artisan migrate --force" }];
        expect(hasUnsavedChanges(initForm(service), cmds, service)).toBe(true);
    });
});

describe("deleteList", () => {
    it("counts what deleting takes, as the board words it, and leaves out what is empty", () => {
        expect(deleteList("the images built for it", { deployments: 48, variables: 12, secrets: 4, files: 0 }, ["a.example", "*.a.example", "a.dev"]))
            .toBe("the images built for it, 48 deployments, 12 variables, 4 secrets and its 3 domains, a.example, *.a.example and a.dev");
        expect(deleteList("the images built for it", { deployments: 1, variables: 0, secrets: 1, files: 2 }, ["a.example"]))
            .toBe("the images built for it, 1 deployment, 1 secret, 2 config files and its domain, a.example");
    });

    it("names the kinds while the counts load, and only the images when nothing else is left", () => {
        expect(deleteList("the images built for it", {}, [])).toBe("the images built for it, its deployments, variables, secrets and config files");
        expect(deleteList("the images built for it", { deployments: 0, variables: 0, secrets: 0, files: 0 }, [])).toBe("the images built for it");
    });
});
