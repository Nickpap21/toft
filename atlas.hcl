# =============================================================================
#  Atlas configuration — PaaS Control Panel
#  Docs: https://atlasgo.io/atlas-schema/projects
#
#  This project uses DECLARATIVE migrations (schema apply), not versioned.
#  Atlas diffs the live DB against schema.sql and generates delta SQL on the fly.
#  No hash/checksum files are needed.
#
#  Usage (if atlas CLI is installed locally):
#    atlas schema apply --env local           # apply to local DB
#    atlas schema apply --env local --dry-run # preview SQL without applying
#    atlas schema inspect --env local         # inspect current DB schema
#
#  Otherwise, use: make migrate / make migrate-plan / make migrate-inspect
#  (those run Atlas via Docker — no local install required)
# =============================================================================

variable "db_url" {
  type    = string
  default = getenv("DATABASE_URL")
}

# For developers who have the atlas CLI installed locally.
# Requires Docker running (Atlas spins up an ephemeral postgres:16 for diffing).
env "local" {
  src = "file://backend/db/migrations/schema.sql"
  url = var.db_url
  dev = "docker://postgres/16/dev?search_path=public"
}
