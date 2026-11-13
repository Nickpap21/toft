-- =============================================================================
-- Atlas Schema for control-panel-paas
-- DECLARATIVE — defines the desired final state.
-- Atlas diffs this against the live DB and generates the delta SQL.
-- Apply: make migrate  |  Preview: make migrate-plan
-- =============================================================================

-- =============================================================================
-- USERS
-- =============================================================================
CREATE TABLE IF NOT EXISTS users (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT        NOT NULL UNIQUE,
    name            TEXT        NOT NULL,
    password_hash   TEXT        NOT NULL,
    role            TEXT        NOT NULL DEFAULT 'admin',
    active_org_id   UUID,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    last_login_at   TIMESTAMPTZ,
    -- A session token issued before this moment is refused (0024): set by a
    -- password change, so every other session of the account ends.
    tokens_valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The theme the user picked in Settings → Appearance (D-32, D-46); NULL
    -- until they pick one. The values are the frontend's THEME_PREFS.
    theme           TEXT CHECK (theme IN ('system:graphite-paper', 'system:mono-daylight',
                        'graphite', 'paper', 'mono', 'daylight', 'ink', 'contrast')),
    -- When and by which operator the account was deactivated (0030, D-50);
    -- NULL while active, and for accounts deactivated before 0030.
    deactivated_at  TIMESTAMPTZ,
    deactivated_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    -- A deleted account (0033, D-57) is a tombstone: the row stays so the
    -- projects, servers and audit rows that point at it keep their
    -- references, while its email, name and password are replaced. Always
    -- deactivated first.
    deleted_at      TIMESTAMPTZ,
    deleted_by      UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (NOT is_active OR (deactivated_at IS NULL AND deactivated_by IS NULL)),
    CHECK (deleted_at IS NULL OR NOT is_active)
);

-- Emails are unique ignoring case too, so 'Founder@x.com' cannot be
-- registered beside 'founder@x.com'. Lookups by email for operator grants and
-- TOFT_OPERATOR_EMAILS are case-insensitive and refuse ambiguous matches.
CREATE UNIQUE INDEX IF NOT EXISTS uq_users_email_lower ON users (lower(email));

