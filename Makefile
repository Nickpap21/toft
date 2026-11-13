# =============================================================================
#  PaaS Control Panel — Makefile
#  Usage: make <target>    Run `make help` to see all available commands.
# =============================================================================

.PHONY: help up down restart logs \
        up-db up-backend up-frontend \
        build rebuild \
        .atlas-dev-db migrate migrate-plan migrate-inspect db-shell \
        db-env-migrate db-backfill db-backfill-envs \
        backend-dev frontend-dev \
        network-rollback \
        test test-linux test-db test-db-down lint audit clean reset

# ─── Defaults ─────────────────────────────────────────────────────────────────
# docker-compose.git.yml: git hosting (D-71); toft upgrade leaves it out under TOFT_GIT=off.
COMPOSE  = docker compose -f docker-compose.yml -f docker-compose.git.yml

# The backend's data dir (TOFT_DATA_DIR, docker-compose.yml) is bind-mounted at
# the same path as on the host. Docker Desktop on macOS shares only /Users,
# /Volumes, /private, /tmp and /var/folders, so there it stays in the repo.
ifeq ($(shell uname -s),Darwin)
TOFT_DATA_DIR ?= $(CURDIR)/.toft-data
endif
export TOFT_DATA_DIR
BACKEND  = cd backend &&
FRONTEND = cd frontend &&

# ─── Atlas (declarative migrations via Docker — no local install needed) ──────
POSTGRES_USER     ?= paas_user
POSTGRES_PASSWORD ?= paas_password
POSTGRES_DB       ?= paas_db
# The community edition, pinned by digest (D-63); toft upgrade uses the same.
ATLAS_IMG         = arigaio/atlas:1.3.3-community@sha256:9c9958f5b8d26d404ab2e28098c8629526eb4bec647f790278611e58a0b19839

# Target DB URL (uses the compose service name "postgres" so the Atlas
# container can reach it over paas-internal, Postgres's own network: it is
# not on paas-network, D-31).
ATLAS_DB_URL  = postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/$(POSTGRES_DB)?sslmode=disable

# Ephemeral dev DB — Atlas creates/drops this automatically on the same
# postgres instance for schema diffing. The postgres superuser has CREATEDB.
ATLAS_DEV_URL = postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/atlas_dev?sslmode=disable

# Common docker run prefix: join paas-internal and mount the schema file.
ATLAS_RUN = docker run --rm \
	--network paas-internal \
	-v $(PWD)/backend/db/migrations:/migrations:ro \
	$(ATLAS_IMG)

# ─── Help ─────────────────────────────────────────────────────────────────────
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' \
		| sort

# Load .env file
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# paas-network's subnet(s), comma-separated: Traefik's metrics and ping
# answer only loopback and the control plane's network (docker-compose.yml).
CONTROL_PLANE_SUBNET = $$(docker network inspect paas-network -f '{{range .IPAM.Config}}{{.Subnet}},{{end}}' 2>/dev/null | sed 's/,$$//')

# ─── Core Stack ───────────────────────────────────────────────────────────────
up: network ## Start the full stack (Traefik + Postgres + Backend + Frontend)
	TOFT_CONTROL_PLANE_SUBNET="$(CONTROL_PLANE_SUBNET)" $(COMPOSE) up -d --build
	@echo "\n=================================================================="
	@echo "🚀 PaaS Control Panel is up and running!"
	@echo "=================================================================="
	@echo "🌐 Frontend:   http://localhost:3000"
	@echo "🔌 API:        http://localhost:8080"
	@echo "🚦 Traefik:    http://localhost:8081 (Dashboard)"
	@echo "\n🎉 You can now access everything locally without changing /etc/hosts!"
	@echo "   No seed account: the first account created on an empty instance becomes its operator."
	@echo "=================================================================="

down: ## Stop and remove all containers
	$(COMPOSE) down

restart: down up ## Full restart of the entire stack

