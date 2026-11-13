import { signInHref } from "@/lib/routes";
import { fullPageLoad } from "@/lib/full-page-load";

/** The API's origin, as the browser reaches it. */
/** The API's origin. Empty means the panel's own origin, where Traefik routes
 *  /api to the backend (D-62); NEXT_PUBLIC_API_URL names another, and local
 *  development defaults to the backend on :8080. */
export const BASE_URL =
    process.env.NEXT_PUBLIC_API_URL ?? (process.env.NODE_ENV === "development" ? "http://localhost:8080" : "");

/** The API's absolute origin, for links, snippets and WebSockets. Browser only. */
export function apiOrigin(): string {
    return BASE_URL || window.location.origin;
}
const TOKEN_KEY = "paas_token";
const ORG_KEY = "toft_org";
/** Set when a request found the tab's workspace gone; AuthProvider reads it. */
export const ORG_GONE_KEY = "toft_org_gone";

// ── Typed API error ───────────────────────────────────────────────────────────

export class ApiError extends Error {
    constructor(message: string, public readonly status: number, public readonly body: unknown) {
        super(message);
        this.name = 'ApiError';
    }
}

/** Why a read failed when the answer isn't the API's own: "toft" when Toft
 *  isn't answering (no answer at all, or a 502, 503 or 504 from the proxy in
 *  front of it, which isn't the API's JSON), "git" when the API says its git
 *  server isn't (503 git_unavailable). Null for every answer the API gave,
 *  a 404 or a 403 included: those are about the thing asked for. */
export function unreachable(err: unknown): "toft" | "git" | null {
    if (!(err instanceof ApiError)) return null;
    const body = err.body as { error?: unknown; code?: unknown; data?: { code?: unknown } } | null;
    if (err.status === 0 || ([502, 503, 504].includes(err.status) && !body?.error)) return "toft";
    return (body?.code ?? body?.data?.code) === "git_unavailable" ? "git" : null;
}

/** The API said the thing asked for doesn't exist (404): the one failed
 *  read a page may call "not found". */
export function notFound(err: unknown): boolean {
    return err instanceof ApiError && err.status === 404;
}

// ── Token storage (localStorage — persists across tabs and browser restarts) ──

function getToken(): string | null {
    if (typeof window === "undefined") return null;
    return localStorage.getItem(TOKEN_KEY);
}

export function saveToken(token: string) {
    localStorage.setItem(TOKEN_KEY, token);
    // Set a presence cookie readable by the Next.js proxy (src/proxy.ts; not httpOnly)
    if (typeof document !== "undefined") {
        document.cookie = "paas_auth=1; path=/; SameSite=Lax";
    }
}

export function clearToken() {
    localStorage.removeItem(TOKEN_KEY);
    if (typeof document !== "undefined") {
        document.cookie = "paas_auth=; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT";
    }
}

// ── The tab's workspace (D-52) ────────────────────────────────────────────────

/** The workspace this tab works in, sent with every request as X-Toft-Org so
 *  a switch in another tab or on another device never moves it. Kept in
 *  sessionStorage, which is per tab and survives a reload. Null until the
 *  tab has one: requests then work in the account's default workspace. */
export function tabOrg(): string | null {
    if (typeof window === "undefined") return null;
    return sessionStorage.getItem(ORG_KEY);
}

export function setTabOrg(orgId: string | null | undefined) {
    if (orgId) sessionStorage.setItem(ORG_KEY, orgId);
    else sessionStorage.removeItem(ORG_KEY);
}

function orgHeader(org: string | null): Record<string, string> {
    return org ? { "X-Toft-Org": org } : {};
}

const GONE_MESSAGE = "You are no longer a member of this workspace.";

/** Set once this page leaves for Home after its workspace was found gone.
 *  Its later requests are not sent: without the header they would act in
 *  the default workspace on a page that still shows the old one. */
let leaving = false;

/** The server refuses a workspace the user no longer belongs to (removed
 *  from it, or deleted) with 403 workspace_unavailable, and never swaps in
 *  another. The tab forgets it and opens Home with a full page load in the
 *  account's default workspace; AuthProvider says why. */
async function leaveIfWorkspaceGone(res: Response, org: string | null): Promise<void> {
    if (res.status !== 403 || !org) return;
    const body = (await res.clone().json().catch(() => null)) as { data?: { code?: string } } | null;
    if (body?.data?.code !== "workspace_unavailable") return;
    if (!leaving) {
        leaving = true;
        setTabOrg(null);
        sessionStorage.setItem(ORG_GONE_KEY, org);
        fullPageLoad("/dashboard");
    }
    throw new Error(GONE_MESSAGE);
}

// ── Password confirmation (D-53) ─────────────────────────────────────────────

const REAUTH_KEY = "toft_reauth";

/** The proof that this tab's session confirmed its password a moment ago.
 *  Host shells and host-key replacement need it; the server makes it last
 *  10 minutes. Kept per tab, in sessionStorage, as a deadline on this
 *  browser's clock. Null once fewer than 15 seconds are left. */
