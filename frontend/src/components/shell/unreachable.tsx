"use client";

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import Link from "next/link";
import { useQueryClient } from "@tanstack/react-query";
import { btnSm, outline } from "@/app/projects/_lib/ui";
import { Button } from "@/components/ui/button";
import { ApiError, notFound, unreachable } from "@/lib/api/client";

const COPY = {
  toft: {
    title: "Toft isn’t answering",
    body: "This page couldn’t read anything from Toft’s API, which isn’t a sign that anything was deleted. It happens while Toft restarts or upgrades, or while its backend is stopped; apps on your servers keep running. Retry once it’s back.",
  },
  git: {
    title: "Toft can’t read this repository right now",
    body: "Toft answered, but its git server didn’t, so this repository’s files, commits and pull requests can’t be shown. That isn’t a sign that anything was lost. It happens while the git server restarts. Retry once it’s back.",
  },
  refused: {
    title: "You don’t have access to this",
    body: "Toft refused this read for your account. Ask an owner of this workspace for access.",
  },
  gone: {
    title: "This no longer exists",
    body: "Toft has no record of it. It may have been deleted, or the link may be out of date.",
  },
};

/** The API's own answer that a Retry won't change: refused, or not there. */
const answered = (error: unknown) => (error instanceof ApiError && error.status === 403 ? "refused" : notFound(error) ? "gone" : null);

/** The read failed and has nothing to show. A failed read fetching again (a
 *  Retry, a poll) has dropped its error and looks like a first load:
 *  errorUpdateCount holds it, so the row and the focused Retry in it stay. */
export function readFailed(q: { data: unknown; isError: boolean; errorUpdateCount: number }) {
  return q.data === undefined && (q.isError || q.errorUpdateCount > 0);
}

type Reading = { data: unknown; error: unknown; errorUpdateCount: number; refetch: () => unknown };

/** Several reads behind one card, for Read: `data` is undefined until all of
 *  them have answered, and only a read with nothing to show counts as failed. */
export function allRead<T>(data: T | undefined, ...reads: Reading[]) {
  const missing = reads.filter((r) => r.data === undefined);
  return {
    data,
    error: missing.find((r) => r.error)?.error ?? null,
    errorUpdateCount: missing.reduce((n, r) => n + r.errorUpdateCount, 0),
    refetch: () => Promise.all(reads.map((r) => r.refetch())),
  };
}

/** The one state for a page whose read failed: Toft not answering, its git
 *  server not answering, a refusal, a 404, or any other answer, with the
 *  API's message. Never "0" or a skeleton that never ends. Home's error
 *  banner, with what it means. In place of a page it is the page's h1;
 *  inside a page that has its own, an h2. A 403 or a 404 won't change on a
 *  Retry: it has none, and `back` is where to go instead. */
export function ReadFailed({ error, onRetry, heading: Heading = "h1", focus = false, back }: { error: unknown; onRetry: () => void; heading?: "h1" | "h2"; focus?: boolean; back?: { href: string; label: string } }) {
  // In place of the page, focus goes to its heading: it was inside the page.
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (focus) heading.current?.focus();
  }, [focus]);
  const answer = answered(error);
  const kind = unreachable(error) ?? answer;
  // A 4xx's message is written for the person; a 5xx's is internal (X4).
  const said = error instanceof ApiError && error.status < 500 ? `Toft answered “${error.message}”.` : "Toft couldn’t read this.";
  const { title, body } = kind
    ? COPY[kind]
    : {
        title: "Couldn’t load this page",
        body: `${said} Retry; if it keeps failing, the backend’s log has the cause.`,
      };
  return (
    <section role="alert" data-read-state="failed" className="flex items-start justify-between gap-4 rounded-lg bg-status-failed-soft px-4 py-3 text-sm text-status-failed-fg">
      <div className="flex max-w-[640px] flex-col gap-1">
        <Heading ref={heading} tabIndex={-1} className="font-semibold">{title}</Heading>
        <p>{body}</p>
      </div>
      {!answer ? (
        <Button variant="outline" size="sm" onClick={onRetry} className="shrink-0">
          Retry
        </Button>
      ) : back ? (
        <Link href={back.href} className={`${outline} shrink-0`}>
          Back to {back.label}
        </Link>
      ) : null}
    </section>
  );
}

/** A part of the page whose read failed: one line, with Retry (the Issues
 *  page's "Couldn’t load <name>." row). With a 403 or a 404 it has no Retry:
 *  that answer won't change. */