logs: ## Follow logs for all services (Ctrl+C to stop)
	$(COMPOSE) logs -f

logs-backend: ## Follow backend logs only
	$(COMPOSE) logs -f backend

logs-frontend: ## Follow frontend logs only
	$(COMPOSE) logs -f frontend

logs-db: ## Follow Postgres logs only
	$(COMPOSE) logs -f postgres

# ─── Selective Start ──────────────────────────────────────────────────────────
up-db: network ## Start only Postgres (useful for local dev)
	$(COMPOSE) up -d postgres

up-backend: network ## Start Postgres + Backend
	$(COMPOSE) up -d postgres backend

up-frontend: ## Start the frontend container only
	$(COMPOSE) up -d frontend

# ─── Build ────────────────────────────────────────────────────────────────────
build: ## Build all Docker images without cache
	$(COMPOSE) build

rebuild: ## Force rebuild all images (no cache) and restart
	$(COMPOSE) build --no-cache
	TOFT_CONTROL_PLANE_SUBNET="$(CONTROL_PLANE_SUBNET)" $(COMPOSE) up -d

# ─── Local Development ────────────────────────────────────────────────────────
# The recommended dev workflow — no image rebuilds needed on code changes:
#
#   Terminal 1:  make dev-db        (Postgres only, stays running)
#   Terminal 2:  make dev-be        (Go binary, hot-reloads on save)
#   Terminal 3:  make dev-fe        (Next.js, hot-reloads on save)

dev-db: network ## Start only Postgres for local dev
	$(COMPOSE) up -d postgres
	@echo "Postgres is up in paas-postgres, on the compose network only (not published on the host)"

dev-be: ## Run Go backend with hot-reload via Air (auto-recompiles on save)
	$(BACKEND) $(HOME)/go/bin/air

dev-fe: ## Run Next.js frontend with hot-reload
	$(FRONTEND) npm run dev

# Legacy aliases kept for compatibility
backend-dev: dev-be
frontend-dev: dev-fe

# ─── Database ─────────────────────────────────────────────────────────────────

# Atlas needs a throw-away "dev" database to diff schemas internally.
# paas_user is not a superuser, so we create atlas_dev as the postgres
# superuser using peer auth inside the container (no password needed).
.atlas-dev-db:
	@docker exec paas-postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -tc \
		"SELECT 1 FROM pg_database WHERE datname = 'atlas_dev'" \
		| grep -q 1 \
		|| docker exec paas-postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c \
			"CREATE DATABASE atlas_dev"

migrate: .atlas-dev-db ## Apply schema changes to the DB via Atlas in Docker (auto-approved)
	@# Data steps Atlas can't express come first (premigrate.sql), on the
	@# same database Atlas targets, over the container's local socket.
	docker exec -i paas-postgres psql -v ON_ERROR_STOP=1 -q \
		"$(subst @postgres:5432,@localhost:5432,$(ATLAS_DB_URL))" < backend/db/migrations/premigrate.sql
	$(ATLAS_RUN) schema apply \
		--url "$(ATLAS_DB_URL)" \
		--to "file:///migrations/schema.sql" \
		--dev-url "$(ATLAS_DEV_URL)" \
		--config file:///migrations/atlas.hcl --env toft \
		--auto-approve
	@echo "✅ Schema applied successfully."

migrate-plan: .atlas-dev-db ## Preview the SQL Atlas would run without applying (dry-run)
	$(ATLAS_RUN) schema apply \
		--url "$(ATLAS_DB_URL)" \
		--to "file:///migrations/schema.sql" \
		--dev-url "$(ATLAS_DEV_URL)" \
		--config file:///migrations/atlas.hcl --env toft \
		--dry-run

migrate-inspect: ## Print the current live DB schema (HCL format)
	$(ATLAS_RUN) schema inspect \
		--url "$(ATLAS_DB_URL)"

