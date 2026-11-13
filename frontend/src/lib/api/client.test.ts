import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, fetchAPI, forgetReauth, reauthHeader, reauthProof, saveReauth, setTabOrg, streamEvents, tabOrg } from "./client";

const A = "0a000000-0000-4000-8000-00000000000a";

/** A JWT that is not expired, so authedFetch sends it. */
const token = `x.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 3600 }))}.y`;

function stubFetch(...answers: Response[]) {
  const fetch = vi.fn<typeof globalThis.fetch>();
  for (const a of answers) fetch.mockResolvedValueOnce(a);
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the tab's workspace (D-52)", () => {
  it("is sent with every request once the tab has one, and not before", async () => {
    localStorage.setItem("paas_token", token);
    const fetch = stubFetch(json(200, { data: 1 }), json(200, { data: 2 }));

    await fetchAPI("/api/v1/org");
    expect(fetch.mock.calls[0][1]?.headers).not.toHaveProperty("X-Toft-Org");

    setTabOrg(A);
    await fetchAPI("/api/v1/projects");
    expect(fetch.mock.calls[1][1]?.headers).toMatchObject({ "X-Toft-Org": A, Authorization: `Bearer ${token}` });
  });

  it("is sent with an event stream", async () => {
    localStorage.setItem("paas_token", token);
    setTabOrg(A);
    const fetch = stubFetch(new Response("", { headers: { "Content-Type": "text/event-stream" } }));
    await streamEvents("/api/v1/events", () => {}, new AbortController().signal);
    expect(fetch.mock.calls[0][1]?.headers).toEqual({ Authorization: `Bearer ${token}`, "X-Toft-Org": A });
  });

  it("is forgotten when the server says it is gone, and Home opens in the default workspace", async () => {
    // A fresh module: the page it stands for is leaving.
    vi.resetModules();
    const c = await import("./client");
    localStorage.setItem("paas_token", token);
    c.setTabOrg(A);
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    const gone = () => json(403, { error: "you are not a member of this workspace", data: { code: "workspace_unavailable" } });
    const fetch = stubFetch(gone(), gone());

    const [first, second] = await Promise.allSettled([c.fetchAPI("/api/v1/projects"), c.fetchAPI("/api/v1/servers")]);
    expect(first.status === "rejected" && second.status === "rejected").toBe(true);
    expect(c.tabOrg()).toBeNull();
    expect(sessionStorage.getItem(c.ORG_GONE_KEY)).toBe(A);
    expect(assign).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledWith("/dashboard");

    await expect(c.fetchAPI("/api/v1/org")).rejects.toThrow("no longer a member");
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("leaves any other 403 to the caller", async () => {
    localStorage.setItem("paas_token", token);
    setTabOrg(A);
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    stubFetch(json(403, { error: "forbidden" }));

    await expect(fetchAPI("/api/v1/projects")).rejects.toBeInstanceOf(ApiError);
    expect(tabOrg()).toBe(A);
    expect(assign).not.toHaveBeenCalled();
  });
});

describe("a 401", () => {
  const fresh = `x.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 7200 }))}.z`;

  it("signs this browser out when it answers the token stored now", async () => {
    localStorage.setItem("paas_token", token);
    vi.stubGlobal("location", { ...window.location, href: "/projects", pathname: "/projects", search: "" });
    stubFetch(json(401, { error: "this session was signed out; sign in again" }));

    await expect(fetchAPI("/api/v1/projects")).rejects.toThrow("Session expired");
    expect(localStorage.getItem("paas_token")).toBeNull();
    expect(window.location.href).toBe("/?next=%2Fprojects");
  });

  it("for a token replaced while the request was out keeps the new one and retries with it", async () => {
    // Sign out other browsers: the old token is revoked and its successor
    // saved while this request, sent with the old one, is still out.
    localStorage.setItem("paas_token", token);
    vi.stubGlobal("location", { ...window.location, href: "/projects", pathname: "/projects", search: "" });
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementationOnce(async () => {
      localStorage.setItem("paas_token", fresh);
      return json(401, { error: "this session was signed out; sign in again" });
    }).mockResolvedValueOnce(json(200, { data: 7 }));
    vi.stubGlobal("fetch", fetch);

    await expect(fetchAPI("/api/v1/projects")).resolves.toBe(7);
    expect(localStorage.getItem("paas_token")).toBe(fresh);
    expect(window.location.href).toBe("/projects");
    expect(fetch.mock.calls[1][1]?.headers).toMatchObject({ Authorization: `Bearer ${fresh}` });
  });
});

describe("the password confirmation (D-53)", () => {
  it("lasts its expires_in on this browser's clock, less a margin", () => {
    vi.useFakeTimers();
    try {
      saveReauth("proof", 600);
      expect(reauthProof()).toBe("proof");
      expect(reauthHeader()).toEqual({ "X-Toft-Reauth": "proof" });
      vi.advanceTimersByTime(586_000);
      expect(reauthProof()).toBeNull();
      expect(reauthHeader()).toEqual({});
    } finally {
      vi.useRealTimers();
    }
  });

  it("is sent only by the requests that ask for it", async () => {
    localStorage.setItem("paas_token", token);
    saveReauth("proof", 600);
    const fetch = stubFetch(json(200, { data: 1 }));
    await fetchAPI("/api/v1/projects");
    expect(fetch.mock.calls[0][1]?.headers).not.toHaveProperty("X-Toft-Reauth");
  });

  it("is dropped when the server no longer accepts it, and kept otherwise", async () => {
    localStorage.setItem("paas_token", token);
    saveReauth("proof", 600);
    stubFetch(json(403, { error: "forbidden" }), json(403, { error: "confirm your password to continue", data: { code: "reauth_required" } }));

    await expect(fetchAPI("/api/v1/servers/s/host-key", { method: "PUT", headers: reauthHeader() })).rejects.toBeInstanceOf(ApiError);
    expect(reauthProof()).toBe("proof");
    await expect(fetchAPI("/api/v1/servers/s/host-key", { method: "PUT", headers: reauthHeader() })).rejects.toBeInstanceOf(ApiError);
    expect(reauthProof()).toBeNull();
    forgetReauth();
  });
});
