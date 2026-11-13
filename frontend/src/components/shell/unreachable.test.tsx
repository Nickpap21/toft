import { QueryClient, QueryClientProvider, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, fetchAPI, unreachable } from "@/lib/api/client";
import ProjectPage from "@/app/projects/[id]/page";
import ServersPage from "@/app/servers/page";
import RepoLayout from "@/app/projects/[id]/code/[repo]/layout";
import BranchesPage from "@/app/projects/[id]/code/[repo]/branches/page";
import CodeIndex from "@/app/projects/[id]/code/page";
import { LoadFailed, ReachablePage, Read, ReadFailed, allRead, readFailed } from "./unreachable";

// QA #5 and #13: with the backend stopped (Traefik answers 502) or its git
// server gone (503 git_unavailable), a page says Toft isn't answering,
// never "not found", "0" or a skeleton that never ends.

let params: Record<string, string> = {};
vi.mock("next/navigation", () => ({
  useParams: () => params,
  usePathname: () => "/",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ token: "t", isLoading: false, user: { id: "u1", orgId: "o1", orgName: "QA Lab", orgs: [] } }),
  useOrgRole: () => "owner",
  useIsOperator: () => false,
}));
vi.mock("@/context/BreadcrumbContext", () => ({ useBreadcrumbs: () => ({ set: vi.fn() }) }));

type Answer = { status: number; body: string };
const json = (status: number, v: unknown): Answer => ({ status, body: JSON.stringify(v) });
const badGateway: Answer = { status: 502, body: "Bad Gateway" }; // Traefik's, not the API's
const gitDown = { error: "the git server is not answering", code: "git_unavailable", data: { code: "git_unavailable" } };
/** Answers each request with the first route its path starts with. */
function serve(routes: [string, Answer][], fallback: Answer = badGateway) {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const a = routes.find(([p]) => new URL(url, "http://x").pathname.startsWith(p))?.[1] ?? fallback;
    return new Response(a.body, { status: a.status });
  }));
}
afterEach(() => vi.unstubAllGlobals());

function renderInShell(page: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><ReachablePage>{page}</ReachablePage></QueryClientProvider>);
}

describe("unreachable", () => {
  it("tells Toft's silence from the API's own answers", () => {
    expect(unreachable(new ApiError("x", 0, null))).toBe("toft");
    expect(unreachable(new ApiError("x", 502, {}))).toBe("toft");
    expect(unreachable(new ApiError("x", 504, {}))).toBe("toft");
    // The API's own 502 (a remote server it couldn't reach) is about that server.
    expect(unreachable(new ApiError("connection failed", 502, { error: "connection failed" }))).toBeNull();
    expect(unreachable(new ApiError("x", 503, { error: "x", code: "git_unavailable", data: { code: "git_unavailable" } }))).toBe("git");
    expect(unreachable(new ApiError("x", 404, { error: "project not found" }))).toBeNull();
    expect(unreachable(new Error("x"))).toBeNull();
  });

  it("a request that gets no answer at all is Toft not answering", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
    const err = await fetchAPI("/api/v1/servers").catch((e: unknown) => e);
    expect(unreachable(err)).toBe("toft");
  });
});

