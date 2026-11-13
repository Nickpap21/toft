"use client";

import { useState, useSyncExternalStore } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Pause, TriangleAlert } from "lucide-react";
import { ApiError, fetchAPI } from "@/lib/api/client";
import { agentService, type AgentClientView, type AgentPause } from "@/lib/api/agents.service";
import type { Project } from "@/lib/api/project.service";
import { useAuth } from "@/context/AuthContext";
import { signInHref } from "@/lib/routes";
import { cn } from "@/lib/utils";
import { Notice, useInstanceHost } from "@/components/shell/auth-shell";
import { Read } from "@/components/shell/unreachable";
import { ToftWordmark } from "@/components/shell/toft-mark";
import { ABILITIES, abilityDefault, consentInput, isLocal, listNames, possessive, roleLabel, sentence, until, type Role, type Scope } from "./view";

// An agent's consent screen (board AgentConsent, D-100). The agent's OAuth
// client sends the person here with its authorization request; the page
// shows who asks and lets them choose a workspace, its projects and what the
// agent may do, then answers Allow or Deny and sends the browser back to the
// client. A request that doesn't check out never redirects anywhere.

const noop = () => () => {};
const mono = "font-mono text-xs text-fg";
const focus = "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link";
const section = "m-0 flex flex-col border-0 border-t border-subtle px-5 py-4";
const legend = "float-left mb-1 p-0 text-[13px] font-semibold";
const secondary = cn("inline-flex items-center rounded-lg border border-subtle bg-raised text-[13px] font-medium text-fg hover:bg-raised/70", focus);

type Connected = { workspace: string; projects: string[] | null };

export default function AuthorizePage() {
  const search = useSyncExternalStore(noop, () => window.location.search.replace(/^\?/, ""), () => null);
  const host = useInstanceHost();
  const { user, logout } = useAuth();
  const [connected, setConnected] = useState<Connected | null>(null);
  const request = useQuery({
    queryKey: ["oauth-authorize-request", search],
    queryFn: () => agentService.authorizeRequest(search ?? ""),
    enabled: search !== null,
    retry: false,
    staleTime: Infinity,
  });
  const me = user?.name || user?.email || "";
  const view = request.data;

  return (
    <div className="flex min-h-dvh justify-center bg-ground px-4 pb-14 pt-12 text-sm leading-5 text-fg">
      <main className="flex w-full max-w-[560px] flex-col gap-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className="inline-flex items-center gap-2.5">
            <ToftWordmark className="h-5 w-auto" />
            <span className="font-mono text-xs text-muted">{host}</span>
          </span>
          {me && (
            <span className="text-xs text-muted">
              Signed in as {me} ·{" "}
              <button
                type="button"
                onClick={() => logout(signInHref(window.location.pathname + window.location.search))}
                className={cn("inline-link", focus)}
              >
                Not you?
              </button>
            </span>
          )}
        </div>

        {request.isError ? (
          <StateCard tone="warn" title={request.error instanceof ApiError && request.error.status === 400 ? "This sign-in link doesn’t work" : "Toft couldn’t check this link"}>
            {request.error instanceof ApiError && request.error.status < 500
              ? `${sentence(request.error.message)} Start connecting again from the agent (in Claude Code, run /mcp and choose Authenticate). Nothing was shared.`
              : "The panel didn’t answer. Reload the page to try again. Nothing was shared."}
          </StateCard>
        ) : connected && view ? (
          <StateCard
            tone="done"
            title={`${view.name} is connected`}
            action={
              <a href="/settings/agents" className={cn(secondary, "h-8 w-fit px-3")}>
                Open Settings → Agents
              </a>
            }
          >
            You can close this tab and go back to your terminal. It reaches{" "}
            {connected.projects ? listNames(connected.projects) : "every project"} in {connected.workspace} until {until(new Date())}.
          </StateCard>
        ) : view && search !== null && user ? (
          <ConsentForm view={view} query={new URLSearchParams(search)} me={me} orgs={user.orgs ?? []} current={user.orgId} onConnected={setConnected} />
        ) : (
          <div aria-label="Loading" className="h-[640px] rounded-xl border border-subtle bg-surface motion-safe:animate-pulse" />
        )}
      </main>
    </div>
  );
}

