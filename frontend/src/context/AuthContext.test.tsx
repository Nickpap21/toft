import { useEffect } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ORG_GONE_KEY, setTabOrg, tabOrg } from "@/lib/api/client";
import { AuthProvider, useAuth, type AuthUser } from "./AuthContext";

const toast = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast }));

const A = "0a000000-0000-4000-8000-00000000000a";
const B = "0b000000-0000-4000-8000-00000000000b";
const orgs = [
  { org_id: A, name: "Founders", role: "owner" },
  { org_id: B, name: "Startup", role: "viewer" },
];
const token = `x.${btoa(JSON.stringify({ sub: "u-1", exp: Math.floor(Date.now() / 1000) + 3600 }))}.y`;

/** The API as these tests need it: GET /org answers the workspace the request
 *  names, or the account's default (`server`) without a header. */
function stubAPI(server: string) {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input, init) => {
    const path = new URL(String(input), location.origin).pathname;
    const named = (init?.headers as Record<string, string> | undefined)?.["X-Toft-Org"];
    const ok = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
    if (path === "/api/v1/me") return ok({ id: "u-1", email: "a@example.test", is_operator: false });
    if (path === "/api/v1/org/list") return ok(orgs);
    if (path === "/api/v1/org") {
      const id = named ?? server;
      return ok({ id, name: orgs.find((o) => o.org_id === id)!.name });
    }
    return new Response("{}", { status: 404 });
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function Shown() {
  const { user } = useAuth();
  return <p>{user ? `${user.orgName ?? "?"} ${user.role ?? "?"}` : "none"}</p>;
}

function signedIn(stored: Partial<AuthUser>) {
  localStorage.setItem("paas_token", token);
  localStorage.setItem("paas_user", JSON.stringify({ id: "u-1", email: "a@example.test", orgs, ...stored }));
}

const storedOrg = () => (JSON.parse(localStorage.getItem("paas_user")!) as AuthUser).orgId;

afterEach(() => {
  vi.unstubAllGlobals();
  toast.mockReset();
});

describe("AuthProvider: each tab keeps its workspace (D-52)", () => {
  it("shows the tab's workspace, not the browser's, and keeps the browser's", async () => {
    signedIn({ orgId: A, orgName: "Founders", role: "owner" });
    setTabOrg(B);
    const fetch = stubAPI(A);
    render(<AuthProvider><Shown /></AuthProvider>);

    expect(screen.getByText("Startup viewer")).toBeInTheDocument();
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
    expect(screen.getByText("Startup viewer")).toBeInTheDocument();
    expect(tabOrg()).toBe(B);
    expect(storedOrg()).toBe(A);
  });

  it("opens a new tab in the browser's workspace and pins it there", async () => {
    signedIn({ orgId: A, orgName: "Founders", role: "owner" });
    const fetch = stubAPI(B);
    render(<AuthProvider><Shown /></AuthProvider>);

    expect(tabOrg()).toBe(A);
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
    expect(screen.getByText("Founders owner")).toBeInTheDocument();
    const org = fetch.mock.calls.find(([u]) => new URL(String(u), location.origin).pathname === "/api/v1/org")!;
    expect(org[1]?.headers).toMatchObject({ "X-Toft-Org": A });
  });

  it("after a gone workspace, opens the account's default and says why", async () => {
    signedIn({ orgId: B, orgName: "Startup", role: "viewer" });
    sessionStorage.setItem(ORG_GONE_KEY, B);
    stubAPI(A);
    render(<AuthProvider><Shown /></AuthProvider>);

    await waitFor(() => expect(screen.getByText("Founders owner")).toBeInTheDocument());
    expect(tabOrg()).toBe(A);
    expect(storedOrg()).toBe(A);
    expect(sessionStorage.getItem(ORG_GONE_KEY)).toBeNull();
    expect(toast).toHaveBeenCalledWith("You're no longer a member of Startup", { description: "Opened Founders." });
  });
});

describe("AuthProvider: a switch closed mid-way (D-52)", () => {
  it("moves this browser's default as soon as the server has switched", async () => {
    signedIn({ orgId: A, orgName: "Founders", role: "owner" });
    setTabOrg(A);
    let switched = false;
    vi.stubGlobal("fetch", vi.fn<typeof globalThis.fetch>(async (input) => {
      const path = new URL(String(input), location.origin).pathname;
      if (path === "/api/v1/org/switch") {
        switched = true;
        return new Response(JSON.stringify({ data: { org_id: B, name: "Startup", role: "viewer" } }), { status: 200 });
      }
      // Everything after the switch hangs, as in a tab closed meanwhile.
      return new Promise<Response>(() => {});
    }));
    let switchOrg: (id: string) => Promise<void> = async () => {};
    function Grab() {
      const auth = useAuth();
      useEffect(() => {
        switchOrg = auth.switchOrg;
      }, [auth.switchOrg]);
      return null;
    }
    render(<AuthProvider><Grab /></AuthProvider>);

    void switchOrg(B);
    await waitFor(() => expect(switched).toBe(true));
    await waitFor(() => expect(storedOrg()).toBe(B));
    expect(tabOrg()).toBe(B);
    expect((JSON.parse(localStorage.getItem("paas_user")!) as AuthUser).orgName).toBe("Startup");
  });
});