export function reauthProof(): string | null {
    if (typeof window === "undefined") return null;
    try {
        const p = JSON.parse(sessionStorage.getItem(REAUTH_KEY) ?? "null") as { token: string; until: number } | null;
        if (p && p.until - Date.now() > 15_000) return p.token;
    } catch {
        // malformed: no proof
    }
    return null;
}

export function saveReauth(token: string, expiresInSeconds: number) {
    sessionStorage.setItem(REAUTH_KEY, JSON.stringify({ token, until: Date.now() + expiresInSeconds * 1000 }));
}

export function forgetReauth() {
    sessionStorage.removeItem(REAUTH_KEY);
}

/** The X-Toft-Reauth header, for the requests that need it only. */
export function reauthHeader(): Record<string, string> {
    const proof = reauthProof();
    return proof ? { "X-Toft-Reauth": proof } : {};
}

// ── JWT expiry check (no library needed — JWT payload is plain base64 JSON) ──

export function isTokenExpired(token: string): boolean {
    try {
        const payload = JSON.parse(atob(token.split(".")[1]));
        // exp is in seconds; Date.now() is in ms
        return payload.exp * 1000 < Date.now();
    } catch {
        return true; // treat malformed tokens as expired
    }
}

// ── Core fetch helper ─────────────────────────────────────────────────────────

/** The request every API call makes: the token in the header, a proactive
 *  redirect when it has expired, and the sign-out on a 401. Returns the
 *  response for the caller to read; a non-2xx is not an error here. */
async function authedFetch(path: string, options: RequestInit = {}): Promise<Response> {
    if (leaving) throw new Error(GONE_MESSAGE);
    const token = getToken();
    const org = tabOrg();

    // Proactively redirect if the stored token is already expired —
    // avoids a round-trip that will only return 401.
    if (token && isTokenExpired(token)) {
        clearToken();
        if (typeof window !== "undefined") {
            sessionStorage.setItem("paas_session_expired", "1");
            window.location.href = signInHref(window.location.pathname + window.location.search);
        }
        throw new Error("Session expired. Please sign in again.");
    }

    const headers: HeadersInit = {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...orgHeader(org),
        ...options.headers,
    };

    const res = await fetch(`${BASE_URL}${path}`, { ...options, headers }).catch((err: unknown) => {
        if ((err as Error)?.name === "AbortError") throw err;
        // The request never got an answer: status 0, which unreachable() reads.
        throw new ApiError("Couldn’t reach Toft. Check your connection and try again.", 0, null);
    });

    // Server-side expiry / revocation — only applies to authenticated requests.
    // A 401 with no token attached (e.g. a failed login attempt) is a normal
    // "wrong credentials" error, not a session expiry, and must not force-navigate
    // away from the caller before it gets a chance to show the real message.
    // A 401 for a token replaced while the request was out (Sign out other
    // browsers revokes this browser's token too and answers its successor)
    // says nothing about the new one: the request goes again with it.
    if (res.status === 401 && token) {
        if (tokenReplaced(token)) return authedFetch(path, options);
        throw new Error(SESSION_EXPIRED);
    }
    if (token) await leaveIfWorkspaceGone(res, org);
    await forgetRefusedProof(res);
    return res;
}

const SESSION_EXPIRED = "Session expired. Please sign in again.";

/** A request with this token was answered 401. True when another token has
 *  replaced it, so the request goes again; otherwise the session is over:
 *  the token is cleared and the page leaves for sign-in. */
function tokenReplaced(token: string): boolean {
    const current = getToken();
    if (current && current !== token) return true;
    clearToken();
    if (typeof window !== "undefined") {
        sessionStorage.setItem("paas_session_expired", "1");
        window.location.href = signInHref(window.location.pathname + window.location.search);
    }
    return false;
}

/** A 403 reauth_required means the proof this tab holds no longer counts
 *  (expired, or the server restarted with another secret): drop it, so the
 *  next attempt asks for the password again. */
async function forgetRefusedProof(res: Response): Promise<void> {
    if (res.status !== 403) return;
    const body = (await res.clone().json().catch(() => null)) as { data?: { code?: string } } | null;
    if (body?.data?.code === "reauth_required") forgetReauth();
}

export async function fetchAPI<T>(
    path: string,
    options: RequestInit = {}
): Promise<T> {
    const res = await authedFetch(path, options);

    if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}));
        const bodyObj = body as Record<string, unknown>;
        const message = (bodyObj?.error ?? bodyObj?.message ?? `Request failed: ${res.status}`) as string;
        throw new ApiError(message, res.status, body);
    }

    // Handle empty bodies (e.g. 204 No Content)
    const text = await res.text();
    if (!text) return undefined as T;

    const body = JSON.parse(text);
    // Unwrap standard envelope { data: T } — fall back to raw body if not enveloped.
    return (body?.data !== undefined ? body.data : body) as T;
}