describe("a page whose reads can't reach Toft", () => {
  it("the project page with a 502 says Toft isn't answering, not Project not found", async () => {
    params = { id: "p1" };
    serve([]);
    renderInShell(<ProjectPage />);
    expect(await screen.findByRole("heading", { name: "Toft isn’t answering" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Project not found" })).toBeNull();
  });

  it("the project page with a real 404 still says Project not found", async () => {
    params = { id: "p1" };
    serve([["/api/v1/projects/p1", json(404, { error: "project not found", status: 404 })]], json(200, { data: [] }));
    renderInShell(<ProjectPage />);
    expect(await screen.findByRole("heading", { name: "Project not found" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Toft isn’t answering" })).toBeNull();
  });

  it("Servers with a 502 says Toft isn't answering, not 0 servers", async () => {
    params = {};
    serve([]);
    renderInShell(<ServersPage />);
    expect(await screen.findByRole("heading", { name: "Toft isn’t answering" })).toBeInTheDocument();
    expect(screen.queryByText(/^0 servers/)).toBeNull();
  });

  it("Retry reads again, and the page comes back once Toft answers", async () => {
    params = {};
    serve([]);
    renderInShell(<ServersPage />);
    const retry = await screen.findByRole("button", { name: /^Retry/ });
    serve([], json(200, { data: [] }));
    fireEvent.click(retry);
    expect(await screen.findByRole("heading", { name: "No servers yet" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Toft isn’t answering" })).toBeNull();
  });

  it("is announced and takes focus to its heading when it replaces the page", async () => {
    params = {};
    serve([]);
    renderInShell(<ServersPage />);
    const heading = await screen.findByRole("heading", { name: "Toft isn’t answering" });
    expect(screen.getByRole("alert")).toContainElement(heading);
    await waitFor(() => expect(heading).toHaveFocus());
  });

  it("stays while Retry's reads are in flight, not the page's skeleton in between", async () => {
    params = {};
    serve([]);
    renderInShell(<ServersPage />);
    const retry = await screen.findByRole("button", { name: /^Retry/ });
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    fireEvent.click(retry);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    expect(screen.getByRole("heading", { name: "Toft isn’t answering" })).toBeInTheDocument();
  });

  it("a page whose own data loaded stays when a background read fails", async () => {
    let calls = 0;
    vi.stubGlobal("fetch", vi.fn(async () => (calls++ ? new Response("Bad Gateway", { status: 502 }) : new Response(JSON.stringify({ data: "ok" }), { status: 200 }))));
    function Page() {
      const page = useQuery({ queryKey: ["page"], queryFn: () => Promise.resolve("form") });
      const poll = useQuery({ queryKey: ["poll"], queryFn: () => fetchAPI<string>("/api/v1/poll") });
      return <><h1>{page.data}</h1><p>{poll.isError ? "poll failed" : poll.data}</p><button type="button" onClick={() => void poll.refetch()}>Poll</button></>;
    }
    renderInShell(<Page />);
    expect(await screen.findByText("ok")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Poll" }));
    expect(await screen.findByText("poll failed")).toBeVisible();
    expect(screen.getByRole("heading", { name: "form" })).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Toft isn’t answering" })).toBeNull();
  });

  it("the project page with a 500 says it couldn't load, never Project not found", async () => {
    params = { id: "p1" };
    serve([["/api/v1/projects/p1", json(500, { error: "failed to get project", status: 500 })]], json(200, { data: [] }));
    renderInShell(<ProjectPage />);
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page" })).toBeInTheDocument();
    expect(screen.queryByText(/failed to get project/)).toBeNull();
    expect(screen.queryByRole("heading", { name: "Project not found" })).toBeNull();
  });

  it("a Code page whose read finds git gone, after the repository loaded, says git isn't answering", async () => {
    params = { id: "p1", repo: "shop" };
    const detail = { id: "r1", name: "shop", default_branch: "main", branch_count: 1, tag_count: 0, empty: false, pulls: { open: 0, merged: 0, closed: 0 } };
    serve([
      ["/api/v1/git", json(200, { data: { enabled: true } })],
      ["/api/v1/projects/p1/repositories/r1/branches", json(503, gitDown)],
      ["/api/v1/projects/p1/repositories/r1", json(200, { data: detail })],
      ["/api/v1/projects/p1/repositories", json(200, { data: [{ id: "r1", name: "shop" }] })],
    ], json(200, { data: [] }));
    renderInShell(<RepoLayout><BranchesPage /></RepoLayout>);
    expect(await screen.findByRole("heading", { name: "Toft can’t read this repository right now" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading")).toBeNull();
  });

  it("/code with the repositories unreadable says so, not a skeleton", async () => {
    params = { id: "p1" };
    serve([
      ["/api/v1/git", json(200, { data: { enabled: true } })],
      ["/api/v1/projects/p1/repositories", json(500, { error: "failed to list repositories", status: 500 })],
    ]);
    renderInShell(<CodeIndex />);
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading")).toBeNull();
  });

  it("a repository's Code with git_unavailable says so, not a skeleton", async () => {
    params = { id: "p1", repo: "shop" };
    serve([
      ["/api/v1/git", json(200, { data: { enabled: true } })],
      ["/api/v1/projects/p1/repositories/r1", json(503, gitDown)],
      ["/api/v1/projects/p1/repositories", json(200, { data: [{ id: "r1", name: "shop" }] })],
    ]);
    renderInShell(<RepoLayout><p>files</p></RepoLayout>);
    expect(await screen.findByRole("heading", { name: "Toft can’t read this repository right now" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading")).toBeNull();
  });
});

// P12, S11: a refusal or a 404 is an answer Retry can't change.
describe("ReadFailed on an answer Retry can't change", () => {
  const refused = new ApiError("forbidden", 403, { error: "forbidden", status: 403 });
  const gone = new ApiError("service not found", 404, { error: "service not found", status: 404 });

  it("a 403 says you have no access and whom to ask, with no Retry", () => {
    render(<ReadFailed error={refused} onRetry={vi.fn()} />);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("You don’t have access to this");
    expect(screen.getByRole("alert").textContent).toMatch(/owner of this workspace/);
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
    expect(screen.getByRole("alert").textContent).not.toMatch(/backend/);
  });

  it("a 404 says it no longer exists, with no Retry", () => {
    render(<ReadFailed heading="h2" error={gone} onRetry={vi.fn()} />);
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("This no longer exists");
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
    expect(screen.getByRole("alert").textContent).not.toMatch(/backend/);
  });

  it("links back where the page says, in Retry's place", () => {
    render(<ReadFailed error={refused} onRetry={vi.fn()} back={{ href: "/projects/p1/services", label: "Services" }} />);
    expect(screen.getByRole("link", { name: "Back to Services" }).getAttribute("href")).toBe("/projects/p1/services");
  });

  it("any other answer keeps Retry, and shows no link back", () => {
    const onRetry = vi.fn();
    render(<ReadFailed error={new ApiError("boom", 500, { error: "boom" })} onRetry={onRetry} back={{ href: "/x", label: "X" }} />);
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(onRetry).toHaveBeenCalledOnce();
    expect(screen.queryByRole("link")).toBeNull();
  });

  // X4: a 5xx's message is internal; a 4xx's is written for the person.
  it("never prints a 5xx's message, and says what to do", () => {
    render(<ReadFailed error={new ApiError("boom: pq internal detail", 500, { error: "boom: pq internal detail" })} onRetry={vi.fn()} />);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Couldn’t load this page");
    expect(screen.getByRole("alert").textContent).not.toMatch(/pq internal/);
    expect(screen.getByRole("alert").textContent).toMatch(/Retry; if it keeps failing/);
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });

  it("nor a programming error's", () => {
    render(<ReadFailed error={new TypeError("x is not a function")} onRetry={vi.fn()} />);
    expect(screen.getByRole("alert").textContent).not.toMatch(/not a function/);
  });

  it("keeps a 4xx's message, which the API wrote for the person", () => {
    render(<ReadFailed error={new ApiError("that filter isn’t valid", 400, { error: "that filter isn’t valid" })} onRetry={vi.fn()} />);
    expect(screen.getByRole("alert").textContent).toMatch(/that filter isn’t valid/);
  });

  it("carries data-read-state, as every state that isn't the data does", () => {
    render(<ReadFailed error={refused} onRetry={vi.fn()} />);
    expect(screen.getByRole("alert").closest("[data-read-state]")?.getAttribute("data-read-state")).toBe("failed");
  });
});

// The one shape for a read: a failed read is never drawn as an answer.
describe("Read", () => {
  const never = () => new Promise<string[]>(() => {});
  const EMPTY = "No shared variables yet";

  function Vars({ fn, ...rest }: { fn: () => Promise<string[]>; loading?: React.ReactNode; heading?: "h1" | "h2" }) {
    const q = useQuery({ queryKey: ["vars"], queryFn: fn });
    return (
      <Read query={q} what="the shared variables" isEmpty={(d) => d.length === 0} empty={<p>{EMPTY}</p>} {...rest}>
        {(d) => <p>{d.join(", ")}</p>}
      </Read>
    );
  }
  function mount(ui: React.ReactNode) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
    return client;
  }
  const state = () => document.querySelector("[data-read-state]")?.getAttribute("data-read-state");

  it("while pending shows a named status, never the empty text", () => {
    mount(<Vars fn={never} />);
    expect(screen.getByRole("status", { name: "Loading the shared variables" })).toBeInTheDocument();
    expect(state()).toBe("loading");
    expect(screen.queryByText(EMPTY)).toBeNull();
  });

  it("while pending shows the loading slot in place of the default", () => {
    mount(<Vars fn={never} loading={<p>skeleton</p>} />);
    expect(screen.getByText("skeleton").closest("[data-read-state]")?.getAttribute("data-read-state")).toBe("loading");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("is never blank: an empty loading slot falls back to the default", () => {
    mount(<Vars fn={never} loading={null} />);
    expect(screen.getByRole("status", { name: "Loading the shared variables" })).toBeInTheDocument();
  });

  it("a failed read says so with Retry, never the empty text", async () => {
    mount(<Vars fn={() => Promise.reject(new Error("boom"))} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn’t load the shared variables.");
    expect(state()).toBe("failed");
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(EMPTY)).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("can ask for ReadFailed in place of the row", async () => {
    mount(<Vars heading="h2" fn={() => Promise.reject(new Error("boom"))} />);
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeInTheDocument();
    expect(state()).toBe("failed");
    expect(screen.queryByText("Couldn’t load the shared variables.")).toBeNull();
  });

  // React Query drops the error when a read with no data fetches again: a
  // poll would swap the row for a skeleton, and re-announce it, every time.
  it("a failed read fetching again stays failed until it settles", async () => {
    const fn = vi.fn<() => Promise<string[]>>().mockRejectedValueOnce(new Error("boom")).mockImplementation(never);
    const client = mount(<Vars fn={fn} />);
    await screen.findByRole("alert");
    void client.refetchQueries({ type: "active" });
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn’t load the shared variables.");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("Retry reads again, and the data replaces the row", async () => {
    const fn = vi.fn<() => Promise<string[]>>().mockRejectedValueOnce(new Error("boom")).mockResolvedValue(["API_URL"]);
    mount(<Vars fn={fn} />);
    fireEvent.click(await screen.findByRole("button", { name: /^Retry/ }));
    expect(await screen.findByText("API_URL")).toBeInTheDocument();
    expect(fn).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("with data shows the children and no read state", async () => {
    mount(<Vars fn={async () => ["API_URL", "PORT"]} />);
    expect(await screen.findByText("API_URL, PORT")).toBeInTheDocument();
    expect(state()).toBeUndefined();
  });

  it("with an empty answer shows the empty text", async () => {
    mount(<Vars fn={async () => []} />);
    expect((await screen.findByText(EMPTY)).closest("[data-read-state]")?.getAttribute("data-read-state")).toBe("empty");
  });

  it("takes an infinite query", async () => {
    function Log() {
      const q = useInfiniteQuery({ queryKey: ["log"], queryFn: async () => ["entry"], initialPageParam: 0, getNextPageParam: () => undefined });
      return <Read query={q} what="the log">{(d) => <p>{d.pages.flat().join()}</p>}</Read>;
    }
    mount(<Log />);
    expect(await screen.findByText("entry")).toBeInTheDocument();
  });

  it("a failed refetch with data on screen changes nothing", async () => {
    const fn = vi.fn<() => Promise<string[]>>().mockResolvedValueOnce(["API_URL"]).mockRejectedValue(new Error("boom"));
    const client = mount(<Vars fn={fn} />);
    await screen.findByText("API_URL");
    await client.refetchQueries({ type: "active" });
    await waitFor(() => expect(client.getQueryState(["vars"])?.status).toBe("error"));
    expect(screen.getByText("API_URL")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(state()).toBeUndefined();
  });

  it("a Retry on a read with no answer keeps the row and the button's focus", async () => {
    const fn = vi.fn<() => Promise<string[]>>().mockRejectedValueOnce(new Error("boom")).mockReturnValue(never());
    mount(<Vars fn={fn} />);
    const retry = await screen.findByRole("button", { name: /^Retry/ });
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: /^Retry/ })).toBe(retry);
    expect(retry).toHaveFocus();
  });
});

describe("readFailed", () => {
  const q = (data: unknown, isError: boolean, errorUpdateCount: number) => ({ data, isError, errorUpdateCount });
  it("is a failure with nothing to show, also while it reads again", () => {
    expect(readFailed(q(undefined, true, 1))).toBe(true);
    expect(readFailed(q(undefined, false, 1))).toBe(true);
  });
  it("isn't a first load, or a failed refetch with data on screen", () => {
    expect(readFailed(q(undefined, false, 0))).toBe(false);
    expect(readFailed(q(["kept"], true, 2))).toBe(false);
    expect(readFailed(q(null, false, 1))).toBe(false);
  });
});

describe("allRead", () => {
  const r = (data: unknown, error: unknown, errorUpdateCount = 0) => ({ data, error, errorUpdateCount, refetch: vi.fn() });

  it("counts only the reads with nothing to show", () => {
    const stale = r(["old"], new Error("poll failed"), 3);
    const failed = new Error("boom");
    expect(allRead(undefined, stale, r(undefined, failed, 1))).toMatchObject({ error: failed, errorUpdateCount: 1 });
    expect(allRead(undefined, stale, r(undefined, null))).toMatchObject({ error: null, errorUpdateCount: 0 });
  });

  it("reads every one of them again", () => {
    const a = r(undefined, null);
    const b = r(["b"], null);
    void allRead(undefined, a, b).refetch();
    expect(a.refetch).toHaveBeenCalled();
    expect(b.refetch).toHaveBeenCalled();
  });
});

describe("LoadFailed", () => {
  it("names what it retries, so a list of buttons can tell them apart", () => {
    render(<LoadFailed what="the servers" onRetry={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Retry loading the servers" })).toHaveTextContent("Retry");
  });

  it("a 403 says there's no access, with no Retry", () => {
    render(<LoadFailed what="this issue" onRetry={vi.fn()} error={new ApiError("no", 403, null)} />);
    expect(screen.getByRole("alert")).toHaveTextContent("You don’t have access to this issue.");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("a 404 says Toft has no record of it, with no Retry", () => {
    render(<LoadFailed what="this issue" onRetry={vi.fn()} error={new ApiError("gone", 404, null)} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Toft has no record of this issue.");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("any other failure says it couldn't load, with Retry", () => {
    render(<LoadFailed what="this issue" onRetry={vi.fn()} error={new ApiError("boom", 500, null)} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn’t load this issue.");
    expect(screen.getByRole("button", { name: "Retry loading this issue" })).toBeInTheDocument();
  });

  it("Read's row form passes the answer on", () => {
    const query = { data: undefined, error: new ApiError("gone", 404, null), refetch: vi.fn() };
    render(<Read query={query} what="this issue">{() => null}</Read>);
    expect(screen.getByRole("alert")).toHaveTextContent("Toft has no record of this issue.");
    expect(screen.queryByRole("button")).toBeNull();
  });
});