const TONE = {
  warn: { Icon: TriangleAlert, box: "border-status-pending/40", icon: "text-status-pending" },
  done: { Icon: Check, box: "border-status-running/35", icon: "text-status-running" },
  paused: { Icon: Pause, box: "border-status-failed/40", icon: "text-status-failed" },
} as const;

/** The board's state cards: an icon, a title and what happens now. */
function StateCard({ tone, title, small, action, children }: {
  tone: keyof typeof TONE;
  title: string;
  small?: boolean;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  const { Icon, box, icon } = TONE[tone];
  const H = small ? "h2" : "h1";
  return (
    <section role={tone === "warn" ? "alert" : "status"} className={cn("flex flex-col gap-2.5 rounded-xl border bg-surface p-4", box, small && "rounded-lg p-3")}>
      <span className="flex items-center gap-2">
        <Icon aria-hidden="true" className={cn("h-4 w-4 flex-none", icon)} strokeWidth={2} />
        <H className={cn("m-0 font-semibold", small ? "text-[13px] leading-[19px]" : "text-[15px] leading-[22px]")}>{title}</H>
      </span>
      <p className="m-0 text-[13px] leading-[19px] text-fg/80 [overflow-wrap:anywhere]">{children}</p>
      {action}
    </section>
  );
}

function ConsentForm({ view, query, me, orgs, current, onConnected }: {
  view: AgentClientView;
  query: URLSearchParams;
  me: string;
  orgs: { org_id: string; name: string; role: Role }[];
  current: string | undefined;
  onConnected: (c: Connected) => void;
}) {
  const [orgId, setOrgId] = useState(orgs.some((o) => o.org_id === current) ? current : orgs[0]?.org_id);
  const [allProjects, setAllProjects] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  // The person's own choices; an ability they haven't touched follows its default.
  const [choices, setChoices] = useState<Partial<Record<Scope, boolean>>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const org = orgs.find((o) => o.org_id === orgId);
  const ws = org?.name ?? "";
  const viewer = org?.role === "viewer";
  // The server's scopes say which abilities this panel offers.
  const abilities = ABILITIES.filter((ab) => view.scopes.includes(ab.scope));
  const on = (sc: Scope) => !viewer && (choices[sc] ?? abilityDefault(sc, view.requested, org?.role));
  // Another workspace than the tab's: the header names it, as the API reads it.
  const asOrg = orgId ? { headers: { "X-Toft-Org": orgId } } : {};
  const projects = useQuery({
    queryKey: ["oauth-authorize-projects", orgId],
    queryFn: () => fetchAPI<Project[]>("/api/v1/projects", asOrg),
    enabled: !!orgId,
  });
  const pause = useQuery({
    queryKey: ["oauth-authorize-pause", orgId],
    queryFn: () => fetchAPI<AgentPause>("/api/v1/agents/pause", asOrg),
    enabled: !!orgId,
  });
  const list = projects.data ?? [];
  const paused = pause.data?.instance ?? pause.data?.workspace;
  const local = isLocal(view.redirect_host);

  const choose = (id: string) => {
    setOrgId(id);
    setPicked([]);
    setChoices({});
    setError(null);
  };

  const answer = async (allow: boolean) => {
    if (allow && !allProjects && picked.length === 0) {
      setError("Choose at least one project, or every project.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { redirect_to } = await agentService.authorize(
        consentInput(query, { allow, orgId, allProjects, projectIds: picked, scopes: abilities.map((ab) => ab.scope).filter(on) }),
      );
      if (allow) onConnected({ workspace: ws, projects: allProjects ? null : list.filter((p) => picked.includes(p.id)).map((p) => p.name) });
      window.location.assign(redirect_to);
    } catch (err) {
      setError(err instanceof ApiError && err.status < 500 ? sentence(err.message) : "Couldn’t reach Toft. Try again.");
      setBusy(false);
    }
  };

  return (
    <form
      aria-labelledby="consent-h"
      onSubmit={(e) => {
        e.preventDefault();
        void answer(true);
      }}
      className="m-0 flex flex-col overflow-hidden rounded-xl border border-subtle bg-surface"
    >
      <div className="flex flex-col gap-3 px-5 pb-4 pt-5">
        <h1 id="consent-h" className="m-0 text-xl font-semibold leading-7 tracking-[-0.01em] [overflow-wrap:anywhere]">
          {view.name} wants to work in Toft as you
        </h1>
        <dl className="m-0 grid grid-cols-1 gap-x-3 gap-y-1.5 text-[13px] leading-[19px] sm:grid-cols-[112px_1fr]">
          <dt className="text-muted">Asking</dt>
          <dd className="m-0 text-fg/80 [overflow-wrap:anywhere]">
            <span className="font-medium text-fg">{view.name}</span>
            {view.kind === "cimd" ? (
              <>
                , from <span className={mono}>{view.verified}</span> (verified by its address).
              </>
            ) : (
              ", the name it gave itself when it registered. Toft can’t check that name."
            )}
          </dd>
          <dt className="text-muted">Returns to</dt>
          <dd className="m-0 text-fg/80 [overflow-wrap:anywhere]">
            {local ? (
              <>
                <span className={mono}>{view.redirect_host}</span>, a program on this computer. If you didn’t just run{" "}
                <span className="font-mono text-xs">/mcp</span>, deny.
              </>
            ) : (
              <>
                A program at <span className={mono}>{view.redirect_host}</span>. If you didn’t just start connecting it, deny.
              </>
            )}
          </dd>
        </dl>
      </div>

      <fieldset className={cn(section, "gap-2")}>
        <legend className={legend}>Workspace</legend>
        {orgs.map((o) => {
          const on = o.org_id === orgId;
          return (
            <label
              key={o.org_id}
              className={cn(
                "flex cursor-pointer items-center gap-2.5 rounded-lg border px-3 py-[9px]",
                on ? "border-link/55 bg-accent/10" : "border-subtle",
              )}
            >
              <input type="radio" name="ws" checked={on} onChange={() => choose(o.org_id)} className="m-0 accent-accent" />
              <span className="min-w-0 flex-1 font-medium [overflow-wrap:anywhere]">{o.name}</span>
              <span className="text-xs text-muted">{roleLabel(o.role)}</span>
            </label>
          );
        })}
        <span className="text-xs text-muted">
          {orgs.length > 0 ? "One workspace per connection. Connect again for another." : "You aren’t in a workspace, so there is nothing to connect it to."}
        </span>
      </fieldset>

      {org && (
        <fieldset className={cn(section, "gap-2")}>
          <legend className={legend}>Projects</legend>
          <label className="flex cursor-pointer items-center gap-2.5 text-[13px]">
            <input type="radio" name="pr" checked={allProjects} onChange={() => setAllProjects(true)} className="m-0 accent-accent" />
            Every project in {ws}, new ones included
          </label>
          <label className="flex cursor-pointer items-center gap-2.5 text-[13px]">
            <input type="radio" name="pr" checked={!allProjects} onChange={() => setAllProjects(false)} className="m-0 accent-accent" />
            Only these
          </label>
          <div className="flex flex-wrap gap-1.5 pl-[26px]">
            <Read
              query={projects}
              what={`${possessive(ws)} projects`}
              loading={<span role="status" aria-label="Loading projects" className="h-[30px] w-60 rounded-md bg-raised motion-safe:animate-pulse" />}
              isEmpty={(d) => d.length === 0}
              empty={<span className="text-xs text-muted">{ws} has no projects yet. Choose every project to include the ones you add.</span>}
            >
              {(d) => d.map((p) => {
                const on = picked.includes(p.id);
                return (
                  <label
                    key={p.id}
                    className={cn(
                      "inline-flex h-[30px] cursor-pointer items-center gap-1.5 rounded-md border px-2.5 text-[13px]",
                      on && !allProjects ? "border-link/55 bg-accent/10" : "border-subtle",
                      allProjects && "cursor-default opacity-60",
                    )}
                  >
                    <input
                      type="checkbox"
                      checked={on}
                      disabled={allProjects}
                      onChange={() => setPicked(on ? picked.filter((x) => x !== p.id) : [...picked, p.id])}
                      className="m-0 accent-accent"
                    />
                    {p.name}
                  </label>
                );
              })}
            </Read>
          </div>
        </fieldset>
      )}

      {org && (
        <fieldset className={cn(section, "gap-0.5")}>
          <legend className={cn(legend, "mb-1.5")}>What it may do</legend>
          {viewer && (
            <p className="m-0 mb-1 text-xs leading-[17px] text-fg/70">
              You’re a viewer in {ws}, so {view.name} can only read there. An owner can change your role; connect again afterwards.
            </p>
          )}
          <Ability name="Read" hint="Projects, services, deploy logs, capacity, docs, issues and diffs. Always on." checked locked />
          {abilities.map((ab) => (
            <Ability
              key={ab.scope}
              name={ab.name}
              hint={viewer ? ab.viewer : ab.hint}
              checked={on(ab.scope)}
              locked={viewer}
              dim={viewer}
              onChange={(v) => setChoices((c) => ({ ...c, [ab.scope]: v }))}
            />
          ))}
        </fieldset>
      )}

      {org && (
        <div className="flex flex-col gap-1.5 border-t border-subtle bg-raised/40 px-5 py-3.5 text-[13px] leading-[18px] text-fg/70">
          <span>
            It acts as {me}, never with more than your role ({roleLabel(org.role)}). {possessive(ws)} agent policy still applies: it decides which
            deploys wait for a person, and some actions are never allowed.
          </span>
          <span>It never reads a secret, changes a role, deletes the workspace or removes a server.</span>
          <span>Access lasts 90 days. You or an owner can revoke it at any time in Settings → Agents.</span>
        </div>
      )}

      {(paused || error) && (
        <div className="flex flex-col gap-2 border-t border-subtle px-5 pt-3.5">
          {paused && (
            <StateCard tone="paused" small title={pause.data?.instance ? "Agents are paused on this panel" : `Agents are paused in ${ws}`}>
              {paused.by} paused every agent {pausedAt(paused.at)}. You can still connect, but every call is refused until{" "}
              {pause.data?.instance ? "an operator resumes" : "an owner resumes"} them.
            </StateCard>
          )}
          {error && <Notice kind="error">{error}</Notice>}
        </div>
      )}

      <div className={cn("flex flex-wrap justify-end gap-2 px-5 py-3.5", !(paused || error) && "border-t border-subtle")}>
        <button type="button" disabled={busy} onClick={() => void answer(false)} className={cn(secondary, "h-9 px-3.5 disabled:opacity-60")}>
          Deny
        </button>
        <button
          type="submit"
          disabled={busy || !org}
          className={cn("h-9 rounded-lg bg-accent px-4 text-[13px] font-medium text-on-accent hover:bg-accent/90 disabled:opacity-60", focus)}
        >
          Allow {view.name}
        </button>
      </div>
    </form>
  );
}

/** "at 13:42" today, else "on 2026-10-20 at 13:42". */
function pausedAt(at: string): string {
  const d = new Date(at);
  const time = d.toTimeString().slice(0, 5);
  return d.toDateString() === new Date().toDateString() ? `at ${time}` : `on ${d.toLocaleDateString("en-CA")} at ${time}`;
}

function Ability({ name, hint, checked, locked, dim, onChange }: {
  name: string;
  hint: string;
  checked: boolean;
  locked?: boolean;
  /** A viewer's ability: off, and the name muted as the reason says why. */
  dim?: boolean;
  onChange?: (checked: boolean) => void;
}) {
  return (
    <label className={cn("flex items-start gap-2.5 py-[7px]", locked ? "cursor-default" : "cursor-pointer")}>
      <input
        type="checkbox"
        checked={checked}
        disabled={locked}
        onChange={(e) => onChange?.(e.target.checked)}
        className="m-0 mt-[3px] accent-accent"
      />
      <span className="flex flex-col gap-px">
        <span className={cn("text-[13px]", dim ? "text-muted" : locked ? "text-fg/80" : "text-fg")}>{name}</span>
        <span className="text-xs leading-[17px] text-muted">{hint}</span>
      </span>
    </label>
  );
}
