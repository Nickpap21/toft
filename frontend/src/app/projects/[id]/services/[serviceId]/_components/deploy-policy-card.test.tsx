import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { ServiceDeployPolicy } from "@/lib/api/deploypolicy.service";
import { DeployPolicyBody } from "./deploy-policy-card";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/components/reauth", () => ({
  GRANT_EXCEPTION_WHY: "",
  REVOKE_EXCEPTION_WHY: "",
  useReauth: () => async () => true,
}));

const getForService = vi.fn();
const grantException = vi.fn();
vi.mock("@/lib/api/deploypolicy.service", () => ({
  deployPolicyService: {
    getForService: (...args: unknown[]) => getForService(...args),
    grantException: (...args: unknown[]) => grantException(...args),
  },
}));

const exception = {
  allowed_rules: ["privileged"],
  allowed_values: [],
  reason: "the first reason",
  updated_at: "2026-11-01T00:00:00Z",
};
const policy = {
  version: "v1",
  service_id: "svc-1",
  mode: "enforce",
  violations: [],
  allowed: [],
  exception,
  compose_checked_at_deploy: false,
  invalid_env_keys: [],
  rules: ["privileged", "docker_socket", "host_network"].map((rule) => ({ rule, description: rule, grantable: true })),
  can_manage_exceptions: true,
  control_plane_restricted: false,
  compose_services: [],
  last_deploy_check: null,
  values_digest: "digest",
} as unknown as ServiceDeployPolicy;

function renderBody() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <DeployPolicyBody serviceId="svc-1" name="web" />
    </QueryClientProvider>,
  );
}

describe("the exception dialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getForService.mockResolvedValue(policy);
  });

  it("keeps the rules and reason chosen through Read it again, and grants on the fresh read", async () => {
    grantException.mockRejectedValueOnce(new ApiError("changed", 409, { code: "changed_since_read" }));
    renderBody();

    await userEvent.click(await screen.findByRole("button", { name: "Change or revoke the exception…" }));
    await userEvent.click(screen.getByRole("checkbox", { name: /docker_socket/ }));
    const reason = screen.getByLabelText("Reason, written to the audit log");
    await userEvent.clear(reason);
    await userEvent.type(reason, "my reason");
    await userEvent.click(screen.getByRole("button", { name: "Update exception" }));
    expect(await screen.findByRole("alert")).toBeTruthy();

    // Another operator allowed host_network and wrote their own reason.
    getForService.mockResolvedValue({
      ...policy,
      version: "v2",
      exception: {
        ...exception,
        allowed_rules: ["privileged", "host_network"],
        reason: "their reason",
        updated_at: "2026-11-09T00:00:00Z",
      },
    });
    await userEvent.click(screen.getByRole("button", { name: "Read it again" }));
    await vi.waitFor(() =>
      expect((screen.getByRole("checkbox", { name: /host_network/ }) as HTMLInputElement).checked).toBe(true),
    );
    await vi.waitFor(() => expect(screen.queryByRole("alert")).toBeNull());

    expect((screen.getByLabelText("Reason, written to the audit log") as HTMLTextAreaElement).value).toBe("my reason");
    expect((screen.getByRole("checkbox", { name: /docker_socket/ }) as HTMLInputElement).checked).toBe(true);

    grantException.mockResolvedValue({ ...exception });
    await userEvent.click(screen.getByRole("button", { name: "Update exception" }));
    expect(grantException).toHaveBeenLastCalledWith(
      "svc-1",
      expect.arrayContaining(["privileged", "host_network", "docker_socket"]),
      "my reason",
      "digest",
      "v2",
    );
  });
});