/** One page of a list that can grow (D-59): the rows and, unless this is
 *  the last page, the cursor to send as ?cursor= for the next one. */
export interface Page<T> {
    data: T;
    next?: string;
}

/** fetchAPI for a paged list: keeps the envelope's `next`. */
export async function fetchPage<T>(path: string, options: RequestInit = {}): Promise<Page<T>> {
    const res = await authedFetch(path, options);
    if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}));
        const bodyObj = body as Record<string, unknown>;
        const message = (bodyObj?.error ?? bodyObj?.message ?? `Request failed: ${res.status}`) as string;
        throw new ApiError(message, res.status, body);
    }
    const body = (await res.json()) as Page<T>;
    return { data: body.data, next: body.next };
}

/** A list a client can replace: the rows and the version of this read, which
 *  the PUT must send back. A read that failed has none to send. */
export interface Versioned<T> {
    data: T;
    version: string;
}

/** A replace refused because the list changed since it was read: nothing was
 *  saved. */
export const changedSinceRead = (err: unknown) =>
    err instanceof ApiError && (err.body as { code?: unknown } | null)?.code === "changed_since_read";

/** fetchAPI for a replaceable list: keeps the envelope's `version`. */
export async function fetchVersioned<T>(path: string, options: RequestInit = {}): Promise<Versioned<T>> {
    const res = await authedFetch(path, options);
    if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}));
        const bodyObj = body as Record<string, unknown>;
        const message = (bodyObj?.error ?? bodyObj?.message ?? `Request failed: ${res.status}`) as string;
        throw new ApiError(message, res.status, body);
    }
    const body = (await res.json()) as Versioned<T>;
    return { data: body.data, version: body.version };
}

/** A text answer (a file to download) with the same token and 401 handling. */
export async function fetchText(path: string, options: RequestInit = {}): Promise<string> {
    const res = await authedFetch(path, options);
    if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}));
        const message = ((body as Record<string, unknown>)?.error ?? `Request failed: ${res.status}`) as string;
        throw new ApiError(message, res.status, body);
    }
    return res.text();
}

/** A file's bytes (a repository's raw file) with the same token and 401
 *  handling, as a Blob of the type the server gave them. */
export async function fetchBlob(path: string, options: RequestInit = {}): Promise<Blob> {
    const res = await authedFetch(path, options);
    if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}));
        const message = ((body as Record<string, unknown>)?.error ?? `Request failed: ${res.status}`) as string;
        throw new ApiError(message, res.status, body);
    }
    return res.blob();
}

// ── Server-sent events ────────────────────────────────────────────────────────

/**
 * Reads a server-sent event stream with fetch, so the token travels in the
 * Authorization header and never in the URL (EventSource cannot send headers).
 * Calls onEvent for each event, with type "message" when the event names none;
 * comments, ids and retry fields are ignored. Resolves when the server ends the
 * stream; rejects with an ApiError for a non-2xx answer, and with the abort
 * error once signal is aborted. No event is delivered after an abort.
 */
export async function streamEvents(
    path: string,
    onEvent: (type: string, data: string) => void,
    signal: AbortSignal,
    init: { method?: string; body?: string; headers?: Record<string, string> } = {}
): Promise<void> {
    if (leaving) throw new Error(GONE_MESSAGE);
    const token = getToken();
    const org = tabOrg();
    const res = await fetch(`${BASE_URL}${path}`, {
        method: init.method ?? "GET",
        body: init.body,
        headers: {
            ...(init.body !== undefined ? { "Content-Type": "application/json" } : {}),
            ...(token ? { Authorization: `Bearer ${token}` } : {}),
            ...orgHeader(org),
            ...init.headers,
        },
        signal,
    });
    if (res.status === 401 && token) {
        if (tokenReplaced(token)) return streamEvents(path, onEvent, signal, init);
        throw new ApiError(SESSION_EXPIRED, 401, null);
    }
    if (token) await leaveIfWorkspaceGone(res, org);
    await forgetRefusedProof(res);
    if (!res.ok || !res.body) {
        const body: unknown = await res.json().catch(() => ({}));
        const message = (body as { error?: string })?.error ?? `Request failed: ${res.status}`;
        throw new ApiError(message, res.status, body);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    let type = "";
    let data: string[] = [];
    while (true) {
        const { done, value } = await reader.read();
        if (done) return;
        buf += decoder.decode(value, { stream: true });
        const lines = buf.split("\n");
        buf = lines.pop() ?? "";
        for (const raw of lines) {
            const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
            if (line === "") {
                if (data.length > 0 && !signal.aborted) onEvent(type || "message", data.join("\n"));
                type = "";
                data = [];
            } else if (!line.startsWith(":")) {
                const colon = line.indexOf(":");
                const field = colon === -1 ? line : line.slice(0, colon);
                let value = colon === -1 ? "" : line.slice(colon + 1);
                if (value.startsWith(" ")) value = value.slice(1);
                if (field === "event") type = value;
                else if (field === "data") data.push(value);
            }
        }
    }
}
