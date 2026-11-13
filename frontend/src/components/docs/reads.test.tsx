import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DocsIndex from "@/app/projects/[id]/docs/page";
import { ConnectedWork } from "./connected-work";
import { DocEditor } from "./editor";
import { DeletedDocs, DocHistory } from "./history";
import { DocReader } from "./reader";
import { DocsTree } from "./tree";

// I3, I4, I19: a read that failed is never drawn as a page that doesn't
// exist, an empty tree or a blank.

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "p1", page: "d1" }),
  usePathname: () => "/projects/p1/docs/d1",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: { id: "u1" } }), useOrgRole: () => "owner", useIsOperator: () => false }));

function stubAPI(status: (path: string, search: string) => number) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>(async (input) => {
      const url = new URL(String(input), location.origin);
      const s = status(url.pathname, url.search);
      if (s !== 200) return new Response(JSON.stringify({ error: "boom" }), { status: s });
      return new Response(JSON.stringify({ data: [], next: null }), { status: 200 });
    }),
  );
}
const failsDocs = (status: number) => (path: string) => (path.includes("/docs") ? status : 200);
function mount(ui: React.ReactNode) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>);
}
afterEach(() => vi.unstubAllGlobals());

const missing = /doesn’t exist, or isn’t this project’s/;

describe("the docs when a read fails", () => {
  it("/docs says the pages failed to load, with Retry, never “No pages yet”", async () => {
    stubAPI(failsDocs(500));
    mount(<DocsIndex />);
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText("No pages yet")).toBeNull();
    expect(document.querySelector("[aria-busy]")).toBeNull();
  });

  it("the tree says the pages failed to load, with Retry", async () => {
    let failing = true;
    stubAPI((p) => (failing && p.includes("/docs") ? 500 : 200));
    mount(<DocsTree projectId="p1" />);
    expect(await screen.findByText("Couldn’t load the pages.")).toBeInTheDocument();
    failing = false;
    await userEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(await screen.findByRole("link", { name: /New page/ })).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load the pages.")).toBeNull();
  });

  it("the tree’s search says it failed, never that no page matches", async () => {
    stubAPI((p) => (p.includes("search") ? 500 : 200));
    mount(<DocsTree projectId="p1" />);
    await userEvent.type(screen.getByRole("searchbox", { name: "Search docs" }), "deploy");
    expect(await screen.findByText("Couldn’t load the search results.")).toBeInTheDocument();
    expect(screen.queryByText("No page matches.")).toBeNull();
  });

  it("a page that failed to load says so, with Retry, not that it doesn’t exist", async () => {
    stubAPI(failsDocs(500));
    mount(<DocReader projectId="p1" pageId="d1" />);
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(missing)).toBeNull();
  });

  it("a page that isn’t there says so only on a 404", async () => {
    stubAPI(failsDocs(404));
    mount(<DocReader projectId="p1" pageId="d1" />);
    expect(await screen.findByText(missing)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
  });

  it("the editor says the same: a failure isn’t a missing page", async () => {
    stubAPI(failsDocs(500));
    mount(<DocEditor projectId="p1" pageId="d1" />);
    expect(await screen.findByRole("heading", { name: "Couldn’t load this page", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(missing)).toBeNull();
  });

  it("the editor keeps the not-found text for a 404", async () => {
    stubAPI(failsDocs(404));
    mount(<DocEditor projectId="p1" pageId="d1" />);
    expect(await screen.findByText(missing)).toBeInTheDocument();
  });

  it("History says the versions failed to load, with Retry", async () => {
    stubAPI((p) => (p.endsWith("/versions") ? 500 : p.includes("/docs") ? 404 : 200));
    mount(<DocHistory projectId="p1" pageId="d1" />);
    expect(await screen.findByText("Couldn’t load the versions.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });

  it("History with the page's read failed too has a heading and says so in the article", async () => {
    stubAPI((p) => (p.includes("/docs") ? 500 : 200));
    mount(<DocHistory projectId="p1" pageId="d1" />);
    expect(await screen.findByRole("heading", { name: "History", level: 1 })).toBeInTheDocument();
    const article = screen.getByRole("article");
    expect(await within(article).findByText("Couldn’t load this page.")).toBeInTheDocument();
    expect(within(article).getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
  });

  it("Deleted pages says they failed to load, never that nothing was deleted", async () => {
    stubAPI((p, q) => (p.includes("/docs") && q.includes("deleted") ? 500 : 200));
    mount(<DeletedDocs projectId="p1" />);
    expect(await screen.findByText("Couldn’t load the deleted pages.")).toBeInTheDocument();
    expect(screen.queryByText(/Nothing deleted/)).toBeNull();
  });

  it("Connected work says it failed to load, with Retry", async () => {
    stubAPI((p) => (p.endsWith("/work") ? 500 : 200));
    mount(<ConnectedWork serviceId="s1" projectId="p1" name="api" />);
    expect(await screen.findByText("Couldn’t load the connected work.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeInTheDocument();
    expect(screen.queryByText(/No open pull request/)).toBeNull();
  });
});
