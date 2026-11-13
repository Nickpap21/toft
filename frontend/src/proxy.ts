import { NextRequest, NextResponse } from "next/server";
import { isPublicPath, signInHref } from "@/lib/routes";

/**
 * The Content-Security-Policy, enforced since a deploy showed no violation on
 * any page in report-only mode (2026-10-16). A nonce per request lets Next's own inline
 * scripts and the theme script run while any other inline script is
 * refused, which is what turns a stored javascript: URL or an injected tag
 * into a blocked click. Reading the nonce in the layout makes every route
 * render on request, which an admin panel can afford.
 */
function contentSecurityPolicy(nonce: string, host: string | null): string {
    const dev = process.env.NODE_ENV !== "production";
    // As BASE_URL in lib/api/client.ts. Same-origin (D-62): 'self' covers the
    // API, and the WebSocket is named, since older browsers don't match
    // wss: against 'self'.
    const api = process.env.NEXT_PUBLIC_API_URL ?? (process.env.NODE_ENV === "development" ? "http://localhost:8080" : "");
    const ws = api ? api.replace(/^http/, "ws") : host ? `wss://${host}` : "";
    return [
        "default-src 'self'",
        `script-src 'self' 'nonce-${nonce}' 'strict-dynamic'${dev ? " 'unsafe-eval'" : ""}`,
        "style-src 'self' 'unsafe-inline'", // Tailwind and Next write inline styles
        "img-src 'self' blob: data:",
        "font-src 'self'",
        ["connect-src 'self'", api, ws].filter(Boolean).join(" "),
        "frame-ancestors 'none'",
        "base-uri 'self'",
        "form-action 'self'",
        "object-src 'none'",
    ].join("; ");
}

export function proxy(request: NextRequest) {
    const { pathname } = request.nextUrl;
    const nonce = btoa(crypto.getRandomValues(new Uint8Array(16)).reduce((s, b) => s + String.fromCharCode(b), ""));
    const requestHeaders = new Headers(request.headers);
    requestHeaders.set("x-nonce", nonce);

    const secure = (res: NextResponse) => {
        res.headers.set("X-Robots-Tag", "noindex, nofollow");
        res.headers.set("Content-Security-Policy", contentSecurityPolicy(nonce, request.headers.get("host")));
        // No URL in a Referer to another site (a reset link's token is in one).
        res.headers.set("Referrer-Policy", "same-origin");
        res.headers.set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()");
        return res;
    };

    // Allow public paths through
    if (
        isPublicPath(pathname) ||
        pathname.startsWith("/_next") ||
        pathname.startsWith("/api")
    ) {
        return secure(NextResponse.next({ request: { headers: requestHeaders } }));
    }

    const authCookie = request.cookies.get("paas_auth");
    if (!authCookie) {
        return NextResponse.redirect(new URL(signInHref(pathname + request.nextUrl.search), request.url));
    }

    return secure(NextResponse.next({ request: { headers: requestHeaders } }));
}

export const config = {
    matcher: ["/((?!_next/static|_next/image|favicon.ico|icon\\.svg$|apple-icon\\.png$).*)"],
};
