import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { Network } from "./overview-tab";

// web in production beside umami, a catalog compose stack (QA #9): the
// stack's containers answer to their compose service names, never to the
// service's network alias.
const web = {
  id: "web", name: "web", scope: "environment", environment_id: "env-1", source_type: "docker-image",
  network_alias: "web-production", port: 8080, connect_to: { host: "web-production", port: 8080 },
} as Service;
const umami = {
  id: "umami", name: "umami", scope: "environment", environment_id: "env-1", source_type: "docker-compose",
  addon_slug: "umami", compose_route_service: "umami", network_alias: "umami-production", port: 3000,
  connect_to: { host: "umami", port: 3000 },
} as Service;

describe("Network", () => {
  it("shows a compose neighbour at the address it answers to", () => {
    render(<Network service={web} serviceId="web" envServices={[web, umami]} />);
    expect(screen.getByText("umami:3000")).toBeTruthy();
    expect(screen.queryByText("umami-production:3000")).toBeNull();
    expect(screen.getByText("web-production:8080")).toBeTruthy();
  });

  // Mailpit, and MailHog services made before the catalog swapped it out.
  it.each(["mailpit", "mailhog"])("names a catalog neighbour's connection string at its real alias (%s)", (slug) => {
    const mail = {
      id: "mail", name: "mail", scope: "environment", environment_id: "env-1", source_type: "docker-image",
      addon_slug: slug, network_alias: "mail-qa", port: 8025, connect_to: { host: "mail-qa", port: 8025 },
    } as Service;
    render(<Network service={web} serviceId="web" envServices={[web, mail]} />);
    expect(screen.getByText("smtp://mail-qa:1025")).toBeTruthy();
  });

  it("shows no address for a neighbour the API names none for", () => {
    const stack = { ...umami, id: "stack", name: "stack", connect_to: undefined } as Service;
    render(<Network service={web} serviceId="web" envServices={[web, stack]} />);
    expect(screen.queryByText("stack")).toBeNull();
  });
});
