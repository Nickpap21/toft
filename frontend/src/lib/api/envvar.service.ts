import type { components } from "./schema";
import { fetchAPI, fetchVersioned, type Versioned } from "./client";

export { changedSinceRead } from "./client";

export type EnvVar = components["schemas"]["EnvVar"];

/** A variable's name: letters, digits and _, not starting with a digit (the
 *  backend's envvar.ValidKey). */
export const VALID_KEY = /^[A-Za-z_][A-Za-z0-9_]*$/;

export type EnvVarRow = Pick<EnvVar, "key" | "value" | "is_secret" | "masked">;

/** Whose variables and secrets: a service's own, or an environment's, which
 *  every service in the environment inherits on its next deploy (a service's
 *  own variable with the same key wins). */
export type VarScope = { kind: "service" | "environment"; id: string };

export const serviceScope = (id: string): VarScope => ({ kind: "service", id });
export const environmentScope = (id: string): VarScope => ({ kind: "environment", id });

/** The API path of a scope's owner. */
export const scopePath = (scope: VarScope) =>
    `/api/v1/${scope.kind === "service" ? "services" : "environments"}/${scope.id}`;

/** A scope's plain variables and the version of this read. */
export type VarList = Versioned<EnvVar[]>;

/** For a read that only shows the list: `select: varsOf`. */
export const varsOf = (list: VarList) => list.data;

export const envvarService = {
    list: (scope: VarScope) =>
        fetchVersioned<EnvVar[]>(`${scopePath(scope)}/env-vars`),

    /** Atomically replaces all plain variables of the scope, deleting those
     *  not sent; secrets are kept. version is the one the rows were read
     *  with: the API refuses a stale one (changedSinceRead). */
    bulkUpsert: (scope: VarScope, version: string, vars: EnvVarRow[]) => {
        const body: components["schemas"]["BulkUpsertInput"] = { vars, version };
        return fetchVersioned<EnvVar[]>(`${scopePath(scope)}/env-vars`, {
            method: "PUT",
            body: JSON.stringify(body),
        });
    },

    upsert: (scope: VarScope, input: EnvVarRow) =>
        fetchAPI<EnvVar>(`${scopePath(scope)}/env-vars`, {
            method: "POST",
            body: JSON.stringify(input),
        }),

    delete: (scope: VarScope, key: string) =>
        fetchAPI<void>(
            `${scopePath(scope)}/env-vars/${encodeURIComponent(key)}`,
            { method: "DELETE" }
        ),
};
