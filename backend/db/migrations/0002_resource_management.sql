-- Resource management: server capacity columns
ALTER TABLE servers
    ADD COLUMN IF NOT EXISTS total_cpu_cores    FLOAT,
    ADD COLUMN IF NOT EXISTS total_memory_mb    BIGINT,
    ADD COLUMN IF NOT EXISTS cpu_cores_override FLOAT,
    ADD COLUMN IF NOT EXISTS memory_mb_override BIGINT;

-- Org-level resource bypass role setting
ALTER TABLE organizations
    ADD COLUMN IF NOT EXISTS resource_bypass_min_role TEXT NOT NULL DEFAULT 'owner';