-- =============================================================================
-- ORGANIZATIONS
-- =============================================================================
CREATE TABLE IF NOT EXISTS organizations (
    id                        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 1 to 100 characters, as the API takes them (0054).
    name                      TEXT        NOT NULL CONSTRAINT organizations_name_length CHECK (length(name) BETWEEN 1 AND 100),
    resource_bypass_min_role  TEXT        NOT NULL DEFAULT 'owner',
    -- The workspace's part of its repositories' URLs (0036, D-71), made from
    -- its name when first needed; NULL until then.
    slug                      TEXT        CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?$'),
    -- How many digits issue numbers show (3: VLB-001), unless a project
    -- says otherwise (D-83; 0041). Workspaces from before 0041 keep 0
    -- (premigrate.sql).
    issue_key_digits          SMALLINT    NOT NULL DEFAULT 3 CHECK (issue_key_digits BETWEEN 0 AND 6),
    -- Agents paused in this workspace (0048, D-100): every agent call is
    -- refused while it is set.
    agents_paused_at          TIMESTAMPTZ,
    agents_paused_by          UUID        REFERENCES users(id) ON DELETE SET NULL,
    -- Cycles (D-90; 0044): their length in weeks (NULL: the workspace
    -- doesn't use them), the weekday they start on (0 Sunday to 6
    -- Saturday) and the time zone their days are in; and the scale of its
    -- issues' estimates.
    cycle_weeks               SMALLINT    CHECK (cycle_weeks BETWEEN 1 AND 4),
    cycle_start_day           SMALLINT    NOT NULL DEFAULT 1 CHECK (cycle_start_day BETWEEN 0 AND 6),
    cycle_time_zone           TEXT        NOT NULL DEFAULT 'UTC' CHECK (char_length(cycle_time_zone) BETWEEN 1 AND 64),
    issue_estimates           TEXT        NOT NULL DEFAULT 'off' CHECK (issue_estimates IN ('off', 'fibonacci', 'linear', 'tshirt')),
    -- The isolation level its services run at unless one chooses its own
    -- (D-95; 0046): standard (runc), sandboxed (gVisor runsc) or
    -- sandboxed_strict (gVisor runsc-strict, its own network stack; D-115, 0053).
    default_isolation         TEXT        NOT NULL DEFAULT 'standard' CHECK (default_isolation IN ('standard', 'sandboxed', 'sandboxed_strict')),
    -- When an owner hid Home's Finish setting up checklist (7g; 0066).
    setup_hidden_at           TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_organizations_slug ON organizations (slug);

ALTER TABLE users ADD CONSTRAINT fk_users_active_org
    FOREIGN KEY (active_org_id) REFERENCES organizations(id) ON DELETE SET NULL;

-- =============================================================================
-- INSTANCE (instance-level; not per-org)
-- Instance operators are separate from org roles (D-14). users.role is not
-- used for this: it is 'admin' on every row.
-- instance_settings is key/value; the 'registration' key holds
-- {"mode":"invite_only"} and a missing row means invite_only. Modes and
-- operator sources are validated in Go, not by CHECK constraints, so new ones
-- need no migration.
-- =============================================================================
CREATE TABLE IF NOT EXISTS instance_settings (
    key        TEXT        PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS instance_operators (
    user_id    UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    granted_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    source     TEXT        NOT NULL,           -- first_run | bootstrap | env | api
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- ORG MEMBERS
-- =============================================================================
CREATE TABLE IF NOT EXISTS org_members (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT        NOT NULL DEFAULT 'viewer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, user_id)
);

-- =============================================================================
-- ORG INVITES
-- =============================================================================
CREATE TABLE IF NOT EXISTS org_invites (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    created_by  UUID        NOT NULL REFERENCES users(id),
    -- The link's sha256 (hex), for lookup. Rows from before 0054 held the
    -- link itself until the backend's first start converted them.
    token       TEXT        NOT NULL UNIQUE,
    role        TEXT        NOT NULL DEFAULT 'developer',
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The link, encrypted with ENCRYPTION_KEY, so owners can copy a pending
    -- one again. NULL only on a row not yet converted.
    token_encrypted TEXT
);

-- =============================================================================
-- PASSWORD RESETS
-- =============================================================================
-- One-time links an instance operator issues for an account that forgot its
-- password (D-56). The URL carries the raw token; only its sha256 is stored.
-- Issuing a new one deletes the account's earlier unused rows.
CREATE TABLE IF NOT EXISTS password_resets (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_by UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- SIGNED-OUT SESSIONS
-- =============================================================================
-- Session tokens signed out one by one (0054): the sha256 of the token, kept
-- until the token would have expired anyway. Signing out of every session
-- moves users.tokens_valid_from instead.
CREATE TABLE IF NOT EXISTS revoked_sessions (
    token_hash TEXT        PRIMARY KEY CHECK (length(token_hash) = 64),
    expires_at TIMESTAMPTZ NOT NULL
);

-- =============================================================================
-- INSTANCE AUDIT LOG
-- =============================================================================
-- What operators did to accounts and operators (D-57): grants, revocations,
-- deactivations, reactivations, deletions, reset links issued and used.
-- audit_log is per workspace (org_id NOT NULL); these are instance events.
-- Names are resolved by join at read time, so a deleted account reads as
-- its tombstone.
CREATE TABLE IF NOT EXISTS instance_audit_log (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id       UUID        REFERENCES users(id) ON DELETE SET NULL,
    action         TEXT        NOT NULL,
    target_user_id UUID        REFERENCES users(id) ON DELETE SET NULL,
    detail         JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The panel's own server, one sample a minute, kept 7 days (D-68): what
-- Instance -> Server charts. The primary key serves both the range reads
-- (taken_at >= $1) and the prune (taken_at < $1).
CREATE TABLE IF NOT EXISTS panel_samples (
    taken_at          TIMESTAMPTZ PRIMARY KEY,
    cpu_percent       REAL        NOT NULL CHECK (cpu_percent >= 0),
    memory_used_bytes BIGINT      NOT NULL CHECK (memory_used_bytes >= 0),
    disk_used_bytes   BIGINT      NOT NULL CHECK (disk_used_bytes >= 0)
);

-- =============================================================================
-- PROJECTS
-- =============================================================================
CREATE TABLE IF NOT EXISTS projects (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id      UUID        REFERENCES organizations(id) ON DELETE SET NULL,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    icon        TEXT        NOT NULL DEFAULT '▦',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, name),
    -- The key of the project's issues (STO in STO-12), set from its name at
    -- its first issue and unique in the workspace, and the number its next
    -- issue gets (D-81; 0040).
    issue_key         TEXT    CHECK (issue_key ~ '^[A-Z][A-Z0-9]{1,9}$'),
    next_issue_number INTEGER NOT NULL DEFAULT 1 CHECK (next_issue_number > 0),
    -- Its own digits (NULL: the workspace's) and default type (D-83; 0041);
    -- the type's foreign key follows issue_types below.
    issue_key_digits  SMALLINT CHECK (issue_key_digits BETWEEN 0 AND 6),
    default_issue_type_id UUID,
    -- Opens an issue for a service's run of failed deploys (D-88; 0043).
    failed_deploy_issues BOOLEAN NOT NULL DEFAULT false,
    -- Previews (D-97; 0047): the environment they copy (NULL: off; its
    -- foreign key follows environments) and the domain under whose wildcard
    -- record they get their addresses (NULL: names under sslip.io).
    preview_environment_id UUID,
    preview_domain         TEXT CHECK (char_length(preview_domain) <= 200
        AND preview_domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'),
    -- How many previews it runs at once, stopped ones included (0077).
    preview_max            SMALLINT NOT NULL DEFAULT 5 CHECK (preview_max BETWEEN 1 AND 20),
    -- For repositories' (project_id, org_id) foreign key (0036).
    UNIQUE(id, org_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_issue_key ON projects (org_id, issue_key) WHERE issue_key IS NOT NULL;

-- One GitHub webhook secret per project (README §7 item 8): a push signed
-- with it deploys only that project's services that match the repository and
-- branch. Created when someone first opens the project's webhook.
CREATE TABLE IF NOT EXISTS project_webhooks (
    project_id  UUID        PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    secret      TEXT        NOT NULL,  -- encrypted with ENCRYPTION_KEY
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The last signed branch push and its repository's full name (0077).
    last_push_at   TIMESTAMPTZ,
    last_push_repo TEXT CHECK (char_length(last_push_repo) <= 200)
);

-- =============================================================================
-- SERVERS
-- =============================================================================
CREATE TABLE IF NOT EXISTS servers (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id      UUID        REFERENCES organizations(id) ON DELETE SET NULL,
    name        TEXT        NOT NULL,
    -- D-153 (0067): the name is the automatic server-<n> Add a server gave
    -- it; the first provisioning replaces it with the server's hostname,
    -- and a rename clears it.
    name_automatic BOOLEAN  NOT NULL DEFAULT false,
    ip_address  TEXT        NOT NULL,
    "user"      TEXT        NOT NULL DEFAULT 'root',
    port        INT         NOT NULL DEFAULT 22,
    ssh_key              TEXT        NOT NULL,
    status               TEXT        NOT NULL DEFAULT 'provisioning',
    -- Set once Provision completes successfully; distinct from `status`, which
    -- also flips to 'active' on a bare SSH test-connection with no provisioning
    -- ever having run. NULL means "not yet provisioned".
    provisioned_at       TIMESTAMPTZ,
    total_cpu_cores      FLOAT,
    total_memory_mb      BIGINT,
    cpu_cores_override   FLOAT,
    memory_mb_override   BIGINT,
    -- The server's SSH host key in authorized_keys form ("ssh-ed25519 AAAA..."),
    -- pinned by the first successful connection (trust on first use); every
    -- later connection must present it. Public, so not encrypted. NULL means
    -- "pin on the next connection". Replaced only by an instance operator
    -- (PUT /servers/:serverID/host-key). See 0009_server_host_keys.sql.
    host_key             TEXT,
    host_key_pinned_at   TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- How the panel reaches the server (D-106): 'ssh' signs in to it, 'agent'
    -- runs the same SSH protocol inside the WebSocket the server's toft-agent
    -- opens to the panel. A server joined by token has no SSH key ('').
    connection           TEXT        NOT NULL DEFAULT 'ssh',
    -- The agent's own ed25519 key in authorized_keys form, pinned when it
    -- joined; every agent connection must present it as its SSH host key.
    -- NULL: no agent has joined (or it was revoked).
    agent_key            TEXT,
    agent_key_pinned_at  TIMESTAMPTZ,
    -- A rotation's new key, admitted beside agent_key until the agent
    -- connects with one of them: pinned in its place when it does.
    agent_next_key       TEXT,
    agent_version        TEXT,
    agent_arch           TEXT,
    -- The server's boot ID when the agent last connected: a new one re-runs
    -- the capability probe.
    agent_boot_id        TEXT,
    agent_connected_at   TIMESTAMPTZ,
    agent_last_seen_at   TIMESTAMPTZ,
    agent_from_ip        TEXT,
    agent_revoked_at     TIMESTAMPTZ,
    -- Desired state (D-111): spec_generation goes up with every change to
    -- what should run on the server; the agent acks the snapshot it applied
    -- into observed_generation, and reports how Docker differs from it in
    -- agent_drift (a JSON array, written at agent_drift_at).
    spec_generation      BIGINT      NOT NULL DEFAULT 0,
    observed_generation  BIGINT      NOT NULL DEFAULT 0,
    agent_drift          JSONB       NOT NULL DEFAULT '[]'::jsonb,
    agent_drift_at       TIMESTAMPTZ,
    -- D-113 (0052): how many services Traefik routes from the agent's
    -- files (NULL while it reads the containers' labels), and, while the
    -- agent holds another panel epoch's state (the panel was restored from
    -- a backup), since when and what runs (agentproto.Running, an array).
    agent_routes         INTEGER     CHECK (agent_routes >= 0),
    agent_frozen_at      TIMESTAMPTZ,
    agent_frozen         JSONB,
    -- The disk policy (D-135); NULL follows the default: 10% of the disk,
    -- untagged and old builds, 85%. An empty auto clean turns it off.
    build_cache_limit_bytes BIGINT   CHECK (build_cache_limit_bytes >= 1000000000),
    disk_auto_clean      TEXT[]      CHECK (disk_auto_clean <@ ARRAY['untagged', 'old_builds', 'unused_pulled']::TEXT[]),
    disk_alert_percent   SMALLINT    CHECK (disk_alert_percent BETWEEN 50 AND 99),
    -- The last disk clean that ended (D-135), as the API's DiskClean.
    disk_last_clean      JSONB       CHECK (jsonb_typeof(disk_last_clean) = 'object' AND octet_length(disk_last_clean::text) <= 8192),
    UNIQUE(org_id, name),
    CONSTRAINT servers_agent_frozen_check CHECK ((agent_frozen_at IS NULL) = (agent_frozen IS NULL)
        AND (agent_frozen IS NULL OR (jsonb_typeof(agent_frozen) = 'array' AND octet_length(agent_frozen::text) <= 262144))),
    CONSTRAINT servers_generation_check CHECK (observed_generation >= 0 AND observed_generation <= spec_generation),
    CONSTRAINT servers_agent_drift_check CHECK (jsonb_typeof(agent_drift) = 'array'),
    CONSTRAINT servers_connection_check CHECK (connection IN ('ssh', 'agent')),
    CONSTRAINT servers_ssh_key_check CHECK (connection = 'agent' OR ssh_key <> '')
);

-- One-time tokens that join a server's agent (D-106): SHA-256 of the token,
-- single use, an hour. replaces_key marks a rejoin, which replaces a pinned
-- agent key and needed the password.
CREATE TABLE IF NOT EXISTS server_join_tokens (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id    UUID        NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    token_hash   BYTEA       NOT NULL UNIQUE,
    replaces_key BOOLEAN     NOT NULL DEFAULT FALSE,
    created_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_server_join_tokens_server ON server_join_tokens (server_id, created_at DESC);

-- =============================================================================
-- SERVER DNS PROVIDERS
-- A remote server's DNS provider, for wildcard certificates (DNS-01). The
-- provisioner writes each credential to a root-only file on the server and
-- gives Traefik <NAME>_FILE (lego reads every variable from a file that way).
-- provider is a lego provider name (https://go-acme.github.io/lego/dns/).
-- =============================================================================
CREATE TABLE IF NOT EXISTS server_dns_providers (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    provider    TEXT        NOT NULL CHECK (provider ~ '^[a-z0-9]{1,40}$'),
    credentials TEXT        NOT NULL,  -- encrypted JSON object: variable name -> value
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- SERVER PROVISION RUNS (D-48, 0029)
-- The last provisioning run of each server, replaced by the next one: who
-- started it, when, how it ended and its log (the last 256 KiB), so the
-- server's page shows it after the stream is gone. finished_at and ok are
-- NULL while it runs; a run the backend's restart cut short is closed at
-- startup as failed.
-- =============================================================================
-- What a server can run, read by the capability probe (D-93; 0045) on
-- Scan again, after provisioning and daily: its kernel, architecture,
-- cgroups, virtualisation, /dev/kvm, Docker and its security, and each
-- runtime Docker lists with the result of a smoke test.
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
    -- [{"name": "runc", "ok": true, "error": ""}], in Docker's order.
    runtimes       JSONB       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(runtimes) = 'array' AND octet_length(runtimes::text) <= 8192),
    error          TEXT        NOT NULL DEFAULT '' CHECK (char_length(error) <= 500),
    -- The host firewall's chain for untrusted previews is loaded (D-97;
    -- 0047): Provision, Install gVisor and toft upgrade install it.
    preview_firewall BOOLEAN NOT NULL DEFAULT false,
    -- The agent's DNS relay answered a container on the bridge (D-115; 0053).
    dns_relay      BOOLEAN     NOT NULL DEFAULT false,
    -- Docker's default-address-pools, as docker info reports them (0074):
    -- [{"base": "10.203.0.0/16", "size": 24}], [] when it has none, NULL
    -- until a probe read them.
    address_pools  JSONB       CHECK (jsonb_typeof(address_pools) = 'array' AND octet_length(address_pools::text) <= 4096),
    probed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Each server's last updates read (D-133; 0057), shaped like the API's
-- HostUpdates. A read that fails keeps report and sets error.
CREATE TABLE IF NOT EXISTS server_updates (
    server_id  UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    report     JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(report) = 'object' AND octet_length(report::text) <= 262144),
    error      TEXT        NOT NULL DEFAULT '' CHECK (char_length(error) <= 500),
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS server_provision_runs (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    started_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    ok          BOOLEAN,
    error       TEXT        NOT NULL DEFAULT '' CHECK (length(error) <= 2000),
    log         TEXT        NOT NULL DEFAULT '' CHECK (length(log) <= 262144),
    -- What a run that ended ok left out (0068): "the firewall (ufw,
    -- fail2ban)", "the host firewall for app containers", "nightly backups",
    -- "automatic security updates". The server is
    -- usable; the Servers page asks for a Re-provision.
    warnings    TEXT[]      NOT NULL DEFAULT '{}' CHECK (cardinality(warnings) <= 10),
    CHECK ((finished_at IS NULL) = (ok IS NULL))
);

-- The last gVisor install on each server (D-95; 0046), as the provision
-- run above: who ran it, its outcome and its log, and the release.
CREATE TABLE IF NOT EXISTS server_gvisor_runs (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    release     TEXT        NOT NULL CHECK (char_length(release) BETWEEN 1 AND 40),
    started_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    ok          BOOLEAN,
    error       TEXT        NOT NULL DEFAULT '' CHECK (length(error) <= 2000),
    log         TEXT        NOT NULL DEFAULT '' CHECK (length(log) <= 262144),
    CHECK ((finished_at IS NULL) = (ok IS NULL))
);

-- The run of updates on each server in progress, or its last (D-134; 0058):
-- install updates, update Docker or reboot. The host's own files say how it
-- goes while it runs; this row says who started it, and finishing it (once,
-- finished_at set) is what writes its outcome to the audit log.
CREATE TABLE IF NOT EXISTS server_update_runs (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    run_id      TEXT        NOT NULL CHECK (run_id ~ '^[0-9a-f]{32}$'),
    action      TEXT        NOT NULL CHECK (action IN ('install', 'docker', 'reboot')),
    started_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    outcome     TEXT        NOT NULL DEFAULT 'running' CHECK (outcome IN ('running', 'succeeded', 'failed')),
    packages    INTEGER     NOT NULL DEFAULT 0 CHECK (packages >= 0),
    error       TEXT        NOT NULL DEFAULT '' CHECK (char_length(error) <= 2000),
    output      TEXT        NOT NULL DEFAULT '' CHECK (octet_length(output) <= 131072),
    CHECK ((finished_at IS NULL) = (outcome = 'running'))
);

-- =============================================================================
-- BACKUP DESTINATIONS (D-43, 0022)
-- Where a server backs itself up: an S3-compatible bucket. server_id NULL is
-- the panel host (one row at most). Each server runs restic on a timer and
-- writes its result to a file the alert monitor reads; last_* is the last
-- result read. The secret key and the restic repository password are
-- encrypted with ENCRYPTION_KEY and never returned by the API; the password
-- is shown once, when the destination is created.
-- =============================================================================
CREATE TABLE IF NOT EXISTS backup_destinations (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id     UUID        UNIQUE REFERENCES servers(id) ON DELETE CASCADE,
    endpoint      TEXT        NOT NULL CHECK (endpoint ~ '^https://[a-z0-9.-]+(:[0-9]{1,5})?$'),
    bucket        TEXT        NOT NULL CHECK (bucket ~ '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$'),
    access_key    TEXT        NOT NULL CHECK (access_key ~ '^[A-Za-z0-9+/=_.~-]{1,255}$'),
    secret_key    TEXT        NOT NULL,
    repo_password TEXT        NOT NULL,
    schedule      TEXT        NOT NULL DEFAULT '03:00' CHECK (schedule ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    last_run_at   TIMESTAMPTZ,
    last_ok       BOOLEAN,
    last_ok_at    TIMESTAMPTZ,
    last_bytes    BIGINT      CHECK (last_bytes >= 0),
    last_error    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS backup_destinations_one_panel_host ON backup_destinations ((server_id IS NULL)) WHERE server_id IS NULL;

-- =============================================================================
-- ENVIRONMENTS
-- =============================================================================
CREATE TABLE IF NOT EXISTS environments (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT        NOT NULL,
    -- Its chip colour and whether changes to it confirm first (D-55). Red
    -- left the colours (D-142): 0060 stopped writing it, 0071 (contract)
    -- refuses it.
    color      TEXT        NOT NULL DEFAULT 'gray'
        CHECK (color IN ('amber', 'violet', 'blue', 'teal', 'gray')),
    protected  BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, name)
);
-- Off when the environment previews copy is deleted.
ALTER TABLE projects ADD CONSTRAINT fk_projects_preview_environment
    FOREIGN KEY (preview_environment_id) REFERENCES environments(id) ON DELETE SET NULL;

-- =============================================================================
-- SERVICES
-- =============================================================================
CREATE TABLE IF NOT EXISTS services (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- scope controls which context this service belongs to.
    -- 'environment' = classic per-env service (environment_id must be set)
    -- 'project'     = shared across all environments of a project (project_id must be set)
    -- 'org'         = shared across the entire org (scoped_org_id must be set)
    scope                TEXT        NOT NULL DEFAULT 'environment'
        CHECK (scope IN ('environment', 'project', 'org')),
    environment_id       UUID        REFERENCES environments(id) ON DELETE CASCADE,
    project_id           UUID        REFERENCES projects(id) ON DELETE CASCADE,
    scoped_org_id        UUID        REFERENCES organizations(id) ON DELETE CASCADE,
    server_id            UUID        REFERENCES servers(id) ON DELETE SET NULL,
    name                 TEXT        NOT NULL,
    type                 TEXT        NOT NULL DEFAULT 'web',
    source_type          TEXT        NOT NULL DEFAULT 'git',
    git_repo_url         TEXT,
    git_branch           TEXT        NOT NULL DEFAULT 'main',
    base_directory       TEXT        NOT NULL DEFAULT '/',
    docker_image         TEXT,
    install_command      TEXT,
    build_command        TEXT,
    start_command        TEXT,
    build_pack           TEXT        NOT NULL DEFAULT 'auto',
    port                 INT         NOT NULL DEFAULT 3000,
    replicas             INT         NOT NULL DEFAULT 1,
    cpu_limit            TEXT,
    memory_limit         TEXT,
    -- Set once at the D-41 upgrade for services without a memory limit; a deploy without one is refused after it.
    memory_limit_grace_until TIMESTAMPTZ,
    runtime_status       TEXT        NOT NULL DEFAULT 'idle',
    active_deployment_id UUID,
    compose_content      TEXT,
    compose_repo_url     TEXT NOT NULL DEFAULT '',
    compose_file_path    TEXT NOT NULL DEFAULT '',
    registry_server      VARCHAR(255),
    registry_username    VARCHAR(255),
    registry_password    TEXT,
    addon_slug           TEXT,
    network_alias        TEXT,
    volumes              TEXT NOT NULL DEFAULT '[]',
    git_username         TEXT,
    git_token            TEXT,
    pre_deploy_commands  TEXT        NOT NULL DEFAULT '',
    post_deploy_commands TEXT        NOT NULL DEFAULT '',
    health_check_path    TEXT        NOT NULL DEFAULT '/health',
    health_check_timeout INT         NOT NULL DEFAULT 5,
    health_check_retries INT         NOT NULL DEFAULT 0,
    required_env_vars    TEXT        NOT NULL DEFAULT '',
    migrate_command      TEXT        NOT NULL DEFAULT '',
    dependencies         TEXT        NOT NULL DEFAULT '[]',
    basic_auth_user      TEXT        NOT NULL DEFAULT '',
    basic_auth_password  TEXT        NOT NULL DEFAULT '',  -- encrypted with ENCRYPTION_KEY (0019)
    ip_allowlist         TEXT        NOT NULL DEFAULT '[]',
    -- Compose services only: the compose service that receives the service's
    -- domains, on port (Settings → Domain Routing). '' = automatic: the
    -- compose file's only service, or no routing when it has several. The
    -- platform generates the Traefik labels; compose files cannot (D-19).
    compose_route_service TEXT       NOT NULL DEFAULT '',
    -- Compose services only: the compose service the last successful deploy
    -- routed, so put on the app network, where other services reach the
    -- stack by its name (0075). NULL until a compose deploy recorded it; ''
    -- when that deploy routed none (no domain, or no service to route to).
    compose_network_service TEXT,
    -- How a deploy replaces the container (D-42, 0021): 'rolling' starts the
    -- new one before the old one stops, 'recreate' stops the old one first,
    -- 'auto' is recreate for a database or cache and rolling otherwise.
    deploy_strategy      TEXT        NOT NULL DEFAULT 'auto'
                         CHECK (deploy_strategy IN ('auto', 'rolling', 'recreate')),
    -- The most processes (threads too) each of its containers may run, for
    -- the hardened runtime (D-93; 0045); NULL is the platform's default.
    pids_limit           INTEGER     CHECK (pids_limit BETWEEN 256 AND 32768),
    -- How long a deploy may run before it fails as timed out, in minutes
    -- (D-125; 0056); NULL is the platform's default, 60.
    deploy_timeout_minutes INTEGER   CHECK (deploy_timeout_minutes BETWEEN 5 AND 240),
    -- How many of its last live deployments keep their image (D-135); NULL
    -- is the default, 10.
    rollback_images_kept SMALLINT    CHECK (rollback_images_kept BETWEEN 1 AND 100),
    -- Its isolation level (D-95; 0046); NULL follows the workspace's
    -- default_isolation.
    isolation            TEXT        CHECK (isolation IN ('standard', 'sandboxed', 'sandboxed_strict')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- One of the project's own repositories the service builds from, or
    -- takes its compose files from, instead of a URL (D-77, 0038); its
    -- foreign key follows the repositories table below. No URL beside it.
    repository_id        UUID,
    -- A push to the service's branch deploys it (GitHub's webhook and our
    -- own repositories alike); off, it deploys only by hand.
    deploy_on_push       BOOLEAN     NOT NULL DEFAULT true,
    -- A preview's copy of a service of the environment it copies (D-97;
    -- 0047): variables and secrets are the original's, read at each deploy.
    -- Its foreign key follows below.
    preview_of           UUID,
    -- The 127.0.0.1 port the catalog's Loki is published on, on its own
    -- server, for that server's services to send logs to (D-148, 0064).
    -- Picked at its first deploy and kept, moves included; unique across
    -- the instance, so it is free on any server. NULL for every other service.
    loopback_port        INTEGER     CHECK (loopback_port BETWEEN 39100 AND 39199),
    -- The Docker health check its containers are created with (0081):
    -- 'image' keeps the image's HEALTHCHECK, 'off' turns it off, 'command'
    -- runs container_health_command in the container's shell. Not for
    -- compose stacks, whose file sets its own.
    container_health     TEXT        NOT NULL DEFAULT 'image'
                         CHECK (container_health IN ('image', 'off', 'command')),
    container_health_command TEXT    NOT NULL DEFAULT ''
                         CHECK (length(container_health_command) <= 1024 AND container_health_command !~ '[\r\n]'),
    CONSTRAINT services_container_health_command CHECK (container_health <> 'command' OR container_health_command <> ''),
    CONSTRAINT services_compose_content_size CHECK (compose_content IS NULL OR length(compose_content) <= 1048576),
    CONSTRAINT services_repository_source CHECK (repository_id IS NULL OR (
        source_type IN ('git', 'docker-compose') AND COALESCE(git_repo_url, '') = '' AND compose_repo_url = '')),
    -- A preview never migrates (D-144; 0061 expand, 0071 contract): its copy
    -- has no migrate or pre-deploy command.
    CONSTRAINT services_preview_no_migrate CHECK (preview_of IS NULL OR (migrate_command = '' AND pre_deploy_commands = ''))
);

-- =============================================================================
-- DEPLOY POLICY EXCEPTIONS (D-19)
-- One row per service at most: the deploy-policy rules (internal/composespec
-- Rule values, e.g. 'docker_socket') that no longer block that service's
-- deploys. Granted and revoked only by instance operators; every change is
-- written to audit_log in the same transaction with metadata
-- {allowed_rules, reason}. Rule names are validated in Go (reserved_label,
-- external_ref and the other integrity rules can never be granted), not by a
-- CHECK constraint, so adding a rule needs no migration.
-- =============================================================================
CREATE TABLE IF NOT EXISTS service_deploy_policy_exceptions (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id    UUID        NOT NULL UNIQUE REFERENCES services(id) ON DELETE CASCADE,
    allowed_rules TEXT[]      NOT NULL DEFAULT '{}',
    reason        TEXT        NOT NULL DEFAULT '',
    granted_by    UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The values the grant approved (K3, 0055): an array of
    -- {fingerprint, rule, service, field, message, image}. A deploy is
    -- allowed a granted rule's violation only when its fingerprint is here.
    -- NULL for exceptions granted before 0055, which cover by rule.
    allowed_values JSONB      CHECK (allowed_values IS NULL OR jsonb_typeof(allowed_values) = 'array')
);

-- The last deploy-time check of a service whose compose file comes from a
-- repository (K3, 0055): its violations of grantable rules, in the
-- allowed_values shape, so an operator can approve what that deploy found.
-- Rewritten at each such deploy.
CREATE TABLE IF NOT EXISTS service_deploy_policy_checks (
    service_id UUID        PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    commit_sha TEXT        NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    violations JSONB       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(violations) = 'array')
);

-- =============================================================================
-- DOMAINS
-- =============================================================================
CREATE TABLE IF NOT EXISTS domains (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id  UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    hostname    TEXT        NOT NULL UNIQUE,
    tls_enabled BOOLEAN     NOT NULL DEFAULT TRUE,
    status      TEXT        NOT NULL DEFAULT 'pending',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Who added it (0023); NULL for domains from before, or a deleted user.
    created_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    -- The first successful deploy that routed it (0023); NULL until then,
    -- and for domains routed before 0023.
    routed_at   TIMESTAMPTZ,
    -- When a valid certificate was first seen for it (0065, D-150); NULL
    -- while one is pending, when a temporary (sslip.io) name answers over
    -- HTTP.
    certificate_at TIMESTAMPTZ
);

-- =============================================================================
-- ENV VARS
-- =============================================================================
CREATE TABLE IF NOT EXISTS env_vars (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id  UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    key         TEXT        NOT NULL,
    value       TEXT        NOT NULL,
    is_secret   BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- A plain variable hidden on screen, still stored and returned readable
    -- (0023); a secret (is_secret) is the write-once kind. Never both.
    masked      BOOLEAN     NOT NULL DEFAULT FALSE,
    -- Who set its current value (0023); NULL before, or a deleted user.
    updated_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    -- The agent that set it, acting for updated_by (0079, D-100); FK below.
    agent_connection_id UUID,
    UNIQUE(service_id, key),
    CONSTRAINT env_vars_masked_not_secret CHECK (NOT (masked AND is_secret)),
    CONSTRAINT env_vars_value_size CHECK (length(value) <= 32768)
);

-- =============================================================================
-- ENVIRONMENT ENV VARS
-- Variables and secrets shared by every service in an environment. A
-- service's own env_vars row with the same key wins (envvar
-- ListAllForDeployment). Secrets are encrypted like env_vars secrets.
-- =============================================================================
CREATE TABLE IF NOT EXISTS environment_env_vars (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id UUID        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    key            TEXT        NOT NULL CHECK (key ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
    value          TEXT        NOT NULL,
    is_secret      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Who set its current value (0023); NULL before, or a deleted user.
    updated_by     UUID        REFERENCES users(id) ON DELETE SET NULL,
    -- The agent that set it, acting for updated_by (0079, D-100); FK below.
    agent_connection_id UUID,
    UNIQUE (environment_id, key),
    CONSTRAINT environment_env_vars_value_size CHECK (length(value) <= 32768)
);

-- =============================================================================
-- DEPLOYMENTS
-- =============================================================================
CREATE TABLE IF NOT EXISTS deployments (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id      UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    status          TEXT        NOT NULL DEFAULT 'pending',
    image_tag       TEXT,
    commit_sha      TEXT,
    commit_message  TEXT,
    triggered_by    TEXT,
    queued_at       TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    error_summary   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The commit a compose service's repository was checked out at, which a
    -- rollback checks out again. commit_sha can be another repository's (a push
    -- to git_repo_url). NULL: no repository, or recorded before 0014.
    compose_commit_sha TEXT CHECK (compose_commit_sha ~ '^[0-9a-f]{40}$'),
    -- The push or merge that started it (git_events, D-77); its foreign key
    -- follows that table below.
    git_event_id    BIGINT,
    -- When the panel handed the rollout to the server's agent (D-111),
    -- written before the request is sent: a panel restart leaves such a
    -- deploy to the agent and records its outcome when the agent connects.
    -- handed_off_input is what the panel needs for that (never the deploy's
    -- environment or credentials, which only the agent has).
    handed_off_at    TIMESTAMPTZ,
    handed_off_input JSONB,
    -- When a disk clean removed its image (D-135): it can't be rolled back to.
    image_removed_at TIMESTAMPTZ,
    -- What the deploy shipped of its service's settings (0072, D-159): an
    -- HMAC of its settings, variables, config files, shared volumes or
    -- compose limits, Loki and runtime security, less its routing, written
    -- when it goes live. Toft redeploys a service for its certificate only
    -- while the live deployment's still matches. NULL: from before 0072, or
    -- a setting changed while it ran.
    settings_fingerprint TEXT CHECK (settings_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT deployments_handed_off_check CHECK ((handed_off_at IS NULL) = (handed_off_input IS NULL))
);

-- =============================================================================
-- DEPLOYMENT LOGS
-- =============================================================================
CREATE TABLE IF NOT EXISTS deployment_logs (
    id              BIGSERIAL   PRIMARY KEY,
    deployment_id   UUID        NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    level           TEXT        NOT NULL DEFAULT 'info',
    message         TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- API KEYS
-- =============================================================================
-- A key without a project acts with everything its user can do, in the
-- user's active workspace. A key with a project is limited to that project
-- and its scopes ("deploy", "variables"; internal/apikey/scope.go), and
-- with service_ids to those services of it (deploy and variables only; an
-- ID of a deleted service stays, so the key never widens). expires_at NULL
-- is a key that never expires (K4, K5; 0055).
CREATE TABLE IF NOT EXISTS api_keys (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    key_hash     TEXT        NOT NULL UNIQUE,
    prefix       TEXT        NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    project_id   UUID        REFERENCES projects(id) ON DELETE CASCADE,
    scopes       TEXT[]      NOT NULL DEFAULT '{}',
    expires_at   TIMESTAMPTZ CHECK (expires_at > created_at),
    service_ids  UUID[]      NOT NULL DEFAULT '{}',
    UNIQUE(user_id, name),
    CONSTRAINT api_keys_service_ids_check CHECK (
        cardinality(service_ids) = 0
        OR (project_id IS NOT NULL AND scopes <@ ARRAY['deploy', 'variables']::TEXT[])
    ),
    CONSTRAINT api_keys_scope_check CHECK (
        (project_id IS NULL AND cardinality(scopes) = 0)
        OR (project_id IS NOT NULL AND cardinality(scopes) > 0
            AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write', 'checks', 'issues_read', 'issues_write',
                                'docs_read', 'docs_write']::TEXT[])
    )
);

-- =============================================================================
-- CODE (git hosting, D-20, D-71; 0036)
-- =============================================================================
-- A project's git repositories; the bare repository lives in the paas-git
-- container at @hashed/ab/cd/<id>.git, so a rename never touches disk. A
-- project has one or more (D-75): the first made with it, others added by
-- its members. Its workspace is its project's, which the composite foreign
-- key holds. protect_default_branch refuses pushes to protected_branch, the
-- default branch's name when protection was turned on or the default branch
-- last moved (git's pre-receive hook in paas-git; never HEAD at the push,
-- 0080); NULL while on until the backend stores HEAD's name at a push, for
-- repositories protected before 0080 or while empty. next_pull_number
-- numbers the repository's pull requests.
CREATE TABLE IF NOT EXISTS repositories (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id  UUID        NOT NULL,
    name        TEXT        NOT NULL CHECK (name ~ '^[a-z0-9]([a-z0-9._-]{0,98}[a-z0-9])?$' AND name !~ '\.git$'),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    protect_default_branch BOOLEAN NOT NULL DEFAULT false,
    protected_branch       TEXT    CHECK (protected_branch <> ''),
    next_pull_number       INTEGER NOT NULL DEFAULT 1 CHECK (next_pull_number > 0),
    UNIQUE (org_id, name),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_repositories_project ON repositories (project_id);

-- A pull request (D-75): head_branch into base_branch, both in the
-- repository. head_sha is the head's commit when last seen (opened or
-- pushed), which refs/pull/<number>/head keeps alive after the branch is
-- deleted. A merged one has its merge commit, method, who merged it, and
-- where the head left the base (merge_base_sha), which its Changes show
-- against once the base holds the head.
CREATE TABLE IF NOT EXISTS pull_requests (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The agent connection that made it, acting for its person (0049, D-102).
    agent_connection_id UUID,
    repository_id  UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number         INTEGER     NOT NULL CHECK (number > 0),
    title          TEXT        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 256),
    body           TEXT        NOT NULL DEFAULT '' CHECK (octet_length(body) <= 65536),
    author_id      UUID        REFERENCES users(id) ON DELETE SET NULL,
    base_branch    TEXT        NOT NULL CHECK (char_length(base_branch) BETWEEN 1 AND 255),
    head_branch    TEXT        NOT NULL CHECK (char_length(head_branch) BETWEEN 1 AND 255),
    head_sha       TEXT        NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    state          TEXT        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed', 'merged')),
    merge_method   TEXT        CHECK (merge_method IN ('merge', 'squash', 'fast_forward')),
    merge_sha      TEXT        CHECK (merge_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    merge_base_sha TEXT        CHECK (merge_base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    merged_by      UUID        REFERENCES users(id) ON DELETE SET NULL,
    merged_at      TIMESTAMPTZ,
    closed_at      TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- ⌘K's search (D-81; 0040).
    search         TSVECTOR    GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED,
    -- Opened with an API key (D-97; 0047), and which one while it exists:
    -- its preview waits for approval.
    opened_with_key BOOLEAN    NOT NULL DEFAULT false,
    api_key_id     UUID        REFERENCES api_keys(id) ON DELETE SET NULL,
    UNIQUE (repository_id, number),
    CHECK (base_branch <> head_branch),
    CHECK ((state = 'merged') = (merge_sha IS NOT NULL AND merge_method IS NOT NULL AND merged_at IS NOT NULL))
);
-- One open pull request per pair of branches.
CREATE UNIQUE INDEX IF NOT EXISTS uq_pull_requests_open_pair ON pull_requests (repository_id, base_branch, head_branch) WHERE state = 'open';
CREATE INDEX IF NOT EXISTS idx_pull_requests_repository_state ON pull_requests (repository_id, state, number DESC);
CREATE INDEX IF NOT EXISTS idx_pull_requests_search ON pull_requests USING GIN (search);

-- A comment in a pull request's conversation, in markdown.
CREATE TABLE IF NOT EXISTS pull_request_comments (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The agent connection that made it, acting for its person (0049, D-102).
    agent_connection_id UUID,
    pull_request_id  UUID        NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    author_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    body             TEXT        NOT NULL CHECK (char_length(body) >= 1 AND octet_length(body) <= 65536),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pull_request_comments_pull ON pull_request_comments (pull_request_id, created_at);

-- What happened to a pull request, for its conversation: pushed (data:
-- before, after, the number of commits), closed, reopened, merged (data:
-- method, sha), head_deleted, title_changed (data: from, to), and its
-- preview's started, waiting, approved, updated, stopped and removed (data:
-- sha, services, reason; D-97), and migrations_skipped (data: sha, files;
-- D-144).
CREATE TABLE IF NOT EXISTS pull_request_events (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_request_id  UUID        NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    actor_id         UUID        REFERENCES users(id) ON DELETE SET NULL,
    kind             TEXT        NOT NULL CHECK (kind IN ('pushed', 'closed', 'reopened', 'merged', 'head_deleted', 'title_changed',
        'preview_started', 'preview_waiting', 'preview_approved', 'preview_updated', 'preview_stopped', 'preview_removed',
        'preview_migrations_skipped')),
    data             JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pull_request_events_pull ON pull_request_events (pull_request_id, created_at);

-- An account's SSH public keys for git over SSH. A key belongs to one
-- account on the instance (the fingerprint is unique).
CREATE TABLE IF NOT EXISTS ssh_keys (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title        TEXT        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 100),
    -- The key as one authorized_keys line, without its comment.
    public_key   TEXT        NOT NULL CHECK (char_length(public_key) <= 16384),
    fingerprint  TEXT        NOT NULL UNIQUE,
    key_type     TEXT        NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ssh_keys_user ON ssh_keys (user_id);

-- =============================================================================
-- DEPLOY ON PUSH AND CHECKS (D-77; 0038)
-- =============================================================================
-- NO ACTION: a repository a service builds from can't be deleted, while a
-- project's delete removes both in one statement. That the repository is the
-- service's project's is checked at save and at deploy (a CHECK can't read
-- another table).
ALTER TABLE services ADD CONSTRAINT fk_services_repository
    FOREIGN KEY (repository_id) REFERENCES repositories(id);
-- NO ACTION: a service previews copy is removed from them first (D-97).
ALTER TABLE services ADD CONSTRAINT fk_services_preview_of
    FOREIGN KEY (preview_of) REFERENCES services(id);
CREATE INDEX IF NOT EXISTS idx_services_preview_of ON services (preview_of) WHERE preview_of IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_services_repository ON services (repository_id, git_branch) WHERE repository_id IS NOT NULL;
-- One service per loopback port, on the whole instance (D-148).
CREATE UNIQUE INDEX IF NOT EXISTS idx_services_loopback_port ON services (loopback_port) WHERE loopback_port IS NOT NULL;

-- The code module's events outbox: one row per branch a push or a panel
-- merge moved (a merge's update-ref runs no hook, so the merge writes its
-- own). A dispatcher claims undispatched rows in order and deploys the
-- services building from the branch; attempts and next_attempt_at are its
-- retries, last_error the last failure. Kept 90 days once dispatched.
-- Since 0039 (D-79) it also holds what deploys nothing, written already
-- dispatched so no dispatcher claims it: a branch deleted (push, after_sha
-- all zeros), a tag created, moved or deleted (tag; branch is the tag's
-- name), and a pull request opened, pushed to, closed or reopened
-- (pull_request with its action; branch is the base). A pull mirror's sync
-- moving a branch is mirror, and deploys as a push does. Writing an event
-- writes its webhook deliveries in the same statement.
CREATE TABLE IF NOT EXISTS git_events (
    id              BIGSERIAL   PRIMARY KEY,
    -- The agent connection that made it, acting for its person (0049, D-102).
    agent_connection_id UUID,
    repository_id   UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('push', 'merge', 'mirror', 'tag', 'pull_request')),
    action          TEXT        CHECK (action IN ('opened', 'pushed', 'closed', 'reopened')),
    branch          TEXT        NOT NULL CHECK (char_length(branch) BETWEEN 1 AND 255),
    -- before_sha is all zeros for a branch the push created.
    before_sha      TEXT        NOT NULL CHECK (before_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    after_sha       TEXT        NOT NULL CHECK (after_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE SET NULL,
    -- after_sha's subject, how many commits the push brought, and whether
    -- it rewrote the branch.
    subject         TEXT        NOT NULL DEFAULT '' CHECK (char_length(subject) <= 300),
    commits         INTEGER     NOT NULL DEFAULT 0 CHECK (commits >= 0),
    forced          BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at   TIMESTAMPTZ,
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT        CHECK (char_length(last_error) <= 1000),
    -- When the issues module applied it (D-81; 0040): NULL until then,
    -- whatever the kind, so events written while it was down or by an older
    -- release are applied later.
    issues_at       TIMESTAMPTZ,
    -- A push made with an API key (D-97; 0047), and when the previews
    -- module applied the event (NULL until then, as issues_at).
    with_key        BOOLEAN     NOT NULL DEFAULT false,
    previews_at     TIMESTAMPTZ,
    CONSTRAINT git_events_action CHECK ((kind = 'pull_request') = (action IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_git_events_repository ON git_events (repository_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_git_events_pending ON git_events (next_attempt_at) WHERE dispatched_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_git_events_issues ON git_events (id) WHERE issues_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_git_events_previews ON git_events (id) WHERE previews_at IS NULL;

-- A pull request's preview (D-97; 0047): an environment copying the
-- project's preview environment, whose services that build from the pull
-- request's repository build from its head. environment_id is set once it
-- starts. state is what the platform decided; whether it is starting,
-- running or failed is its deployments'. An untrusted preview (opened or
-- pushed to with an API key) runs only commits an owner or developer
-- approved, Sandboxed, in a network of its own. NO ACTION on the pull
-- request and the environment it copies: their deletes remove previews
-- first; deleting the preview's own environment removes its row.
CREATE TABLE IF NOT EXISTS previews (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_request_id       UUID        NOT NULL UNIQUE REFERENCES pull_requests(id),
    project_id            UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_environment_id UUID        NOT NULL REFERENCES environments(id),
    environment_id        UUID        UNIQUE REFERENCES environments(id) ON DELETE CASCADE,
    untrusted             BOOLEAN     NOT NULL DEFAULT false,
    head_sha              TEXT        NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    approved_sha          TEXT        CHECK (approved_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    approved_by           UUID        REFERENCES users(id) ON DELETE SET NULL,
    approved_at           TIMESTAMPTZ,
    state                 TEXT        NOT NULL
        CHECK (state IN ('waiting_approval', 'waiting_room', 'active', 'stopped', 'removing')),
    stopped_reason        TEXT        CHECK (stopped_reason IN ('room', 'manual')),
    -- Why it can't start, or what stopped it for room, in words.
    note                  TEXT        NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000),
    -- The migrations the pull request's diff touches, which the preview
    -- skips (D-144): the first 50 paths.
    migration_files       TEXT[]      NOT NULL DEFAULT '{}',
    -- True when the diff was too large to check (over 3,000 files), so
    -- migration_files may be incomplete.
    migrations_unchecked  BOOLEAN     NOT NULL DEFAULT false,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((state = 'stopped') = (stopped_reason IS NOT NULL)),
    CHECK (state <> 'active' OR environment_id IS NOT NULL),
    CHECK ((approved_sha IS NULL) = (approved_at IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_previews_project ON previews (project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_previews_source ON previews (source_environment_id);

-- One deployment per service and event, so a dispatch run twice deploys once.
ALTER TABLE deployments ADD CONSTRAINT fk_deployments_git_event
    FOREIGN KEY (git_event_id) REFERENCES git_events(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_deployments_git_event ON deployments (git_event_id, service_id) WHERE git_event_id IS NOT NULL;

-- A commit's checks: external CI reports them by name (a project key with
-- the checks scope); every deploy of a service building from the repository
-- reports one named toft/deploy/<environment>/<service>, linked to its
-- deployment, a prefix nobody else may use. Reporting a name again updates
-- it. At most 100 per commit (checked when one is added).
CREATE TABLE IF NOT EXISTS commit_checks (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    sha           TEXT        NOT NULL CHECK (sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    name          TEXT        NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 100 AND name !~ '[[:cntrl:]]'),
    state         TEXT        NOT NULL CHECK (state IN ('pending', 'running', 'success', 'failure', 'cancelled', 'skipped')),
    description   TEXT        NOT NULL DEFAULT '' CHECK (char_length(description) <= 300),
    details_url   TEXT        CHECK (details_url ~ '^https?://' AND char_length(details_url) <= 2000),
    deployment_id UUID        REFERENCES deployments(id) ON DELETE SET NULL,
    reporter_id   UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at    TIMESTAMPTZ,
    completed_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (repository_id, sha, name)
);

-- =============================================================================
-- IMPORT, MIRRORS AND OUTBOUND WEBHOOKS (D-79; 0039)
-- =============================================================================
-- A repository's pull mirror (one at most: what an import is; keep makes it
-- pull every 10 minutes after the import, and the repository is read-only
-- meanwhile) or push mirrors (Toft force-pushes every branch and tag after
-- each change). The URL is HTTPS with no user in it; the token or password
-- is encrypted (ENCRYPTION_KEY) and never returned. next_sync_at is when the
-- worker should sync it (NULL: nothing due); syncing_since is set while a
-- sync runs; failures counts failed syncs in a row. remote_* is what a pull
-- mirror found on the remote when it last looked.
CREATE TABLE IF NOT EXISTS repository_mirrors (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id      UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    direction          TEXT        NOT NULL CHECK (direction IN ('pull', 'push')),
    url                TEXT        NOT NULL CHECK (url ~ '^https://[^/?#@[:space:]]+/[^?#[:space:]]+$' AND char_length(url) <= 2048),
    username           TEXT        NOT NULL DEFAULT '' CHECK (char_length(username) <= 255 AND username !~ '[[:cntrl:]:]'),
    password_encrypted TEXT,
    keep               BOOLEAN     NOT NULL DEFAULT true,
    created_by         UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    next_sync_at       TIMESTAMPTZ,
    syncing_since      TIMESTAMPTZ,
    last_sync_at       TIMESTAMPTZ,
    last_ok_at         TIMESTAMPTZ,
    failures           INTEGER     NOT NULL DEFAULT 0 CHECK (failures >= 0),
    last_error         TEXT        CHECK (char_length(last_error) <= 1000),
    last_moved         INTEGER     CHECK (last_moved >= 0),
    remote_head        TEXT        CHECK (char_length(remote_head) <= 255),
    remote_branches    INTEGER     CHECK (remote_branches >= 0),
    remote_tags        INTEGER     CHECK (remote_tags >= 0),
    CONSTRAINT repository_mirrors_push_keeps CHECK (direction = 'pull' OR keep)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_repository_mirrors_pull ON repository_mirrors (repository_id) WHERE direction = 'pull';
CREATE INDEX IF NOT EXISTS idx_repository_mirrors_repository ON repository_mirrors (repository_id, direction);
CREATE INDEX IF NOT EXISTS idx_repository_mirrors_due ON repository_mirrors (next_sync_at) WHERE next_sync_at IS NOT NULL;

-- A repository's outbound webhooks: Toft posts the events named in events
-- to url (HTTPS, no user in it), signed with the secret (encrypted, shown
-- once at create and at rotate).
CREATE TABLE IF NOT EXISTS repository_webhooks (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id     UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    url               TEXT        NOT NULL CHECK (url ~ '^https://[^/?#@[:space:]]+([/?][^#[:space:]]*)?$' AND char_length(url) <= 2048),
    -- Written as Postgres prints it, so a second apply plans no change.
    events            TEXT[]      NOT NULL CHECK (((cardinality(events) >= 1) AND (cardinality(events) <= 2)) AND (events <@ ARRAY['push'::text, 'pull_request'::text])),
    active            BOOLEAN     NOT NULL DEFAULT true,
    secret_encrypted  TEXT        NOT NULL,
    created_by        UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    secret_rotated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    secret_rotated_by UUID        REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_repository_webhooks_repository ON repository_webhooks (repository_id);

-- One event for one webhook: written with its git event (or by a ping),
-- its payload built and kept at the first attempt, so a retry or a
-- redelivery sends the same bytes. A worker sends pending ones from
-- next_attempt_at; kept 30 days.
CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id      UUID        NOT NULL REFERENCES repository_webhooks(id) ON DELETE CASCADE,
    git_event_id    BIGINT      REFERENCES git_events(id) ON DELETE SET NULL,
    event           TEXT        NOT NULL CHECK (event IN ('push', 'pull_request', 'ping')),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    summary         TEXT        NOT NULL DEFAULT '' CHECK (char_length(summary) <= 300),
    payload         TEXT        CHECK (octet_length(payload) <= 262144),
    state           TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'failed')),
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    response_status INTEGER,
    response_body   TEXT        CHECK (octet_length(response_body) <= 2048),
    error           TEXT        CHECK (char_length(error) <= 500),
    duration_ms     INTEGER     CHECK (duration_ms >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_deliveries_event ON webhook_deliveries (webhook_id, git_event_id, event) WHERE git_event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook ON webhook_deliveries (webhook_id, created_at DESC, id DESC);

-- =============================================================================
-- ISSUES (D-81; 0040)
-- =============================================================================
-- A project's issues, numbered from projects.next_issue_number. Events from
-- the git_events outbox and deploys going live move status forward
-- (issue_events says how); people move it any way. search serves ⌘K.
CREATE TABLE IF NOT EXISTS issues (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The agent connection that made it, acting for its person (0049, D-102).
    agent_connection_id UUID,
    project_id        UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number            INTEGER     NOT NULL CHECK (number > 0),
    title             TEXT        NOT NULL CHECK (char_length(title) >= 1 AND char_length(title) <= 256 AND title !~ '[[:cntrl:]]'),
    description       TEXT        NOT NULL DEFAULT '' CHECK (octet_length(description) <= 65536),
    status            TEXT        NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog', 'todo', 'in_progress', 'in_review', 'shipped', 'cancelled')),
    priority          TEXT        NOT NULL DEFAULT 'none' CHECK (priority IN ('none', 'low', 'medium', 'high', 'urgent')),
    assignee_id       UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_by        UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status_changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    search            TSVECTOR    GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED,
    -- Its type (NULL: shown as the workspace's Task) and its parent, an Epic
    -- or Release, or the issue a sub-task belongs to (D-83; 0041); their
    -- foreign keys follow issue_types below.
    type_id           UUID,
    parent_id         UUID,
    -- The failed deploy it was opened from (D-88; 0043); its foreign key
    -- follows deployments.
    from_deployment_id UUID,
    -- Its cycle (its foreign key follows cycles), the dates it starts and
    -- is due (a container's start and target) and its estimate in points
    -- (D-90; 0044).
    cycle_id          UUID,
    start_on          DATE,
    due_on            DATE,
    estimate          SMALLINT    CHECK (estimate BETWEEN 1 AND 100),
    UNIQUE (project_id, number),
    CONSTRAINT issues_parent_not_self CHECK (parent_id <> id),
    CONSTRAINT issues_dates_in_order CHECK (start_on <= due_on)
);
CREATE INDEX IF NOT EXISTS idx_issues_project_status ON issues (project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues (assignee_id, status) WHERE assignee_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issues_search ON issues USING GIN (search);

-- An issue's labels: lower case, free text per project (D-81). A label
-- named after one of the project's services links the issue to it.
CREATE TABLE IF NOT EXISTS issue_labels (
    issue_id UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    name     TEXT NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 40 AND name = lower(btrim(name)) AND name !~ '[[:cntrl:],]'),
    PRIMARY KEY (issue_id, name)
);
CREATE INDEX IF NOT EXISTS idx_issue_labels_name ON issue_labels (name, issue_id);

CREATE TABLE IF NOT EXISTS issue_comments (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The agent connection that made it, acting for its person (0049, D-102).
    agent_connection_id UUID,
    issue_id   UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author_id  UUID        REFERENCES users(id) ON DELETE SET NULL,
    body       TEXT        NOT NULL CHECK (char_length(body) >= 1 AND octet_length(body) <= 65536),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_issue_comments_issue ON issue_comments (issue_id, created_at, id);

-- An issue's history. automatic marks what an event did (git_event_id, or
-- deployment_id for a deploy going live), with the git event's actor; one
-- row per issue, git event and kind, so applying an event twice adds
-- nothing. from_value and to_value are the field's values (a status, a
-- priority, an assignee's id, labels joined by commas, a title).
CREATE TABLE IF NOT EXISTS issue_events (
    id              BIGSERIAL   PRIMARY KEY,
    issue_id        UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged',
                                                         'type', 'parent', 'related', 'unrelated', 'unblocked', 'cloned', 'deploy_failed',
                                                         'cycle', 'start', 'due', 'estimate')),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    automatic       BOOLEAN     NOT NULL DEFAULT false,
    from_value      TEXT        CHECK (char_length(from_value) <= 300),
    to_value        TEXT        CHECK (char_length(to_value) <= 300),
    git_event_id    BIGINT      REFERENCES git_events(id) ON DELETE SET NULL,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE SET NULL,
    deployment_id   UUID        REFERENCES deployments(id) ON DELETE SET NULL,
    branch          TEXT        CHECK (char_length(branch) BETWEEN 1 AND 255),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_issue_events_issue ON issue_events (issue_id, created_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_events_git_event ON issue_events (issue_id, git_event_id, kind) WHERE git_event_id IS NOT NULL;

-- The branches and pull requests that name an issue's key (D-81), found by
-- the issues module in the git_events outbox: a branch by its name, a pull
-- request by its head branch, title or description.
CREATE TABLE IF NOT EXISTS issue_links (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id        UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    repository_id   UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE CASCADE,
    branch          TEXT        CHECK (char_length(branch) BETWEEN 1 AND 255),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((pull_request_id IS NULL) <> (branch IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_links_pull ON issue_links (issue_id, pull_request_id) WHERE pull_request_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_links_branch ON issue_links (issue_id, repository_id, branch) WHERE branch IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issue_links_pull ON issue_links (pull_request_id) WHERE pull_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issue_links_branch ON issue_links (repository_id, branch) WHERE branch IS NOT NULL;

-- A workspace's issue types (D-83; 0041): Task, Bug, Feature, Epic, Release
-- and Sub-task until its owners change them. kind is what a type does:
-- standard, container (children, progress, shipped with them) or subtask
-- (always has a parent). Archived, never deleted: issues keep their type.
CREATE TABLE IF NOT EXISTS issue_types (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 40 AND name !~ '[[:cntrl:]]'),
    kind        TEXT        NOT NULL CHECK (kind IN ('standard', 'container', 'subtask')),
    icon        TEXT        NOT NULL CHECK (icon IN ('task', 'bug', 'feature', 'epic', 'release', 'subtask', 'story', 'chore', 'incident', 'spike')),
    color       TEXT        NOT NULL CHECK (color IN ('gray', 'blue', 'red', 'green', 'violet', 'amber', 'teal')),
    position    INTEGER     NOT NULL DEFAULT 0,
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, name)
);
-- NO ACTION: a type is archived, never deleted, while issues have it.
ALTER TABLE issues ADD CONSTRAINT fk_issues_type FOREIGN KEY (type_id) REFERENCES issue_types(id);
ALTER TABLE issues ADD CONSTRAINT fk_issues_parent FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE SET NULL;
ALTER TABLE projects ADD CONSTRAINT fk_projects_default_issue_type FOREIGN KEY (default_issue_type_id) REFERENCES issue_types(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_issues_parent ON issues (parent_id) WHERE parent_id IS NOT NULL;
ALTER TABLE issues ADD CONSTRAINT fk_issues_from_deployment FOREIGN KEY (from_deployment_id) REFERENCES deployments(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_issues_from_deployment ON issues (from_deployment_id) WHERE from_deployment_id IS NOT NULL;

-- The workspace's types a project doesn't use (D-83): a type added later
-- shows in every project until one hides it.
CREATE TABLE IF NOT EXISTS project_hidden_issue_types (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type_id    UUID NOT NULL REFERENCES issue_types(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, type_id)
);

-- Relations between two issues of a workspace (D-83): from blocks, relates
-- to, duplicates or clones to. relates is one row whichever way it was
-- made; parent and children are issues.parent_id.
CREATE TABLE IF NOT EXISTS issue_relations (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    from_issue_id UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    to_issue_id   UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind IN ('blocks', 'relates', 'duplicates', 'clones')),
    created_by    UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (from_issue_id <> to_issue_id),
    UNIQUE (from_issue_id, to_issue_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_issue_relations_to ON issue_relations (to_issue_id, kind);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_relations_relates ON issue_relations (LEAST(from_issue_id, to_issue_id), GREATEST(from_issue_id, to_issue_id)) WHERE kind = 'relates';

-- A project's former keys (D-83): STO-12 still opens, links and moves its
-- issue after the project's key became VLB. A key is the workspace's once,
-- as a project's or an alias (checked when a key is set).
CREATE TABLE IF NOT EXISTS issue_key_aliases (
    org_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    key        TEXT        NOT NULL CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
    project_id UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, key)
);
CREATE INDEX IF NOT EXISTS idx_issue_key_aliases_project ON issue_key_aliases (project_id);

-- Saved views of a project's issues, shared with the project (D-81): the
-- list's filters, grouping and order as the panel's URL holds them.
CREATE TABLE IF NOT EXISTS issue_views (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_id   UUID        REFERENCES users(id) ON DELETE SET NULL,
    name       TEXT        NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 60 AND name !~ '[[:cntrl:]]'),
    filters    JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(filters) = 'object' AND octet_length(filters::text) <= 4096),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, name)
);

-- =============================================================================
-- CYCLES (D-90; 0044)
-- =============================================================================
-- A workspace's cycles, numbered from 1, made by the issues module from the
-- workspace's schedule (organizations.cycle_*): the current one and the next
-- two. ends_on is its last day. closed_at is set when it ended and its
-- unfinished issues moved to the next one, with what it held then.
CREATE TABLE IF NOT EXISTS cycles (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    number               INTEGER     NOT NULL CHECK (number > 0),
    name                 TEXT        NOT NULL DEFAULT '' CHECK (char_length(name) <= 60 AND name !~ '[[:cntrl:]]'),
    starts_on            DATE        NOT NULL,
    ends_on              DATE        NOT NULL,
    closed_at            TIMESTAMPTZ,
    final_scope          INTEGER     CHECK (final_scope >= 0),
    final_shipped        INTEGER     CHECK (final_shipped >= 0),
    final_cancelled      INTEGER     CHECK (final_cancelled >= 0),
    final_carried        INTEGER     CHECK (final_carried >= 0),
    final_points         INTEGER     CHECK (final_points >= 0),
    final_shipped_points INTEGER     CHECK (final_shipped_points >= 0),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, number),
    CHECK (ends_on >= starts_on)
);
ALTER TABLE issues ADD CONSTRAINT fk_issues_cycle FOREIGN KEY (cycle_id) REFERENCES cycles(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_issues_cycle ON issues (cycle_id) WHERE cycle_id IS NOT NULL;

-- The issues a cycle moved to the next one when it ended.
CREATE TABLE IF NOT EXISTS cycle_carryovers (
    cycle_id    UUID NOT NULL REFERENCES cycles(id) ON DELETE CASCADE,
    issue_id    UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    to_cycle_id UUID NOT NULL REFERENCES cycles(id) ON DELETE CASCADE,
    PRIMARY KEY (cycle_id, issue_id)
);
CREATE INDEX IF NOT EXISTS idx_cycle_carryovers_to ON cycle_carryovers (to_cycle_id, issue_id);

-- A cycle's counts at the end of each day it ran, for its chart: scope
-- leaves cancelled issues out; started is in progress, in review or
-- shipped. Today's row is rewritten until the day ends.
CREATE TABLE IF NOT EXISTS cycle_days (
    cycle_id       UUID    NOT NULL REFERENCES cycles(id) ON DELETE CASCADE,
    day            DATE    NOT NULL,
    scope          INTEGER NOT NULL CHECK (scope >= 0),
    started        INTEGER NOT NULL CHECK (started >= 0),
    shipped        INTEGER NOT NULL CHECK (shipped >= 0),
    scope_points   INTEGER NOT NULL DEFAULT 0 CHECK (scope_points >= 0),
    started_points INTEGER NOT NULL DEFAULT 0 CHECK (started_points >= 0),
    shipped_points INTEGER NOT NULL DEFAULT 0 CHECK (shipped_points >= 0),
    PRIMARY KEY (cycle_id, day)
);

-- =============================================================================
-- DOCS (D-81, D-86; 0042)
-- =============================================================================
-- A project's pages, in a tree by parent_id and position: markdown with
-- Toft's blocks fenced in it (toft-service, toft-run, toft-issue,
-- toft-pull). title and body are the current version's, which is version;
-- verified_at is the last Mark as verified. A deleted page stays 30 days
-- (deleted_at) and comes back with its versions. search serves ⌘K and the
-- tree's search.
CREATE TABLE IF NOT EXISTS doc_pages (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_id   UUID        REFERENCES doc_pages(id) ON DELETE CASCADE,
    position    INTEGER     NOT NULL DEFAULT 0,
    title       TEXT        NOT NULL CHECK (char_length(title) >= 1 AND char_length(title) <= 200 AND title !~ '[[:cntrl:]]'),
    body        TEXT        NOT NULL DEFAULT '' CHECK (octet_length(body) <= 262144),
    version     INTEGER     NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    verified_at TIMESTAMPTZ,
    verified_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    deleted_at  TIMESTAMPTZ,
    search      TSVECTOR    GENERATED ALWAYS AS (setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('simple', body), 'B')) STORED,
    CONSTRAINT doc_pages_parent_not_self CHECK (parent_id <> id)
);
CREATE INDEX IF NOT EXISTS idx_doc_pages_project ON doc_pages (project_id, parent_id, position);
CREATE INDEX IF NOT EXISTS idx_doc_pages_search ON doc_pages USING GIN (search);
CREATE INDEX IF NOT EXISTS idx_doc_pages_deleted ON doc_pages (deleted_at) WHERE deleted_at IS NOT NULL;

-- Every version of a page (D-86), the current one included.
CREATE TABLE IF NOT EXISTS doc_page_versions (
    page_id    UUID        NOT NULL REFERENCES doc_pages(id) ON DELETE CASCADE,
    version    INTEGER     NOT NULL CHECK (version > 0),
    title      TEXT        NOT NULL CHECK (char_length(title) >= 1 AND char_length(title) <= 200 AND title !~ '[[:cntrl:]]'),
    body       TEXT        NOT NULL CHECK (octet_length(body) <= 262144),
    author_id  UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (page_id, version)
);

-- The services a page's blocks name (D-86), each with what the page last
-- saw of it: its state when the page was verified, or when a save first
-- named it. The page is out of date once the service's state differs
-- (internal/doc says what counts).
CREATE TABLE IF NOT EXISTS doc_page_services (
    page_id    UUID        NOT NULL REFERENCES doc_pages(id) ON DELETE CASCADE,
    service_id UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    seen       JSONB       NOT NULL CHECK (jsonb_typeof(seen) = 'object' AND octet_length(seen::text) <= 65536),
    seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (page_id, service_id)
);
CREATE INDEX IF NOT EXISTS idx_doc_page_services_service ON doc_page_services (service_id);

-- The issues, pull requests and pages a page's current version names
-- (D-86): their Referenced in docs, and a page's Referenced by.
CREATE TABLE IF NOT EXISTS doc_refs (
    page_id         UUID NOT NULL REFERENCES doc_pages(id) ON DELETE CASCADE,
    issue_id        UUID REFERENCES issues(id) ON DELETE CASCADE,
    pull_request_id UUID REFERENCES pull_requests(id) ON DELETE CASCADE,
    to_page_id      UUID REFERENCES doc_pages(id) ON DELETE CASCADE,
    CONSTRAINT doc_refs_one_target CHECK (num_nonnulls(issue_id, pull_request_id, to_page_id) = 1)
);
CREATE INDEX IF NOT EXISTS idx_doc_refs_page ON doc_refs (page_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_doc_refs_issue ON doc_refs (issue_id, page_id) WHERE issue_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_doc_refs_pull ON doc_refs (pull_request_id, page_id) WHERE pull_request_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_doc_refs_page ON doc_refs (to_page_id, page_id) WHERE to_page_id IS NOT NULL;

-- =============================================================================
-- INBOX (D-88; 0043)
-- =============================================================================
-- A person's updates: what happened on work they opened, are assigned,
-- commented on or were mentioned in, written by the inbox's notifier from
-- the issues', pull requests' and docs' own records (never for the person's
-- own actions). Kept 90 days. Needs you isn't stored: it is computed when
-- read.
CREATE TABLE IF NOT EXISTS notifications (
    id              BIGSERIAL   PRIMARY KEY,
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id          UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('issue_status', 'issue_assigned', 'issue_comment', 'mention',
                                                         'pull_merged', 'pull_closed', 'pull_comment', 'page_edited')),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    project_id      UUID        REFERENCES projects(id) ON DELETE CASCADE,
    issue_id        UUID        REFERENCES issues(id) ON DELETE CASCADE,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE CASCADE,
    page_id         UUID        REFERENCES doc_pages(id) ON DELETE CASCADE,
    detail          JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object' AND octet_length(detail::text) <= 4096),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at         TIMESTAMPTZ,
    done_at         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications (user_id, org_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications (user_id, org_id) WHERE read_at IS NULL AND done_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_notifications_created ON notifications (created_at);

-- The Needs you items a person marked done (D-88). An item's key names its
-- version (a deploy, an alert, a page's last verification), so a new one
-- comes back. Kept 30 days.
CREATE TABLE IF NOT EXISTS inbox_done (
    user_id  UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT        NOT NULL CHECK (char_length(item_key) >= 1 AND char_length(item_key) <= 200),
    done_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, item_key)
);

-- How far the notifier has read each of its sources (D-88): the last
-- row's time and id, so a restart neither repeats nor loses an update.
CREATE TABLE IF NOT EXISTS inbox_cursors (
    source     TEXT        PRIMARY KEY CHECK (source IN ('issue_events', 'issue_comments', 'pull_requests',
                                                         'pull_request_events', 'pull_request_comments', 'doc_page_versions')),
    at         TIMESTAMPTZ NOT NULL,
    last_id    TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Shared servers: a server's owner workspace lets another workspace deploy
-- to it, within a capacity share (private/specs/2026-10-12-shared-servers-design.md).
CREATE TABLE IF NOT EXISTS server_shares (
    server_id  UUID        NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    org_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    cpu_cores  NUMERIC     NOT NULL CHECK (cpu_cores > 0),
    memory_mb  BIGINT      NOT NULL CHECK (memory_mb > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (server_id, org_id)
);
CREATE INDEX IF NOT EXISTS idx_server_shares_org ON server_shares(org_id);

-- Shared volumes (D-38, private/specs/2026-10-12-shared-volumes-design.md):
-- storage the platform creates and names, owned by a workspace, which its
-- services on the same server mount. The Docker volume is paas-shared-<id>.
CREATE TABLE IF NOT EXISTS shared_volumes (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- NULL is the control-plane server.
    server_id        UUID        REFERENCES servers(id) ON DELETE RESTRICT,
    name             TEXT        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    size_limit_bytes BIGINT      NOT NULL CHECK (size_limit_bytes > 0),
    used_bytes       BIGINT      CHECK (used_bytes >= 0),  -- NULL until first measured
    used_at          TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, name)
);

-- Which services mount which shared volume, and where. RESTRICT: a mounted
-- volume cannot be deleted.
CREATE TABLE IF NOT EXISTS service_shared_volumes (
    service_id       UUID    NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    shared_volume_id UUID    NOT NULL REFERENCES shared_volumes(id) ON DELETE RESTRICT,
    target           TEXT    NOT NULL CHECK (target ~ '^/' AND target !~ '(^|/)\.\.(/|$)'),
    read_only        BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (service_id, target)
);
CREATE INDEX IF NOT EXISTS idx_service_shared_volumes_volume ON service_shared_volumes(shared_volume_id);

-- =============================================================================
-- ALERTS (internal/alert)
-- A workspace's webhook, and the alerts that are open now. An alert fires
-- once (one open row per kind and subject) and resolves once; deploy_failed
-- is never stored, as it never resolves.
-- =============================================================================
CREATE TABLE IF NOT EXISTS alert_channels (
    org_id         UUID        PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    webhook_url    TEXT        NOT NULL CHECK (webhook_url ~ '^https://' AND length(webhook_url) <= 2048),
    signing_secret TEXT        NOT NULL,  -- encrypted with ENCRYPTION_KEY
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    -- The last message posted (an alert or Send test) and what the receiver
    -- answered: its HTTP status, NULL when it gave none, and the error.
    last_delivery_at     TIMESTAMPTZ,
    last_delivery_status INT CHECK (last_delivery_status BETWEEN 100 AND 599),
    last_delivery_error  TEXT NOT NULL DEFAULT '' CHECK (length(last_delivery_error) <= 500),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- subject is a service ID, a server ID, a shared volume ID, or
-- 'control-plane'. A failed deploy and a memory kill are one message each:
-- their rows are resolved as they fire, so Settings → Alerts lists them.
CREATE TABLE IF NOT EXISTS alerts (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        TEXT        NOT NULL CHECK (kind IN ('service_down', 'service_restarting',
                    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
                    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
                    'panel_container', 'panel_certificate', 'panel_upgrade_failed', 'mirror_failing',
                    'agent_offline', 'agent_frozen', 'security_updates', 'reboot_required',
                    'docker_advisory')),
    subject     TEXT        NOT NULL,
    detail      TEXT        NOT NULL,
    fired_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    CONSTRAINT alerts_resolved_after_fired CHECK (resolved_at IS NULL OR resolved_at >= fired_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS alerts_one_open ON alerts (kind, subject) WHERE resolved_at IS NULL;

-- =============================================================================
-- Agents over MCP with OAuth (0048, D-100). Toft is the authorization server:
-- an agent (an OAuth client) connects as a person, to one workspace, with the
-- abilities the person allowed, and acts under the workspace's agent policy.
-- =============================================================================

-- OAuth clients: a Client ID Metadata Document's URL ('cimd', fetched and
-- cached) or a dynamically registered public client ('dcr').
CREATE TABLE IF NOT EXISTS oauth_clients (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id     TEXT        NOT NULL UNIQUE,
    kind          TEXT        NOT NULL CHECK (kind IN ('cimd', 'dcr')),
    name          TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    redirect_uris TEXT[]      NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 10),
    -- Registration needs no account: ten URIs of at most 2048 characters.
    CONSTRAINT oauth_clients_redirect_uris_size CHECK (length(array_to_string(redirect_uris, '')) <= 20480),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- When a CIMD client's document was last fetched (it is refetched after
    -- a day); a DCR client's registration.
    fetched_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A connection is one consent: who, which workspace, which projects, what.
-- It ends at expires_at (90 days) or when revoked; its tokens end with it.
CREATE TABLE IF NOT EXISTS agent_connections (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id    UUID        NOT NULL REFERENCES oauth_clients(id),
    -- The client's name when the person allowed it.
    name         TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- Every project of the workspace, new ones included; otherwise those in
    -- agent_connection_projects.
    all_projects BOOLEAN     NOT NULL,
    scopes       TEXT[]      NOT NULL CHECK (scopes <@ ARRAY['read', 'deploy', 'code', 'issues', 'variables', 'runbooks']::TEXT[]
                                             AND 'read' = ANY (scopes)),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    revoked_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    last_used_at TIMESTAMPTZ,
    last_tool    TEXT,
    CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_agent_connections_org ON agent_connections (org_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_connections_client ON agent_connections (client_id);

CREATE TABLE IF NOT EXISTS agent_connection_projects (
    connection_id UUID NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    PRIMARY KEY (connection_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_connection_projects_project ON agent_connection_projects (project_id);

-- Authorization codes: single use, 10 minutes, PKCE S256. The consent's
-- choices ride on the code until the token exchange makes the connection.
CREATE TABLE IF NOT EXISTS oauth_codes (
    code_hash      TEXT        PRIMARY KEY,
    client_id      UUID        NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    user_id        UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id         UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    all_projects   BOOLEAN     NOT NULL,
    project_ids    UUID[]      NOT NULL DEFAULT '{}',
    scopes         TEXT[]      NOT NULL,
    redirect_uri   TEXT        NOT NULL,
    code_challenge TEXT        NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    used_at        TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (all_projects OR cardinality(project_ids) > 0)
);

-- Access tokens (1 hour) and refresh tokens (rotated; a reused one revokes
-- its connection), stored as SHA-256 hashes.
CREATE TABLE IF NOT EXISTS agent_tokens (
    token_hash    TEXT        PRIMARY KEY,
    connection_id UUID        NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind IN ('access', 'refresh')),
    expires_at    TIMESTAMPTZ NOT NULL,
    -- A refresh token's use (rotation): a second use is a stolen token.
    used_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (kind = 'refresh' OR used_at IS NULL)
);
CREATE INDEX IF NOT EXISTS idx_agent_tokens_connection ON agent_tokens (connection_id);
CREATE INDEX IF NOT EXISTS idx_agent_tokens_expires ON agent_tokens (expires_at);

-- The workspace's policy: a missing row is the default (agent.DefaultPolicy).
CREATE TABLE IF NOT EXISTS agent_policies (
    org_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    action     TEXT        NOT NULL CHECK (action IN ('deploy', 'code', 'issues', 'variables', 'secrets', 'runbooks')),
    env_class  TEXT        NOT NULL CHECK (env_class IN ('preview', 'other', 'protected', 'any')),
    decision   TEXT        NOT NULL CHECK (decision IN ('allow', 'ask', 'never')),
    updated_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, action, env_class)
);

-- A project's own rules (0049, D-101, D-102): a cell here replaces the
-- workspace's for that project; a missing one follows the workspace. Its own
-- table, not agent_policies.project_id: v0.15.0 reads every row of a
-- workspace's policy as its own.
CREATE TABLE IF NOT EXISTS agent_project_policies (
    project_id UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    action     TEXT        NOT NULL CHECK (action IN ('deploy', 'code', 'issues', 'variables', 'secrets', 'runbooks')),
    env_class  TEXT        NOT NULL CHECK (env_class IN ('preview', 'other', 'protected', 'any')),
    decision   TEXT        NOT NULL CHECK (decision IN ('allow', 'ask', 'never')),
    updated_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, action, env_class)
);

-- Ask a person: an agent's call waiting for an owner or developer. request
-- is the operation Approve runs (method, path, body); it never holds a
-- secret: set_secret's value is in sealed, encrypted with ENCRYPTION_KEY,
-- until the request is run, turned down or past its time (0049, D-102). A
-- waiting request past expires_at has expired.
CREATE TABLE IF NOT EXISTS agent_approvals (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connection_id  UUID        NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    user_id        UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action         TEXT        NOT NULL CHECK (action IN ('deploy', 'code', 'issues', 'variables', 'secrets', 'runbooks')),
    tool           TEXT        NOT NULL,
    service_id     UUID        REFERENCES services(id) ON DELETE CASCADE,
    environment_id UUID        REFERENCES environments(id) ON DELETE CASCADE,
    project_id     UUID        REFERENCES projects(id) ON DELETE CASCADE,
    target         TEXT        NOT NULL,
    request        JSONB       NOT NULL,
    sealed         TEXT,
    request_hash   TEXT        NOT NULL,
    reason         TEXT        NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    plan           TEXT        NOT NULL DEFAULT '' CHECK (length(plan) <= 4000),
    -- The plan in steps, and the rollback an approved deploy covers (0062, D-146).
    plan_steps     JSONB       NOT NULL DEFAULT '[]' CONSTRAINT agent_approvals_plan_steps_check CHECK (jsonb_typeof(plan_steps) = 'array'),
    rollback_to    UUID        REFERENCES deployments(id) ON DELETE SET NULL,
    deployment_id  UUID        REFERENCES deployments(id) ON DELETE SET NULL,
    rollback_used_at TIMESTAMPTZ,
    state          TEXT        NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'approved', 'denied', 'cancelled')),
    expires_at     TIMESTAMPTZ NOT NULL,
    decided_by     UUID        REFERENCES users(id) ON DELETE SET NULL,
    decided_at     TIMESTAMPTZ,
    note           TEXT        NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    executed_at    TIMESTAMPTZ,
    result         JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((state = 'waiting') = (decided_at IS NULL)),
    CHECK (executed_at IS NULL OR state = 'approved'),
    CONSTRAINT agent_approvals_sealed_check CHECK (sealed IS NULL OR action = 'secrets')
);
CREATE INDEX IF NOT EXISTS idx_agent_approvals_waiting ON agent_approvals (org_id, created_at) WHERE state = 'waiting';
CREATE INDEX IF NOT EXISTS idx_agent_approvals_connection ON agent_approvals (connection_id, request_hash);
CREATE INDEX IF NOT EXISTS idx_agent_approvals_sealed ON agent_approvals (expires_at) WHERE sealed IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_approvals_rollback_to ON agent_approvals (rollback_to) WHERE rollback_to IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_approvals_deployment ON agent_approvals (deployment_id) WHERE deployment_id IS NOT NULL;

-- Branches an agent made (0049, D-102): only that connection pushes to one,
-- over HTTPS with token_hash's credential (toft_agt_, 24 hours, a new one
-- from create_branch on the same branch).
CREATE TABLE IF NOT EXISTS agent_branches (
    repository_id    UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    name             TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    connection_id    UUID        NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    user_id          UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash       TEXT        NOT NULL UNIQUE,
    token_expires_at TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (repository_id, name)
);
CREATE INDEX IF NOT EXISTS idx_agent_branches_connection ON agent_branches (connection_id);

ALTER TABLE pull_requests ADD CONSTRAINT fk_pull_requests_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE pull_request_comments ADD CONSTRAINT fk_pull_request_comments_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE git_events ADD CONSTRAINT fk_git_events_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE issues ADD CONSTRAINT fk_issues_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE issue_comments ADD CONSTRAINT fk_issue_comments_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE env_vars ADD CONSTRAINT fk_env_vars_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE environment_env_vars ADD CONSTRAINT fk_environment_env_vars_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;


-- =============================================================================
-- AUDIT LOG
-- =============================================================================
CREATE TABLE IF NOT EXISTS audit_log (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID        NOT NULL REFERENCES organizations(id),
    -- NULL when Toft itself acted, as an automatic disk clean (D-135).
    actor_id   UUID        REFERENCES users(id),
    action     TEXT        NOT NULL,
    resource   TEXT,
    metadata   JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Who acted through what (0048, D-100): the agent connection acting for
    -- actor_id, or the API key it used. A key's ID stays after the key is
    -- revoked (its row is deleted), so it carries no foreign key: it is a
    -- record of what happened, like resource.
    agent_connection_id UUID REFERENCES agent_connections(id) ON DELETE SET NULL,
    api_key_id          UUID
);

-- =============================================================================
-- SERVICE METRICS
-- =============================================================================
CREATE TABLE IF NOT EXISTS service_metrics (
    id           BIGSERIAL    PRIMARY KEY,
    service_id   UUID         NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    cpu_percent  NUMERIC(6,2) NOT NULL,
    memory_mb    NUMERIC(10,2) NOT NULL,
    compose_service TEXT          NOT NULL DEFAULT '',
    memory_peak_mb  NUMERIC(10,2),
    throttled_pct   NUMERIC(5,2)  CHECK (throttled_pct BETWEEN 0 AND 100),
    cpu_wait_pct    NUMERIC(5,2)  CHECK (cpu_wait_pct BETWEEN 0 AND 100),
    collected_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    -- The compose service's containers' restarts since the previous sample
    -- (Docker's RestartCount, compared per container by the collector);
    -- NULL when one's count was not read (rows from before 0069, an agent
    -- that predates it).
    restarts        INT           CHECK (restarts >= 0)
);

-- Resource limits (D-41, private/specs/2026-10-12-resource-limits-design.md)
CREATE TABLE IF NOT EXISTS service_compose_limits (
    service_id      UUID    NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service TEXT    NOT NULL CHECK (compose_service <> ''),
    memory_mb       BIGINT  NOT NULL CHECK (memory_mb >= 64),
    cpu_cores       NUMERIC CHECK (cpu_cores IS NULL OR cpu_cores > 0),
    PRIMARY KEY (service_id, compose_service)
);

CREATE TABLE IF NOT EXISTS service_oom_kills (
    id              BIGSERIAL   PRIMARY KEY,
    service_id      UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service TEXT        NOT NULL DEFAULT '',
    container       TEXT        NOT NULL,
    killed_at       TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS service_usage_daily (
    service_id        UUID    NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service   TEXT    NOT NULL DEFAULT '',
    day               DATE    NOT NULL,
    samples           INT     NOT NULL CHECK (samples >= 0),
    memory_peak_mb    NUMERIC(10,2) NOT NULL,
    memory_p95_mb     NUMERIC(10,2) NOT NULL,
    cpu_p95_percent   NUMERIC(7,2)  NOT NULL,
    throttled_max_pct NUMERIC(5,2),
    cpu_wait_avg_pct  NUMERIC(5,2),
    oom_kills         INT     NOT NULL DEFAULT 0 CHECK (oom_kills >= 0),
    PRIMARY KEY (service_id, compose_service, day)
);

-- The daily figures of the summed samples (0070), what the Service page's
-- 7d range draws, so a day means what a minute does at 1h and 24h: per
-- compose service its containers summed, and under compose_service '' the
-- whole service (a stack's compose services summed at each sample). Unlike
-- service_usage_daily's memory_peak_mb (the largest single container, which
-- a per-container limit is sized against). A day summarised before 0070 has
-- no row here; 7d marks it as the largest container's.
CREATE TABLE IF NOT EXISTS service_usage_daily_sums (
    service_id      UUID          NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service TEXT          NOT NULL DEFAULT '',
    day             DATE          NOT NULL,
    memory_peak_mb  NUMERIC(10,2) NOT NULL CHECK (memory_peak_mb >= 0),
    cpu_p95_percent NUMERIC(7,2)  NOT NULL CHECK (cpu_p95_percent >= 0),
    PRIMARY KEY (service_id, compose_service, day)
);

-- A routed service's requests per minute, from its server's Traefik (D-156;
-- 0076): written by the metrics collector for every running service with a
-- domain, kept 48 hours. buckets are the minute's requests per bucket of
-- hoststats.LatencyBounds, not cumulative, the last one above the top
-- bound; ponytail: the bounds live in code, so rows of another length
-- (other bounds) are skipped by their length, not converted. A minute the
-- server's Traefik could not be read has no buckets and says why in
-- unmeasured: 'agent' (an agent older than the latency section), 'adopted'
-- (a Traefik Toft did not start), 'traefik' (Toft's, without the buckets).
-- The hourly GC deletes by collected_at alone, a scan of a table this size.
CREATE TABLE IF NOT EXISTS service_latency (
    service_id   UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    collected_at TIMESTAMPTZ NOT NULL,
    buckets      INT[]       NOT NULL,
    unmeasured   TEXT        NOT NULL DEFAULT '' CHECK (unmeasured IN ('', 'agent', 'adopted', 'traefik')),
    CONSTRAINT service_latency_unmeasured_empty CHECK (unmeasured = '' OR cardinality(buckets) = 0),
    PRIMARY KEY (service_id, collected_at)
);

CREATE TABLE IF NOT EXISTS server_usage_daily (
    server_id        UUID    NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    day              DATE    NOT NULL,
    samples          INT     NOT NULL CHECK (samples >= 0),
    cpu_wait_sum_pct NUMERIC(12,2) NOT NULL,
    cpu_wait_max_pct NUMERIC(5,2)  NOT NULL,
    PRIMARY KEY (server_id, day)
);

-- =============================================================================
-- INDEXES
-- =============================================================================
CREATE INDEX IF NOT EXISTS idx_projects_user_id              ON projects(user_id);
CREATE INDEX IF NOT EXISTS idx_projects_org_id               ON projects(org_id);
CREATE INDEX IF NOT EXISTS idx_environments_project_id       ON environments(project_id);
CREATE INDEX IF NOT EXISTS idx_services_environment_id       ON services(environment_id);
CREATE INDEX IF NOT EXISTS idx_services_server_id            ON services(server_id);
CREATE INDEX IF NOT EXISTS idx_services_runtime_status       ON services(runtime_status);
CREATE INDEX IF NOT EXISTS idx_services_project_id           ON services(project_id);
CREATE INDEX IF NOT EXISTS idx_services_scope                ON services(scope);
CREATE INDEX IF NOT EXISTS idx_services_scoped_org_id        ON services(scoped_org_id);

-- Partial unique indexes replace the old UNIQUE(environment_id, name) constraint.
-- Each index covers one scope so names are unique within their deployment context.
CREATE UNIQUE INDEX IF NOT EXISTS uq_services_name_env
    ON services(environment_id, name) WHERE environment_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_services_name_project
    ON services(project_id, name)
    WHERE project_id IS NOT NULL AND environment_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_services_name_org
    ON services(scoped_org_id, name)
    WHERE scoped_org_id IS NOT NULL AND environment_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_deployments_service_id        ON deployments(service_id);
CREATE INDEX IF NOT EXISTS idx_deployments_status            ON deployments(status);
CREATE INDEX IF NOT EXISTS idx_deployment_logs_deployment_id ON deployment_logs(deployment_id);
CREATE INDEX IF NOT EXISTS idx_domains_service_id            ON domains(service_id);
CREATE INDEX IF NOT EXISTS idx_env_vars_service_id           ON env_vars(service_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_user_id              ON api_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash             ON api_keys(key_hash);
-- A project's keys, newest first (0077), and the project delete's cascade.
CREATE INDEX IF NOT EXISTS idx_api_keys_project ON api_keys (project_id, created_at DESC, id DESC) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_org_members_org_id            ON org_members(org_id);
CREATE INDEX IF NOT EXISTS idx_org_members_user_id           ON org_members(user_id);
CREATE INDEX IF NOT EXISTS idx_org_invites_token             ON org_invites(token);
-- The audit log list: WHERE org_id = $1 ORDER BY created_at DESC LIMIT n (0025).
CREATE INDEX IF NOT EXISTS idx_audit_log_org_created         ON audit_log(org_id, created_at DESC);
-- The audit log's Agent filter (D-100).
CREATE INDEX IF NOT EXISTS idx_audit_log_agent ON audit_log (org_id, agent_connection_id, created_at DESC) WHERE agent_connection_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_instance_audit_log_created    ON instance_audit_log(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_servers_org_id                ON servers(org_id);
CREATE INDEX IF NOT EXISTS idx_service_metrics_service_time  ON service_metrics(service_id, collected_at DESC);
-- SummariseUsageDays and DeleteServiceMetricsBefore scan by time.
CREATE INDEX IF NOT EXISTS idx_service_metrics_collected_at ON service_metrics(collected_at);
-- CountOOMKillsSince and the daily summary read one service's kills by time.
CREATE INDEX IF NOT EXISTS idx_service_oom_kills_service_time ON service_oom_kills(service_id, killed_at);
-- One row per kill: the collector's event windows overlap at their edges and
-- after a restart, and InsertOOMKill skips a kill already recorded.
CREATE UNIQUE INDEX IF NOT EXISTS uq_service_oom_kills_kill ON service_oom_kills(service_id, container, killed_at);
-- DeleteUsageBefore (the primary key leads with service_id).
CREATE INDEX IF NOT EXISTS idx_service_usage_daily_day ON service_usage_daily(day);

-- =============================================================================
-- SERVICE CONFIG FILES
-- =============================================================================
CREATE TABLE IF NOT EXISTS service_config_files (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id  UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    path        TEXT        NOT NULL,
    content     TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (service_id, path),
    CONSTRAINT service_config_files_content_size CHECK (length(content) <= 1048576)
);

CREATE INDEX IF NOT EXISTS idx_config_files_service_id ON service_config_files(service_id);

-- =============================================================================
-- OBSERVABILITY CONFIGS
-- =============================================================================
CREATE TABLE IF NOT EXISTS observability_configs (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id            UUID        REFERENCES projects(id) ON DELETE CASCADE,
    -- NULL project_id = org-scoped config
    loki_service_id       UUID        REFERENCES services(id) ON DELETE SET NULL,
    grafana_service_id    UUID        REFERENCES services(id) ON DELETE SET NULL,
    prometheus_service_id UUID        REFERENCES services(id) ON DELETE SET NULL,
    loki_url              TEXT,   -- e.g. "http://loki:3100" (internal network alias)
    grafana_url           TEXT,   -- public domain URL once domains are wired
    prometheus_url        TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- One row per project, and one workspace-level row (project_id NULL):
    -- NULLS NOT DISTINCT, or two NULLs never conflict (0028).
    UNIQUE NULLS NOT DISTINCT (org_id, project_id)
);

CREATE INDEX IF NOT EXISTS idx_observability_org_id     ON observability_configs(org_id);
CREATE INDEX IF NOT EXISTS idx_observability_project_id ON observability_configs(project_id);

-- A catalog app's admin password (or temporary login), kept sealed for the
-- person who created the service or reset it, until they dismiss it or 7
-- days pass; then the row is deleted (D-160). Its only reader is that
-- person, an owner or developer with their password confirmed. secret_key
-- names the service's secret ('' for the temporary login); sealed is
-- crypto.Keys under AD secret_reveals.sealed + id (reseal). One a person
-- and service: a reset replaces it. The UNIQUE serves the lookup; the
-- clean-up scans the table (a handful of rows, each gone within 7 days).
CREATE TABLE IF NOT EXISTS secret_reveals (
    id          UUID        PRIMARY KEY,
    service_id  UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    secret_key  TEXT        NOT NULL CHECK (length(secret_key) <= 255),
    sealed      TEXT        NOT NULL,
    reason      TEXT        NOT NULL CHECK (reason IN ('created', 'reset')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT secret_reveals_service_user UNIQUE (service_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_secret_reveals_user ON secret_reveals (user_id);