db-shell: ## Open an interactive Postgres shell (psql)
	$(COMPOSE) exec postgres psql -U $${POSTGRES_USER:-paas_user} -d $${POSTGRES_DB:-paas_db}

db-env-migrate: ## Backfill environments table and populate environment_id on existing services
	@echo "Step 1: Creating 'production' environment for every project that has services..."
	@docker exec paas-postgres psql -U $${POSTGRES_USER:-paas_user} -d $${POSTGRES_DB:-paas_db} -c \
		"INSERT INTO environments (project_id, name) \
		 SELECT DISTINCT s.project_id, 'production' \
		 FROM services s \
		 ON CONFLICT (project_id, name) DO NOTHING;"
	@echo "Step 2: Linking existing services to their 'production' environment..."
	@docker exec paas-postgres psql -U $${POSTGRES_USER:-paas_user} -d $${POSTGRES_DB:-paas_db} -c \
		"UPDATE services SET environment_id = ( \
		    SELECT e.id FROM environments e WHERE e.project_id = services.project_id LIMIT 1 \
		 ) WHERE environment_id IS NULL;"
	@echo "✅ Environment migration complete."

db-backfill-envs: ## Backfill: create "production" env for projects with none; assign orphaned services
	@echo "Step 1: Creating 'production' environment for every project that has none..."
	@docker exec paas-postgres psql -U $${POSTGRES_USER:-paas_user} -d $${POSTGRES_DB:-paas_db} -c \
		"INSERT INTO environments (project_id, name) \
		 SELECT p.id, 'production' FROM projects p \
		 WHERE NOT EXISTS (SELECT 1 FROM environments e WHERE e.project_id = p.id) \
		 ON CONFLICT (project_id, name) DO NOTHING;"
	@echo "Step 2: Assigning services with NULL environment_id to their project's production env..."
	@docker exec paas-postgres psql -U $${POSTGRES_USER:-paas_user} -d $${POSTGRES_DB:-paas_db} -c \
		"UPDATE services s \
		 SET environment_id = ( \
		     SELECT e.id FROM environments e \
		     JOIN projects p ON e.project_id = p.id \
		     WHERE e.name = 'production' \
		     LIMIT 1 \
		 ) \
		 WHERE environment_id IS NULL;"
	@echo "✅ Environment backfill complete."

db-backfill: ## Backfill org_id on projects/servers that pre-date multi-tenancy
	@docker exec paas-postgres psql -U $${POSTGRES_USER:-paas_user} -d $${POSTGRES_DB:-paas_db} -c \
		"UPDATE projects p SET org_id = om.org_id FROM org_members om WHERE p.org_id IS NULL AND p.user_id = om.user_id;"
	@docker exec paas-postgres psql -U $${POSTGRES_USER:-paas_user} -d $${POSTGRES_DB:-paas_db} -c \
		"UPDATE servers s SET org_id = om.org_id FROM org_members om WHERE s.org_id IS NULL AND s.user_id = om.user_id;"
	@echo "✅ Backfill complete."

db-dump: ## Dump the database to ./backup.sql
	$(COMPOSE) exec postgres pg_dump -U $${POSTGRES_USER:-paas_user} $${POSTGRES_DB:-paas_db} > backup.sql
	@echo "✅ Dump saved to backup.sql"

# ─── Code Quality ─────────────────────────────────────────────────────────────
api-types: ## Regenerate the web client's types from backend/api/openapi.yaml (D-18)
	cd frontend && npm run api:types

api-client: ## Regenerate the Go models the toft CLI uses from backend/api/openapi.yaml (D-18)
	cd backend/pkg/apiclient && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 -config oapi-codegen.yaml ../../api/openapi.yaml

toft: ## Build the toft CLI into ./bin/toft
	cd backend && go build -o ../bin/toft ./cmd/toft

test: ## Run all Go tests
	$(BACKEND) go test ./... -v

