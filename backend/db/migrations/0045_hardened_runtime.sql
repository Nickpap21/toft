-- The hardened Standard runtime and the capability probe (D-93, stage 5
-- session 1): a service's process limit, and what each server can run.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added (D-63): v0.11.0 runs on it and ignores it.

ALTER TABLE services ADD COLUMN IF NOT EXISTS pids_limit INTEGER CHECK (pids_limit BETWEEN 256 AND 32768);

CREATE TABLE IF NOT EXISTS server_capabilities (
    server_id      UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    kernel         TEXT        NOT NULL DEFAULT '' CHECK (char_length(kernel) <= 200),
    arch           TEXT        NOT NULL DEFAULT '' CHECK (char_length(arch) <= 40),
    cgroup_v2      BOOLEAN     NOT NULL DEFAULT false,
    virtualization TEXT        NOT NULL DEFAULT '' CHECK (char_length(virtualization) <= 40),
    kvm            BOOLEAN     NOT NULL DEFAULT false,
    docker_version TEXT        NOT NULL DEFAULT '' CHECK (char_length(docker_version) <= 40),
    apparmor       BOOLEAN     NOT NULL DEFAULT false,
    seccomp        BOOLEAN     NOT NULL DEFAULT false,
    runtimes       JSONB       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(runtimes) = 'array' AND octet_length(runtimes::text) <= 8192),
    error          TEXT        NOT NULL DEFAULT '' CHECK (char_length(error) <= 500),
    probed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Rollback (reversible; it loses every process limit set and what the
-- probe last read):
--   DROP TABLE IF EXISTS server_capabilities;
--   ALTER TABLE services DROP COLUMN IF EXISTS pids_limit;
