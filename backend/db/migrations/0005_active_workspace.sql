-- Reversible: ALTER TABLE users DROP CONSTRAINT fk_users_active_org; ALTER TABLE users DROP COLUMN active_org_id;
-- Tracks which org a user is currently working in, so a user who belongs to
-- multiple orgs (workspaces) can switch between them without reissuing a JWT.
-- NULL means "not yet resolved" — org.Service.GetForUser falls back to the
-- earliest membership and self-heals this column the first time it's read.
ALTER TABLE users ADD COLUMN active_org_id UUID;
ALTER TABLE users ADD CONSTRAINT fk_users_active_org
    FOREIGN KEY (active_org_id) REFERENCES organizations(id) ON DELETE SET NULL;
