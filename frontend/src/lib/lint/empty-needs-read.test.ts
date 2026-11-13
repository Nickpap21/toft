import { RuleTester } from "eslint";
import parser from "@typescript-eslint/parser";
import { describe, it } from "vitest";
import rule from "./empty-needs-read.mjs";

// The cause of H2, P1, I5, W1 and their kin: a failed read drawn as if it
// were an answer. Each invalid case is a shape from the QA review.

RuleTester.describe = describe;
RuleTester.it = it;
RuleTester.itOnly = it.only;

const tester = new RuleTester({
  languageOptions: { parser, parserOptions: { ecmaFeatures: { jsx: true } } },
});

const OPTS = `{ queryKey: ["k"], queryFn: list }`;
const unread = (code: string) => ({ code, errors: [{ messageId: "unread" as const }] });

tester.run("empty-needs-read", rule, {
  invalid: [
    // P1: the data destructured, its length tested.
    unread(`function P1() {
      const { data: vars } = useQuery(${OPTS});
      return vars?.length === 0 ? <Empty /> : <List vars={vars} />;
    }`),
    // H2: the member, tested in a return.
    unread(`function H2() {
      const q = useQuery(${OPTS});
      if (q.data?.length === 0) return <Empty />;
      return <List items={q.data} />;
    }`),
    // I5, W1: the skeleton that never ends.
    unread(`function I5() {
      const q = useQuery(${OPTS});
      if (!q.data) return <Skeleton />;
      return <List items={q.data} />;
    }`),
    unread(`function W1() {
      const { data } = useQuery(${OPTS});
      return !data ? <Skeleton /> : <List items={data} />;
    }`),
    // Derived through ??, then tested.
    unread(`function Derived() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`),
    // Derived through || and a destructured default.
    unread(`function Or() {
      const q = useQuery(${OPTS});
      const rows = q.data || [];
      return !rows.length && <Empty />;
    }`),
    unread(`function Default() {
      const { data: rows = [] } = useQuery(${OPTS});
      return rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`),
    // .filter-derived, and transitively through .map.
    unread(`function Filtered() {
      const q = useQuery(${OPTS});
      const owned = (q.data ?? []).filter((s) => !s.shared);
      return owned.length === 0 ? <Empty /> : <List items={owned} />;
    }`),
    unread(`function Transitive() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      const owned = rows.filter((s) => !s.shared);
      const names = owned.map((s) => s.name);
      return names.length > 0 ? <List items={names} /> : <Empty />;
    }`),
    // isLoading read, no failure.
    unread(`function LoadingOnly() {
      const q = useQuery(${OPTS});
      if (q.isLoading) return <Skeleton />;
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`),
    unread(`function LoadingOr() {
      const { data, isLoading } = useQuery(${OPTS});
      if (isLoading || !data) return <Skeleton />;
      return <List items={data} />;
    }`),
    // useInfiniteQuery, through .pages and .flatMap.
    unread(`function Infinite() {
      const q = useInfiniteQuery(${OPTS});
      const rows = q.data?.pages.flatMap((p) => p.data) ?? [];
      return rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`),
    // useQueries, by index.
    unread(`function Many() {
      const results = useQueries({ queries: ids.map((id) => ({ queryKey: [id], queryFn: list })) });
      return results[0].data?.length === 0 ? <Empty /> : null;
    }`),
    // H1: through a plain function. H3: the length defaulted, then compared.
    unread(`function H1() {
      const needs = useQuery(${OPTS});
      const items = inboxItems(needs.data ?? [], now);
      return items.length === 0 ? <Empty /> : <List items={items} />;
    }`),
    unread(`function H3() {
      const needs = useQuery(${OPTS});
      return (needs.data?.length ?? 0) > 0 && <Needs />;
    }`),
    // Through any other method.
    unread(`function Sorted() {
      const q = useQuery(${OPTS});
      const rows = (q.data ?? []).slice().sort(byName);
      return rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`),
    // The other tests of emptiness.
    unread(`function NotLength() {
      const q = useQuery(${OPTS});
      return !q.data?.length ? <Empty /> : <List items={q.data} />;
    }`),
    unread(`function Bare() {
      const { data } = useQuery(${OPTS});
      return data?.length ? <List items={data} /> : <Empty />;
    }`),
    unread(`function Greater() {
      const q = useQuery(${OPTS});
      return q.data?.length > 0 ? <List items={q.data} /> : <Empty />;
    }`),
    unread(`function Flipped() {
      const q = useQuery(${OPTS});
      return 0 === q.data?.length ? <Empty /> : <List items={q.data} />;
    }`),
    unread(`function Size() {
      const q = useQuery(${OPTS});
      const set = new Set(q.data);
      return set.size === 0 ? <Empty /> : <List items={set} />;
    }`),
    unread(`function Keys() {
      const { data } = useQuery(${OPTS});
      return Object.keys(data ?? {}).length === 0 ? <Empty /> : <List items={data} />;
    }`),
    unread(`function Inline() {
      const { data } = useQuery(${OPTS});
      return (data ?? []).length === 0 ? <Empty /> : <List items={data} />;
    }`),
    // A test that disables or hides on it counts too.
    unread(`function Disabled() {
      const q = useQuery(${OPTS});
      return <button disabled={q.data?.length === 0}>Save</button>;
    }`),
    // Absence of the data itself, deciding what renders.
    unread(`function Undefined() {
      const q = useQuery(${OPTS});
      if (q.data === undefined) return <Skeleton />;
      return <List items={q.data} />;
    }`),
    unread(`function LooseNull() {
      const { data } = useQuery(${OPTS});
      return data == null ? <Skeleton /> : <List items={data} />;
    }`),
    unread(`function AndSkeleton() {
      const q = useQuery(${OPTS});
      return <div>{!q.data && <Skeleton />}</div>;
    }`),
    unread(`function Blank() {
      const q = useQuery(${OPTS});
      if (!q.data) return null;
      return <List items={q.data} />;
    }`),
    unread(`function Positive() {
      const q = useQuery(${OPTS});
      return q.data ? <List items={q.data} /> : <Skeleton />;
    }`),
    // Through an alias of the data, and a memo.
    unread(`function Alias() {
      const q = useQuery(${OPTS});
      const project = q.data;
      if (!project) return <NotFound />;
      return <p>{project.name}</p>;
    }`),
    unread(`function Memo() {
      const log = useInfiniteQuery(${OPTS});
      const entries = useMemo(() => log.data?.pages.flatMap((p) => p.data) ?? [], [log.data]);
      return entries.length === 0 ? <Empty /> : <List items={entries} />;
    }`),
    // Inside a function nested in the component.
    unread(`function Nested() {
      const q = useQuery(${OPTS});
      const body = () => (q.data?.length === 0 ? <Empty /> : <List items={q.data} />);
      return body();
    }`),
    // Options that aren't a literal can't declare a way out.
    unread(`function Shared() {
      const { data } = useQuery(projectQuery(id));
      return data?.length === 0 ? <Empty /> : <List items={data} />;
    }`),
    // The way out must be a reason.
    unread(`function NoReason() {
      const q = useQuery({ queryKey: ["k"], queryFn: list, meta: { quietFailure: "" } });
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`),
    unread(`function Blank2() {
      const q = useQuery({ queryKey: ["k"], queryFn: list, meta: { quietFailure: "   " } });
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`),
    unread(`function NotString() {
      const q = useQuery({ queryKey: ["k"], queryFn: list, meta: { quietFailure: true } });
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`),
    // A query read, its neighbour tested: the read doesn't cover it.
    unread(`function Neighbour() {
      const a = useQuery(${OPTS});
      const b = useQuery(${OPTS});
      if (a.isError) return <Failed />;
      return b.data?.length === 0 ? <Empty /> : <List items={b.data} />;
    }`),
    // A printed count: the zero is the claim (H1's Needs you 0, I6's storefront 0).
    unread(`function Printed() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return <h2>Needs you <span>{rows.length}</span></h2>;
    }`),
    unread(`function PrintedDefault() {
      const q = useQuery(${OPTS});
      return <span>{q.data?.length ?? 0}</span>;
    }`),
    unread(`function Template() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return <p>{\`\${rows.length} open\`}</p>;
    }`),
    // An all-clear from every or some.
    unread(`function Every() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return rows.every((r) => r.ok) ? <AllClear /> : <List items={rows} />;
    }`),
    unread(`function NoneBad() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return !rows.some((r) => r.bad) && <AllClear />;
    }`),
    unread(`function EveryAssigned() {
      const q = useQuery(${OPTS});
      const ok = (q.data ?? []).every((r) => r.ok);
      return ok ? <AllClear /> : <List />;
    }`),
    // useFinishSetup: the count defaulted, assigned, compared later.
    unread(`function Assigned() {
      const q = useQuery(${OPTS});
      const n = q.data?.length ?? 0;
      return n > 1 ? <Team /> : <Invite />;
    }`),
    // Text drawn in place of the data.
    unread(`function TextFallback() {
      const { data } = useQuery(${OPTS});
      return <p>{data ? <X /> : "No services"}</p>;
    }`),
    unread(`function TemplateFallback() {
      const { data } = useQuery(${OPTS});
      return <p>{!data ? \`No services in \${env}\` : <X />}</p>;
    }`),
    unread(`function TextAnd() {
      const q = useQuery(${OPTS});
      return <p>{!q.data && "No services"}</p>;
    }`),
    // A total the API counted.
    unread(`function Total() {
      const { data } = useQuery(${OPTS});
      return data?.total === 0 ? <Empty /> : <List />;
    }`),
    // The empty list handed down: the child can't tell it from an answer.
    unread(`function HandedDown() {
      const { data, isPending } = useQuery(${OPTS});
      return isPending ? <S /> : <List rows={data ?? []} />;
    }`),
    // A failure read that doesn't guard the claim beside it.
    unread(`function Beside() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return <div>{q.isError && <Failed />}{rows.length === 0 && <Empty />}</div>;
    }`),
    unread(`function ReadElsewhere() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      if (rows.length === 0) return <Empty />;
      return q.isError ? <Failed /> : <List items={rows} />;
    }`),
    unread(`function ReadOutside() {
      const q = useQuery(${OPTS});
      return <div><Read query={q} what="the rows">{(d) => <List items={d} />}</Read>{(q.data ?? []).length === 0 && <Empty />}</div>;
    }`),
    unread(`function WrongBranch() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return <div>{q.isError ? <Failed /> : null}<p>{rows.length} open</p></div>;
    }`),
    // A statement that doesn't end the render doesn't guard what follows it.
    unread(`function Logged() {
      const q = useQuery(${OPTS});
      if (q.isError) log(q.error);
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`),
    // A query passed down is not a read of its failure.
    unread(`function NotHelper() {
      const q = useQuery(${OPTS});
      if (other(q)) return <Failed />;
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`),
    // PanelServerCard: told apart from a failure, not from a read still going.
    unread(`function PendingClaim() {
      const q = useQuery(${OPTS});
      const here = (q.data ?? []).filter((s) => s.here);
      return <p>{q.isError && !q.data ? "—" : here.length === 0 ? "Nothing" : "Some"}</p>;
    }`),
    // Passed to a component that isn't Read.
    unread(`function Other() {
      const q = useQuery(${OPTS});
      return q.data?.length === 0 ? <Empty /> : <Card query={q} />;
    }`),
  ],

  valid: [
    // Each shape, with the failure read.
    `function P1() {
      const { data: vars, isError } = useQuery(${OPTS});
      if (isError) return <Failed />;
      return vars?.length === 0 ? <Empty /> : <List vars={vars} />;
    }`,
    `function P1Error() {
      const { data: vars, error: varsError } = useQuery(${OPTS});
      if (varsError) return <Failed />;
      return vars?.length === 0 ? <Empty /> : <List vars={vars} />;
    }`,
    `function H2() {
      const q = useQuery(${OPTS});
      if (q.isError) return <Failed />;
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`,
    `function I5() {
      const q = useQuery(${OPTS});
      if (q.error) return <Failed />;
      if (!q.data) return <Skeleton />;
      return <List items={q.data} />;
    }`,
    `function IsLoadingError() {
      const q = useQuery(${OPTS});
      if (q.isLoadingError) return <Failed />;
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`,
    `function Status() {
      const { data, status } = useQuery(${OPTS});
      if (status === "error") return <Failed />;
      return data?.length === 0 ? <Empty /> : <List items={data} />;
    }`,
    `function Reason() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      if (q.isPending) return <Skeleton />;
      return q.failureReason ? <Failed /> : rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`,
    `function Derived() {
      const q = useQuery(${OPTS});
      const owned = (q.data ?? []).filter((s) => !s.shared);
      if (q.isError) return <Failed />;
      if (q.isPending) return <Skeleton />;
      return owned.length === 0 ? <Empty /> : <List items={owned} />;
    }`,
    `function Infinite() {
      const q = useInfiniteQuery(${OPTS});
      const rows = q.data?.pages.flatMap((p) => p.data) ?? [];
      if (q.isPending) return <Skeleton />;
      return q.isError ? <Failed /> : rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`,
    `function Many() {
      const results = useQueries({ queries: ids.map((id) => ({ queryKey: [id], queryFn: list })) });
      if (results.some((r) => r.isError)) return <Failed />;
      return results[0].data?.length === 0 ? <Empty /> : null;
    }`,
    // Read through an array of queries.
    `function Both() {
      const a = useQuery(${OPTS});
      const b = useQuery(${OPTS});
      if ([a, b].some((q) => q.isError)) return <Failed />;
      return a.data?.length === 0 && b.data?.length === 0 ? <Empty /> : null;
    }`,
    // Read after an alias.
    `function Aliased() {
      const q = useQuery(${OPTS});
      const { error } = q;
      if (error) return <Failed />;
      return q.data?.length === 0 ? <Empty /> : <List items={q.data} />;
    }`,
    // Guards, each where it stands in front of the claim.
    `function Guarded() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return q.isLoading ? <S /> : !q.isError && rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`,
    `function Inside() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return rows.length === 0 ? (q.isError || q.isLoading ? <Failed /> : <Empty />) : <List items={rows} />;
    }`,
    `function InsideRead() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return <Read query={q} what="the rows">{() => (rows.length === 0 ? <Empty /> : <List items={rows} />)}</Read>;
    }`,
    `function Helper() {
      const q = useQuery(${OPTS});
      if (readFailed(q)) return <Failed />;
      if (q.isPending) return <S />;
      return q.data.length === 0 ? <Empty /> : <List items={q.data} />;
    }`,
    `function HelperMany() {
      const a = useQuery(${OPTS});
      const b = useQuery(${OPTS});
      if ([a, b].some(readFailed)) return <Failed />;
      return a.isPending || b.isPending ? <S /> : a.data.length + b.data.length === 0 ? <Empty /> : <List />;
    }`,
    // Through a name: the name's uses are the guards.
    `function Named() {
      const q = useQuery(${OPTS});
      const failed = q.isError && q.data === undefined;
      const rows = q.data ?? [];
      if (q.isLoading) return <S />;
      return failed ? <Failed /> : rows.length === 0 ? <Empty /> : <List items={rows} />;
    }`,
    `function Destructured() {
      const { data, isError, isLoading } = useQuery(${OPTS});
      const rows = data ?? [];
      if (isLoading) return <S />;
      return <div>{isError ? <Failed /> : <p>{rows.length} open</p>}</div>;
    }`,
    // The child is told in the same tag.
    `function SameTag() {
      const q = useQuery(${OPTS});
      return <List rows={q.data ?? []} failed={q.isError} loading={q.isPending} />;
    }`,
    // No data, no claim: the unknown mark isn't an answer.
    `function Mark() {
      const q = useQuery(${OPTS});
      const here = (q.data ?? []).filter((s) => s.here);
      return <p>{!q.data ? "—" : here.length === 0 ? "Nothing" : "Some"}</p>;
    }`,
    `function MarkCount() {
      const q = useQuery(${OPTS});
      return <span>{q.data ? q.data.length : "—"}</span>;
    }`,
    `function PresentThenCount() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return q.data && <p>{rows.length} open</p>;
    }`,
    // Drawn only when there are some, with the failure read: it says nothing.
    `function BlankElsewhere() {
      const q = useQuery(${OPTS});
      const rows = q.data ?? [];
      return <div>{rows.length > 0 && <Count />}{q.isError && <Failed />}</div>;
    }`,
    // Passed to Read.
    `function ThroughRead() {
      const q = useQuery(${OPTS});
      return <Read query={q} what="the rows" isEmpty={(d) => d.length === 0} empty={<Empty />}>{(d) => <List items={d} />}</Read>;
    }`,
    `function ThroughReadTested() {
      const q = useQuery(${OPTS});
      return <div>{q.data?.length > 0 && <Count />}<Read query={q} what="the rows">{(d) => <List items={d} />}</Read></div>;
    }`,
    // Only passes the data down; no test of its own.
    `function PassesDown() {
      const { data } = useQuery(${OPTS});
      return <List items={data} />;
    }`,
    // The one way out.
    `function Quiet() {
      const q = useQuery({ queryKey: ["k"], queryFn: list, meta: { quietFailure: "the options of a filter menu: a failure leaves the menu shorter" } });
      return q.data?.length === 0 ? null : <Menu items={q.data} />;
    }`,
    `function QuietMany() {
      const results = useQueries({ queries: ids.map((id) => ({ queryKey: [id], queryFn: list, meta: { quietFailure: "a decoration" } })) });
      return results[0].data?.length === 0 ? null : <Badge />;
    }`,
    // Not a test of emptiness.
    `function Count() {
      const q = useQuery(${OPTS});
      return <p>{q.data?.length} rows</p>;
    }`,
    `function NotEmptiness() {
      const q = useQuery(${OPTS});
      return q.data?.length > 5 ? <More /> : <List items={q.data} />;
    }`,
    // Presence, with nothing drawn when absent: blank, not a claim.
    `function Present() {
      const q = useQuery(${OPTS});
      return <div>{q.data && <List items={q.data} />}</div>;
    }`,
    `function PresentNull() {
      const q = useQuery(${OPTS});
      return q.data ? <List items={q.data} /> : null;
    }`,
    // An absence test that doesn't decide what renders.
    `function Effect() {
      const q = useQuery(${OPTS});
      useEffect(() => { if (!q.data) return; save(q.data); }, [q.data]);
      return <p>ok</p>;
    }`,
    // A shadowing parameter isn't the query's data.
    `function Shadow() {
      const rows = useQuery(${OPTS});
      return lists.map((rows) => (rows.length === 0 ? <Empty /> : <List items={rows} />));
    }`,
    // Not a query at all.
    `function NotAQuery() {
      const { data } = useThing();
      return data?.length === 0 ? <Empty /> : <List items={data} />;
    }`,
    // Query state that isn't data.
    `function Fetching() {
      const q = useQuery(${OPTS});
      return !q.isFetching ? <Done /> : <Spinner />;
    }`,
  ],
});
