import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PolicyViolationList } from "./policy-violations";

const bindMount = {
  rule: "host_bind_mount",
  field: "volumes[0].host_path",
  message: "volume 1 mounts the server folder /var/www/northwind-images at /app/images in the container",
  title: "Bind mounts of host paths",
  why: "A container that mounts a folder of the server can read and change everything in it.",
  fix: "Use a named volume.",
  grantable: true,
};

describe("PolicyViolationList", () => {
  it("explains a violation: what it is, what it does, why, what to do, and where", () => {
    render(<PolicyViolationList violations={[bindMount]} tone="warning" title="Would be refused in enforce mode" />);
    expect(screen.getByText("Bind mounts of host paths")).toBeInTheDocument();
    expect(screen.getByText(/^Volume 1 mounts the server folder \/var\/www\/northwind-images/)).toBeInTheDocument();
    expect(screen.getByText(/can read and change everything in it/)).toBeInTheDocument();
    expect(screen.getByText(/Use a named volume\. If you trust this service, an instance operator can grant/)).toBeInTheDocument();
    expect(screen.getByText("volumes[0].host_path")).toBeInTheDocument();
  });

  it("under an exception, says it is allowed instead of what to change", () => {
    render(<PolicyViolationList violations={[bindMount]} tone="allowed" title="Allowed by this exception" />);
    expect(screen.getByRole("status")).toHaveTextContent("granted this service an exception");
    expect(screen.queryByText(/What to do/)).not.toBeInTheDocument();
  });

  it("offers no exception for a rule that cannot be granted", () => {
    render(<PolicyViolationList violations={[{ ...bindMount, rule: "reserved_label", grantable: false }]} />);
    expect(screen.getByRole("alert")).not.toHaveTextContent("grant");
  });
});
