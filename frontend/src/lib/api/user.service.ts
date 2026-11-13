import type { components } from "./schema";
import { fetchAPI } from "./client";
import type { ThemePref } from "@/lib/theme/themes";

export type UserProfile = components["schemas"]["Profile"];

export const userService = {
    getMe: () =>
        fetchAPI<UserProfile>("/api/v1/me"),

    updateProfile: (name: string) =>
        fetchAPI<UserProfile>("/api/v1/me", {
            method: "PUT",
            body: JSON.stringify({ name }),
        }),

    /** A password change signs out every other session of the account; the
     *  answer carries a fresh token for this one. */
    updatePassword: (current_password: string, new_password: string) =>
        fetchAPI<{ message: string; token?: string }>("/api/v1/me/password", {
            method: "PUT",
            body: JSON.stringify({ current_password, new_password }),
        }),

    /** Ends this session on the server: its token is refused from now on.
     *  keepalive: it outlives the navigation that follows a sign-out. */
    signOut: () => fetchAPI<void>("/api/v1/me/sessions/sign-out", { method: "POST", keepalive: true }),
    /** Ends every other session of the account; the answer carries a fresh
     *  token for this one. */
    signOutOthers: () =>
        fetchAPI<{ message: string; token?: string }>("/api/v1/me/sessions/sign-out-others", { method: "POST" }),

    /** Saves the theme to the account. */
    setTheme: (theme: ThemePref) => fetchAPI<void>("/api/v1/me/theme", { method: "PUT", body: JSON.stringify({ theme }) }),
};
