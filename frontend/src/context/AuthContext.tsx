"use client";

import React, { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { fullPageLoad } from "@/lib/full-page-load";
import { toast } from "sonner";
import { ApiError, fetchAPI, saveToken, clearToken, isTokenExpired, ORG_GONE_KEY, saveReauth, setTabOrg, tabOrg } from "@/lib/api/client";
import { orgService, type OrgMembership } from "@/lib/api/org.service";
import { userService } from "@/lib/api/user.service";

export interface AuthUser {
    id: string;
    email: string;
    name?: string;
    /** Instance operator (D-21): can create workspaces and manage operators.
     *  Missing on paas_user objects stored before it existed; refreshed from
     *  /me on every page load. */
    is_operator?: boolean;
    /** The workspace this tab works in (D-52; the browser's copy holds the
     *  one new tabs open in). Undefined when the user belongs to no
     *  workspace, or while that is not known (loading the org context
     *  failed); `noWorkspace` tells them apart. */
    orgId?: string;
    orgName?: string;
    role?: string;
    orgs?: OrgMembership[];
    /** True only once GET /org has answered 403 for a non-operator: the user
     *  belongs to no workspace. Undefined means not known yet. */
    noWorkspace?: boolean;
}

/** Body of POST /api/v1/auth/register. Without invite_token it only succeeds
 *  on an empty instance (first run). */
export interface RegisterInput {
    name: string;
    email: string;
    password: string;
    org_name?: string;
    setup_token?: string;
    invite_token?: string;
}

/** Body of a successful login or register. */
interface SignIn {
    token: string;
    user: AuthUser;
    reauth: { token: string; expires_in: number };
}

interface AuthState {
    user: AuthUser | null;
    token: string | null;
    isLoading: boolean;
    login: (email: string, password: string) => Promise<void>;
    /** Creates the account and signs in with the session the server returns,
     *  so no second /auth call is spent. Throws only if registration failed. */
    register: (input: RegisterInput) => Promise<void>;
    /** Signs out and opens `to` (the sign-in page by default). */
    logout: (to?: string) => void;
    /** The server ended this session and answered its successor (a password
     *  change, Sign out other browsers): this browser goes on with it. */
    sessionRenewed: (token: string) => void;
    /** Makes orgId the active workspace and opens `to` in it (Home by
     *  default) with a full page load (fullPageLoad). */
    switchOrg: (orgId: string, to?: string) => Promise<void>;
    createOrg: (name: string) => Promise<void>;
    /** Deletes the active workspace (confirm is its name), then opens the
     *  workspace the server falls back to, or none. */
    deleteOrg: (confirm: string) => Promise<void>;
    /** Leaves the active workspace, then opens the one the server falls back
     *  to, or none. */
    leaveOrg: () => Promise<void>;
    /** The active workspace was renamed: the sidebar and switcher follow. */
    orgRenamed: (name: string) => void;
}

const AuthContext = createContext<AuthState | undefined>(undefined);

/** The browser's copy of the account (localStorage, shared by its tabs).
 *  Its workspace is the one new tabs open in: only a sign-in, a switch, a
 *  new workspace or a left one changes it. Each tab keeps its own
 *  (tabOrg), so the sidebar always names the workspace requests act in. */
const USER_KEY = "paas_user";


/** After the active workspace was deleted or left: asks the server which
 *  workspace is active now (it falls back to the user's earliest, or none)
 *  and opens Home in it. */
async function leaveActiveWorkspace() {
    const current = readLocal<AuthUser>(USER_KEY, true);
    if (!current) return;
    await loadDefaultWorkspace(current);
    fullPageLoad("/dashboard");
}

/** Loads the workspace the server holds as the account's default (the one
 *  it last switched to, or its fallback once that is gone) and makes it
 *  this tab's and the browser's. */
async function loadDefaultWorkspace(base: AuthUser): Promise<AuthUser> {
    setTabOrg(null);
    const next = await loadOrgContext({ ...base, orgId: undefined, orgName: undefined, role: undefined });
    setTabOrg(next.orgId);
    localStorage.setItem(USER_KEY, JSON.stringify(next));
    return next;
}

/** The browser's copy of the account as this tab sees it: in the tab's
 *  workspace, with its name and role from the copy's membership list. A
 *  workspace the list doesn't have yet (joined in another tab) shows no
 *  name until the page load's refresh fills it. */
function viewFor(stored: AuthUser, org: string | null): AuthUser {
    if (!org || org === stored.orgId) return stored;
    const m = stored.orgs?.find((o) => o.org_id === org);
    return { ...stored, orgId: org, orgName: m?.name, role: m?.role };
}

/** Writes what this tab learned about the account into the browser's copy,
 *  keeping the copy's workspace: a tab's own workspace never becomes the
 *  one new tabs open in. */
function storeAccount(tab: AuthUser) {
    const stored = readLocal<AuthUser>(USER_KEY, true);
    if (!stored || stored.id !== tab.id) return;
    const same = stored.orgId === tab.orgId;
    localStorage.setItem(USER_KEY, JSON.stringify({
        ...stored,
        is_operator: tab.is_operator,
        orgs: tab.orgs,
        ...(same ? { orgName: tab.orgName, role: tab.role } : {}),
    }));
}

/** Read a value from localStorage synchronously. Safe for SSR. */
function readLocal<T>(key: string, json = false): T | null {
    if (typeof window === "undefined") return null;
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    if (json) {
        try { return JSON.parse(raw) as T; } catch { return null; }
    }
    return raw as unknown as T;
}

/** Fetches the user's operator flag, active org and full org list and merges
 *  them onto the user object. Shared by login, switchOrg, and createOrg, since
 *  all three need the same "what's my current workspace now" refresh afterward.
 *  Only a missing org (403) is recovered here — any other failure (network,
 *  500, a bad switch/create response) propagates, since silently keeping stale
 *  org data after a real failure is worse than surfacing the error to the
 *  caller. */
async function loadOrgContext(baseUser: AuthUser): Promise<AuthUser> {
    // is_operator can change after login (granted or revoked elsewhere).
    // If /me fails, keep what we had; unknown means "not an operator".
    let isOperator = baseUser.is_operator === true;
    try {
        isOperator = (await userService.getMe()).is_operator === true;
    } catch {
        // fall through with the cached value
    }

    let orgData: { id: string; name: string };
    try {
        orgData = await fetchAPI<{ id: string; name: string }>("/api/v1/org", {});
    } catch (err) {
        if (!(err instanceof ApiError && err.status === 403)) {
            throw err;
        }
        // No workspace: removed from every workspace, or an account whose
        // workspace was never created. Only instance operators may create one
        // (D-21); everyone else gets a no-workspace state. Clear role so the
        // meaningless users.role ("admin") never shows as an org role.
        if (!isOperator) {
            return {
                ...baseUser,
                is_operator: false,
                orgId: undefined,
                orgName: undefined,
                role: undefined,
                orgs: [],
                noWorkspace: true,
            };
        }
        orgData = await orgService.createOrg(`${baseUser.name ?? baseUser.email}'s Workspace`);
    }
    const orgs = await orgService.listOrgs();
    const mine = orgs.find((o) => o.org_id === orgData.id);
    return {
        ...baseUser,
        is_operator: isOperator,
        orgId: orgData.id,
        orgName: orgData.name,
        role: mine?.role ?? "owner",
        orgs,
        noWorkspace: false,
    };
}

/** Loads the org context for a freshly signed-in user. If that fails (a
 *  transient 500 or network error), the user is kept with the org context
 *  unknown, not "no workspace": the dashboard keeps working because the server
 *  resolves the org itself, and the next page load retries. users.role
 *  ("admin" on every row) is dropped so it never shows as an org role. */
async function orgContextOrUnknown(baseUser: AuthUser): Promise<AuthUser> {
    try {
        return await loadOrgContext(baseUser);
    } catch {
        return { ...baseUser, role: undefined, orgId: undefined, noWorkspace: undefined };
    }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
    // Start with null so server and client first renders match (avoids hydration error #418).
    // After mount, read localStorage and populate real auth state.
    const [token, setToken] = useState<string | null>(null);
    const [user, setUser] = useState<AuthUser | null>(null);
    const [isMounted, setIsMounted] = useState(false);

    // Reading localStorage after mount is deliberate: the first client render
    // must match the server's, which has no storage.
    /* eslint-disable react-hooks/set-state-in-effect */
    // A tab without a workspace of its own (new, or just signed in elsewhere)
    // takes the browser's, unless a request just found that one gone.
    useEffect(() => {
        const stored = readLocal<AuthUser>(USER_KEY, true);
        if (stored?.orgId && !tabOrg() && stored.orgId !== sessionStorage.getItem(ORG_GONE_KEY)) {
            setTabOrg(stored.orgId);
        }
        setToken(readLocal<string>("paas_token"));
        setUser(stored && viewFor(stored, tabOrg()));
        setIsMounted(true);
    }, []);
    /* eslint-enable react-hooks/set-state-in-effect */

    // Once a token is known, refresh on every page load what may have changed
    // elsewhere: the operator flag (a grant or revoke), the tab workspace's
    // name and the role there, and the membership list. A tab with no
    // workspace (none, or loading it failed at sign-in), or whose workspace a
    // request found gone, loads the account's default instead, so a transient
    // failure doesn't stick until the next login. An expired token is left
    // alone so public pages (sign-up, invites) don't redirect a visitor.
    useEffect(() => {
        if (!token || isTokenExpired(token)) return;
        const stored = readLocal<AuthUser>(USER_KEY, true);
        if (!stored) return;
        let cancelled = false;
        const org = tabOrg();
        const gone = sessionStorage.getItem(ORG_GONE_KEY);

        const load = org && !gone
            ? loadOrgContext(viewFor(stored, org)).then((next) => {
                if (cancelled) return;
                storeAccount(next);
                setUser(next);
            })
            : loadDefaultWorkspace(stored).then((next) => {
                if (cancelled) return;
                setUser(next);
                if (gone) {
                    sessionStorage.removeItem(ORG_GONE_KEY);
                    const name = stored.orgs?.find((o) => o.org_id === gone)?.name ?? "That workspace";
                    toast(`You're no longer a member of ${name}`, {
                        description: next.orgName ? `Opened ${next.orgName}.` : undefined,
                    });
                }
            });
        load.catch(() => {
            // keep the cached view; the next page load tries again, and
            // fetchAPI already handles a 401
        });
        return () => { cancelled = true; };
    }, [token]);

    /** Stores a session the server issued (login or register) and loads the
     *  user's org context. Org info is optional here: a failure to load it
     *  must not fail the sign-in. The sign-in also counts as a password
     *  confirmation for a few minutes in this tab (D-161). */
    const establishSession = useCallback(async ({ token: sessionToken, user: baseUser, reauth }: SignIn) => {
        saveToken(sessionToken);
        saveReauth(reauth.token, reauth.expires_in);
        // A new session starts in the account's default workspace.
        setTabOrg(null);
        sessionStorage.removeItem(ORG_GONE_KEY);
        const enrichedUser = await orgContextOrUnknown(baseUser);
        setTabOrg(enrichedUser.orgId);
        localStorage.setItem(USER_KEY, JSON.stringify(enrichedUser));
        setToken(sessionToken);
        setUser(enrichedUser);
    }, []);

    const login = useCallback(async (email: string, password: string) => {
        const res = await fetchAPI<SignIn>("/api/v1/auth/login", {
            method: "POST",
            body: JSON.stringify({ email, password }),
        });
        await establishSession(res);
    }, [establishSession]);

    const register = useCallback(async (input: RegisterInput) => {
        const res = await fetchAPI<SignIn>("/api/v1/auth/register", {
            method: "POST",
            body: JSON.stringify(input),
        });
        await establishSession(res);
    }, [establishSession]);

    // Switching moves this tab and becomes the default for new tabs and the
    // account's other sessions; tabs already open stay where they are.
    const switchOrg = useCallback(async (orgId: string, to = "/dashboard") => {
        await orgService.switchOrg(orgId);
        const current = readLocal<AuthUser>(USER_KEY, true);
        if (!current) return;
        setTabOrg(orgId);
        // The server's default moved: this browser's follows at once, so a
        // tab closed before the context below loads leaves the two agreeing.
        localStorage.setItem(USER_KEY, JSON.stringify(viewFor(current, orgId)));
        const enrichedUser = await loadOrgContext(current);
        localStorage.setItem(USER_KEY, JSON.stringify(enrichedUser));
        fullPageLoad(to);
    }, []);

    const deleteOrg = useCallback(async (confirm: string) => {
        await orgService.deleteOrg(confirm);
        await leaveActiveWorkspace();
    }, []);

    const leaveOrg = useCallback(async () => {
        await orgService.leaveOrg();
        await leaveActiveWorkspace();
    }, []);

    const orgRenamed = useCallback((name: string) => {
        setUser((prev) => {
            if (!prev) return prev;
            const next = {
                ...prev,
                orgName: name,
                orgs: prev.orgs?.map((o) => (o.org_id === prev.orgId ? { ...o, name } : o)),
            };
            storeAccount(next);
            return next;
        });
    }, []);

    // The new workspace becomes the account's default; open it here.
    const createOrg = useCallback(async (name: string) => {
        const created = await orgService.createOrg(name);
        const current = readLocal<AuthUser>(USER_KEY, true);
        if (!current) return;
        // As for a switch: this browser's default follows the server's at once.
        localStorage.setItem(USER_KEY, JSON.stringify({ ...current, orgId: created.id, orgName: created.name, role: "owner" }));
        await loadDefaultWorkspace(current);
        fullPageLoad("/dashboard");
    }, []);

    const logout = useCallback((to = "/") => {
        // The token is read before clearToken runs, so the request carries it.
        const current = localStorage.getItem("paas_token");
        if (current && !isTokenExpired(current)) void userService.signOut().catch(() => {});
        clearToken();
        setTabOrg(null);
        localStorage.removeItem(USER_KEY);
        setToken(null);
        setUser(null);
        // Force full navigation to clear React Query cache and all in-memory state.
        if (typeof window !== "undefined") {
            window.location.href = to;
        }
    }, []);

    const sessionRenewed = useCallback((next: string) => {
        saveToken(next);
        setToken(next);
    }, []);

    const value = useMemo(
        () => ({ user, token, isLoading: !isMounted, login, register, logout, sessionRenewed, switchOrg, createOrg, deleteOrg, leaveOrg, orgRenamed }),
        [user, token, isMounted, login, register, logout, sessionRenewed, switchOrg, createOrg, deleteOrg, leaveOrg, orgRenamed]
    );

    return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
    const ctx = useContext(AuthContext);
    if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
    return ctx;
}

/** True when the signed-in user is an instance operator (D-21): only they can
 *  create workspaces or manage operators. Gate every operator-only affordance
 *  (workspace creation in the switcher, ⌘K actions, Settings → Instance) on
 *  this hook so the rule lives in one place. The server enforces it anyway. */
export function useIsOperator(): boolean {
    const { user } = useAuth();
    return user?.is_operator === true;
}

export function useOrgRole(): "owner" | "developer" | "viewer" | null {
    const { user } = useAuth();
    return (user?.role as "owner" | "developer" | "viewer") ?? null;
}