export function LoadFailed({ what, onRetry, error }: { what: string; onRetry: () => void; error?: unknown }) {
  const answer = answered(error);
  return (
    <div data-read-state="failed" className="flex items-center justify-between gap-3 text-[13px] text-status-failed">
      <span role="alert">{answer === "refused" ? `You don’t have access to ${what}.` : answer === "gone" ? `Toft has no record of ${what}.` : `Couldn’t load ${what}.`}</span>
      {!answer && (
        <button type="button" onClick={onRetry} aria-label={`Retry loading ${what}`} className={btnSm}>
          Retry
        </button>
      )}
    </div>
  );
}

/** A further page of a list that wouldn't load: the rows stay, and this says
 *  so under them. It stays while its Retry fetches again. */
export function NextPageFailed({ query, what, className }: { query: { isFetchNextPageError: boolean; fetchNextPage: () => unknown }; what: string; className?: string }) {
  if (!query.isFetchNextPageError) return null;
  return (
    <div className={className}>
      <LoadFailed what={what} onRetry={() => void query.fetchNextPage()} error={(query as { error?: unknown }).error} />
    </div>
  );
}

/** One read's states, so a failed read is never drawn as an answer: the
 *  loading slot (or a named status), a failure (LoadFailed, or ReadFailed
 *  with `heading`), `empty` when `isEmpty` says so, else the children. The
 *  branch is on the data, not isError: a failed refetch sets isError but
 *  leaves the data on screen, and the data is still the answer. */
export function Read<T>({ query, what, heading, focus, loading, isEmpty, empty, children }: {
  query: { data: T | undefined; error: unknown; refetch: () => unknown; errorUpdateCount?: number };
  what: string;
  heading?: "h1" | "h2";
  focus?: boolean;
  loading?: React.ReactNode;
  isEmpty?: (data: T) => boolean;
  empty?: React.ReactNode;
  children: (data: T) => React.ReactNode;
}) {
  const { data, error } = query;
  // A failed read fetching again has dropped its error: hold it until it
  // settles, or a poll swaps the row for a skeleton and re-announces it.
  const [last, setLast] = useState<unknown>(null);
  if (error && error !== last) setLast(error);
  const failure = error ?? (query.errorUpdateCount ? last : null);
  if (data === undefined) {
    if (failure) {
      const retry = () => void query.refetch();
      return heading ? <ReadFailed heading={heading} focus={focus} error={failure} onRetry={retry} /> : <LoadFailed what={what} onRetry={retry} error={failure} />;
    }
    if (loading == null) return <div role="status" aria-label={`Loading ${what}`} data-read-state="loading" className="h-8 rounded-md bg-raised motion-safe:animate-pulse" />;
    return <div data-read-state="loading" className="contents">{loading}</div>;
  }
  if (isEmpty?.(data)) return <div data-read-state="empty" className="contents">{empty}</div>;
  return children(data);
}

/** The page in the shell's <main>, or ReadFailed in its place while a read
 *  on screen failed because Toft, or its git server, isn't answering. The
 *  page stays mounted, hidden, so its reads stay active: Retry runs them
 *  again, and the page is back as soon as they answer. */
export function ReachablePage({ children }: { children: React.ReactNode }) {
  const client = useQueryClient();
  const cache = client.getQueryCache();
  const held = useRef<unknown>(null);
  // The failing read's error: the same object until a fetch settles again.
  // Only reads with nothing to show count: a background poll failing under
  // loaded data leaves the page up.
  const down = useSyncExternalStore(
    useCallback((onChange: () => void) => cache.subscribe(onChange), [cache]),
    () => {
      const active = cache.findAll({ type: "active" }).filter((q) => q.state.data === undefined);
      // A failed read fetching again has dropped its error: hold the state
      // until it settles, rather than the page's skeleton in between.
      if (active.some((q) => q.state.errorUpdateCount > 0 && q.state.fetchStatus === "fetching")) return held.current;
      const failed = active.filter((q) => q.state.status === "error");
      held.current = null;
      for (const kind of ["toft", "git"] as const) {
        const q = failed.find((f) => unreachable(f.state.error) === kind);
        if (q) {
          held.current = q.state.error;
          break;
        }
      }
      return held.current;
    },
    () => null,
  );
  return (
    <>
      {down && <ReadFailed focus error={down} onRetry={() => void client.refetchQueries({ type: "active" })} />}
      <div hidden={!!down} className={down ? undefined : "contents"}>
        {children}
      </div>
    </>
  );
}
