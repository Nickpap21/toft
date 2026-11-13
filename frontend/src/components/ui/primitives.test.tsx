import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Button } from "./button";
import { ServerStatusDot } from "./server-status-dot";
import { StatusBadge } from "./status-badge";

describe("StatusBadge", () => {
  it("renders the label with the tone's chip classes", () => {
    render(
      <>
        <StatusBadge status="running" />
        <StatusBadge status="failed" />
        <StatusBadge status="Deployed" />
      </>,
    );
    expect(screen.getByText("Running")).toHaveClass("bg-status-running-soft", "text-status-running-fg");
    // Failed is the soft chip now, not a solid red fill with white text (D-32).
    const failed = screen.getByText("Failed");
    expect(failed).toHaveClass("bg-status-failed-soft", "text-status-failed-fg");
    expect(failed.className).not.toMatch(/text-white/);
    expect(screen.getByText("Deployed")).toHaveClass("bg-raised", "text-muted");
  });

  it("hides the dot from assistive tech, so the label is the whole name", () => {
    render(<StatusBadge status="building" />);
    const dot = screen.getByText("Building").querySelector("span");
    expect(dot).toHaveAttribute("aria-hidden", "true");
    expect(dot).toHaveClass("motion-safe:animate-pulse");
  });
});

describe("ServerStatusDot", () => {
  it("separates reachable from provisioned", () => {
    render(
      <>
        <ServerStatusDot status="active" provisionedAt="2026-10-01T00:00:00Z" />
        <ServerStatusDot status="active" provisionedAt={null} />
      </>,
    );
    expect(screen.getByText("active")).toHaveClass("bg-status-running-soft");
    expect(screen.getByText("Needs provisioning")).toHaveClass("bg-status-pending-soft");
  });

  it("shows a failing server as failed, not neutral", () => {
    render(<ServerStatusDot status="error" provisionedAt={null} />);
    expect(screen.getByText("error")).toHaveClass("bg-status-failed-soft");
  });
});

describe("Button", () => {
  it.each([
    ["default", ["bg-accent", "text-on-accent"]],
    ["destructive", ["bg-danger", "text-on-danger"]],
    ["outline", ["border-strong", "text-fg"]],
    ["secondary", ["bg-raised", "text-fg"]],
    ["ghost", ["text-muted"]],
    ["link", ["text-link"]],
  ] as const)("%s uses theme tokens", (variant, classes) => {
    render(<Button variant={variant}>Go</Button>);
    expect(screen.getByRole("button", { name: "Go" })).toHaveClass(...classes);
  });
});
