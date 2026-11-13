import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { Service } from "@/lib/api/service.service";
import { AdminLoginRow } from "./admin-password-reset";

// D-160's Reset: the dialog opens only once the password is confirmed; a
// protected environment asks for the name (MinIO's too, as its deploy's
// confirm); MinIO's deploys; a refusal says nothing changed and shows the
// app's output apart.

let confirmed = true;
vi.mock("@/context/AuthContext", () => ({ useOrgRole: () => "developer" }));
vi.mock("@/components/reauth", () => ({ useReauth: () => async () => confirmed }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const calls: string[] = [];
let pending: unknown;
let refuse: ApiError | null = null;
vi.mock("@/lib/api/client", async (orig) => ({
  ...(await orig<typeof import("@/lib/api/client")>()),
  fetchAPI: async (path: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? "GET"} ${path}`);
    if (path === "/api/v1/catalog") return [{ slug: "grafana", name: "Grafana" }, { slug: "minio", name: "MinIO" }];
    if (path.endsWith("/admin-password") && init?.method === "POST") {
      if (refuse) throw refuse;
      return { what: "admin_password", password: "new-one" };
    }
    if (path.endsWith("/admin-password")) return pending;
    return null;
  },
}));

const grafana = { id: "g1", name: "grafana", addon_slug: "grafana" } as Service;
const minio = { id: "m1", name: "files", addon_slug: "minio" } as Service;

function renderRow(service: Service, onDeploy = vi.fn(), prot = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AdminLoginRow service={service} serviceId={service.id} env={{ name: "production", color: "amber", protected: prot }} onDeploy={onDeploy} />
    </QueryClientProvider>,
  );
  return onDeploy;
}
const posted = () => calls.filter((c) => c.startsWith("POST")).length;

beforeEach(() => {
  calls.length = 0;
  confirmed = true;
  refuse = null;
  pending = { pending: false, reset: "app", user: "boss" };
});

describe("AdminLoginRow", () => {
  it("opens only once the password is confirmed", async () => {
    const onDeploy = renderRow(grafana);
    expect(await screen.findByText("boss")).toBeTruthy();
    confirmed = false;
    fireEvent.click(screen.getByRole("button", { name: "Reset admin password…" }));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("dialog")).toBeNull();
    confirmed = true;
    fireEvent.click(screen.getByRole("button", { name: "Reset admin password…" }));
    fireEvent.click(await screen.findByRole("button", { name: "Reset password" }));
    await vi.waitFor(() => expect(posted()).toBe(1));
    await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(onDeploy).not.toHaveBeenCalled();
  });

  it("asks for the name in a protected environment", async () => {
    renderRow(grafana, vi.fn(), true);
    fireEvent.click(await screen.findByRole("button", { name: "Reset admin password…" }));
    const go = await screen.findByRole("button", { name: "Reset password" });
    expect(go.hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "grafana " } });
    expect(go.hasAttribute("disabled")).toBe(false);
  });

  it("deploys MinIO after its reset", async () => {
    pending = { pending: false, reset: "deploy", user: "minioadmin" };
    const onDeploy = renderRow(minio, vi.fn(), true);
    fireEvent.click(await screen.findByRole("button", { name: "Reset admin password…" }));
    fireEvent.change(await screen.findByRole("textbox"), { target: { value: "files" } });
    fireEvent.click(screen.getByRole("button", { name: "Reset and deploy" }));
    await vi.waitFor(() => expect(onDeploy).toHaveBeenCalledTimes(1));
  });

  it("shows the app's refusal", async () => {
    refuse = new ApiError("the app refused the new password, so nothing changed", 502, { error: "x", data: { code: "reset_failed", output: "no such user" } });
    renderRow(grafana);
    fireEvent.click(await screen.findByRole("button", { name: "Reset admin password…" }));
    fireEvent.click(await screen.findByRole("button", { name: "Reset password" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Grafana refused the new password, so nothing changed.");
    expect(alert.querySelector("pre")?.textContent).toBe("no such user");
  });

  it("renders nothing for an app without Reset", async () => {
    pending = { pending: true, what: "admin_password", reason: "created" };
    renderRow({ id: "p1", name: "db", addon_slug: "postgres" } as Service);
    await vi.waitFor(() => expect(calls).toContain("GET /api/v1/services/p1/admin-password"));
    expect(screen.queryByRole("button")).toBeNull();
  });
});