test-linux: test-db ## The backend tests as CI runs them, on Linux in Docker (some read /proc and the shell); run before a tag
	docker run --rm -v "$(CURDIR)":/src -w /src/backend -v toft-gomod:/go/pkg/mod -v toft-gocache:/root/.cache/go-build \
		--network container:$(TEST_DB) -e TOFT_TEST_DATABASE_URL='$(TEST_DB_URL_IN)' \
		golang:$$(sed -n 's/^go //p' backend/go.mod) go test -race ./...

# The Postgres-backed tests' server (internal/testdb): the image docker-compose.yml
# pins, on tmpfs, reachable on 127.0.0.1 only. Each test makes and drops its own
# database there. Run them with TOFT_TEST_DATABASE_URL='$(TEST_DB_URL)'.
TEST_DB        = toft-test-postgres
TEST_DB_URL    = postgres://postgres:toft@127.0.0.1:55432/postgres?sslmode=disable
TEST_DB_URL_IN = postgres://postgres:toft@127.0.0.1:5432/postgres?sslmode=disable

test-db: ## Start the Postgres the database tests use (TOFT_TEST_DATABASE_URL), unless it runs the pinned image already
	@img=$$(sed -n '/^  postgres:/,/image:/s/^ *image: *//p' docker-compose.yml); \
	test "$$(docker inspect -f '{{.State.Running}} {{.Config.Image}}' $(TEST_DB) 2>/dev/null)" = "true $$img" || { \
		docker rm -f $(TEST_DB) >/dev/null 2>&1; \
		docker run -d --name $(TEST_DB) -p 127.0.0.1:55432:5432 -e POSTGRES_PASSWORD=toft --tmpfs /var/lib/postgresql/data \
			"$$img" >/dev/null; }
	@for i in $$(seq 30); do docker exec $(TEST_DB) pg_isready -q -U postgres -h 127.0.0.1 && exit 0; sleep 1; done; \
		echo "$(TEST_DB) did not start"; docker logs $(TEST_DB) | tail -20; exit 1
	@echo "TOFT_TEST_DATABASE_URL='$(TEST_DB_URL)'"

test-db-down: ## Remove the database tests' Postgres
	docker rm -f $(TEST_DB)

lint: ## Run Go linter (requires golangci-lint)
	$(BACKEND) golangci-lint run ./...

audit: ## Known vulnerabilities in the dependencies the code reaches (Go) and ships (npm); CI runs the same
	./scripts/govulncheck.sh
	$(FRONTEND) npm audit --omit=dev --audit-level=high

# Pinned so codegen output does not drift with whatever sqlc happens to be
# installed. Needs gcc (cgo); the first run builds sqlc and takes ~90s.
SQLC_VERSION ?= v1.30.0

sqlc-gen: ## Regenerate sqlc Go files from SQL queries (pinned sqlc via go run)
	$(BACKEND) go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

tidy: ## Tidy Go module dependencies
	$(BACKEND) go mod tidy

# ─── Utilities ────────────────────────────────────────────────────────────────
network: ## Create the control plane's Docker network if it doesn't exist (apps never join it under isolated app networks)
	@docker network inspect paas-network >/dev/null 2>&1 || \
		(docker network create paas-network && echo "✅ Created paas-network")

network-rollback: ## Reconnect app containers to paas-network with their aliases (before going back from isolated app networks, or to an older backend)
	@TRAEFIK_NETWORK=$${TRAEFIK_NETWORK:-paas-network} sh scripts/network-rollback.sh

ps: ## Show running containers for this project
	$(COMPOSE) ps

status: ps ## Alias for ps

clean: ## Remove stopped containers and dangling images
	$(COMPOSE) down --remove-orphans
	docker image prune -f

reset: ## ⚠️  DESTRUCTIVE: remove all containers, volumes, and images for this project
	$(COMPOSE) down --volumes --remove-orphans
	docker image prune -f
	@echo "🗑️  All project data has been wiped."
