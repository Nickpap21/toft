# AGENTS.md — Toft

Rules for anyone changing this repository, people and coding agents alike.
`CONTRIBUTING.md` covers running Toft locally and how pull requests get in.

Toft is a self-hosted platform: deploys, git hosting, issues, docs and agent
access, on servers you own. Go REST API, Next.js panel, SSH-based server
provisioning, a Docker/Traefik deploy pipeline.

## Stack

| Layer    | Tech                                                                         |
| -------- | ---------------------------------------------------------------------------- |
| Backend  | Go 1.26, Gin, pgx/v5, zap, golang-jwt/v5, Docker SDK                         |
| Frontend | Next.js 16 (App Router), React 19, Tailwind 4, shadcn/ui, TypeScript, vitest |
| Infra    | Docker Compose, Traefik v3.6, PostgreSQL 16                                  |
| DB layer | sqlc (generated, pinned v1.30.0), Atlas (declarative migrations)             |

Local: frontend `:3000` (`npm run dev`), backend `:8080`, Traefik dashboard
`127.0.0.1:8081`. Sign-up is invite-only; the first account on an empty
instance becomes the instance operator.

## Commands

The `Makefile` is the authoritative list (`make help`).

| Task               | Command                                                                                                |
| ------------------ | ------------------------------------------------------------------------------------------------------ |
| Up                 | `make up` (creates the external `paas-network` first; bare `docker compose up -d` fails without it)    |
| Backend checks     | `cd backend && go vet ./... && go test -race ./...` (`make test` skips vet and `-race`)                |
| Linux tests        | `make test-linux` (some tests read `/proc`: a Mac passes what Linux fails)                             |
| Frontend checks    | `cd frontend && npm run lint && npm run lint:tokens && npm run typecheck && npm test && npm run build` |
| Regenerate queries | `make sqlc-gen` (needs gcc)                                                                            |
| Migrations         | `make migrate` (Atlas, declarative from `backend/db/migrations/schema.sql`)                            |

## Definition of done

- A backend change passes the backend checks; a frontend change the
  frontend checks; both, both.
- A schema or query change has run `make sqlc-gen`, and the generated code
  is in the same commit.
- Never skip, disable or weaken a test or a lint rule to get a green run.
  If one fails and you think it's wrong, say so.
- Every commit is signed off: `git commit -s` (DCO, `CONTRIBUTING.md`).

## Decisions already made

Don't re-litigate these or "improve" them into something else.

- **sqlc + Atlas over an ORM**: explicit SQL, generated types, plan control.
- **An opaque JWT in localStorage, not an httpOnly cookie**: the event
  streams need the token in a header, and `EventSource` can't send one.
- **Deploys are fire-and-forget goroutines returning 202**: the pipeline can
  run for minutes; the client follows progress over the event stream.
- **Secrets are write-once**: no read-back endpoint, by design.
- **Per-server Traefik rather than a central proxy**: each server keeps
  working if the control plane is down.

## Naming: Toft vs paas

The project was called "paas" before it was Toft.

- Existing identifiers, package names, storage keys, the `paas_` token and
  API-key prefixes and `paas-network` stay as they are. Never rename them as
  a side effect of other work.
- New env vars, systemd units and user-facing strings use `toft` (as in
  `TOFT_APP_NETWORKS`, `toft-firewall.service`).
- New Go packages and database objects are plain domain nouns with neither
  prefix (`sharedvolume`, `deploypolicy`; tables `repository_mirrors`,
  `issue_links`).

## The API

- The contract is `backend/api/openapi.yaml`. A new or changed endpoint goes
  into the spec first; the route table test (`cmd/server/routes_test.go`)
  and the response checks (`internal/apispec`) fail otherwise. Then
  `make api-types` (the panel's `schema.d.ts`) and `make api-client` (the
  CLI's models).
- Errors carry a stable code and RFC 9457 fields beside `error`/`data.code`.
- Lists that can grow page with `?limit`, `?cursor` and `next`.

## Backend (`backend/`)

### Layers

1. Handler → service → repository. No business logic in handlers, no
   database calls outside repositories. Every repository sits behind an
   interface.
2. Model structs never reach the API: requests and responses have their own
   types.
3. Responses go through the `respond` package (`respond.JSON`,
   `respond.Error`, `respond.NoContent`), never `c.JSON`.
4. Handlers parse the request, call a service and write the response.
   Nothing else.

### Authorization

- Resources belong to a workspace (`organizations`), not a user. Check the
  resource is in the caller's active workspace before any access, and the
  member role (owner, developer, viewer; `middleware.MemberRoleFrom`) before
  any change: viewers never write. Instance operators
  (`instance_operators`) are separate from workspace roles.
- Every non-auth route sits in the `protected` group, which uses
  `middleware.AuthWithOrg` (a JWT, a `paas_` API key or an agent's
  `toft_oat_` token, plus the active workspace) and `apikey.EnforceScopes`
  (project-scoped keys, and agents: deny by default, `AgentScopeFor`). A
  route that acts for an agent runs the agent policy in its use case
  (`deployment.Service.CheckAgent`) before anything else.
- `cmd/server/routes_roles_test.go` records each route's minimum role,
  session and password requirement. A change to who may call a route
  changes its entry on purpose.
- Route parameters use the names already in the router (`:projectID`,
  `:serviceID`, `:serverID`, `:deploymentID`, `:environmentID`,
  `:repositoryID`, `:issueNumber` and the rest in `cmd/server`). Mixed names
  on the same path conflict in Gin's router.

### Database

- `db/migrations/schema.sql` is the source of truth; Atlas applies it
  declaratively. To change the schema, edit it, add the next numbered change
  file in `db/migrations/`, and run `make sqlc-gen` if queries changed.
- New queries go through sqlc. Never add raw pgx (some older repositories
  still have it; don't extend it), and never hand-edit `db/sqlcdb/`.
  `make sqlc-gen` needs gcc; never hand-write the generated code.
- **The schema only moves forward.** A rollback runs the previous release
  on the newer schema, so every change works with the previous release's
  code:
  - add, never rename or retype in place;
  - a new NOT NULL column has a default;
  - a CHECK only widens;
  - a drop goes in `db/migrations/contract.sql` (explicit, idempotent,
    `IF EXISTS`), one release after the code stopped using the object.
- `db/migrations/atlas.hcl` makes Atlas skip dropping schemas, tables,
  columns and foreign keys. The Atlas image is pinned by digest in the
  `Makefile` and in `cmd/toft/upgrade.go`; change both together.

### Tests

- Postgres-backed tests (`internal/testdb.New`) skip unless
  `TOFT_TEST_DATABASE_URL` is set. `make test-db` starts their server and
  prints the value. Set but unreachable is a failure, not a skip.
- Git tests need git 2.54+, `git-http-backend` and `ssh`; without them they
  skip locally and fail in CI. Run them in `golang:1.26.11-alpine` with
  `apk add git git-daemon openssh-client gcc musl-dev`.

### Domain model

```
users ── org_members (owner | developer | viewer) ── organizations ("workspaces")
instance_operators (users who run the instance)

organizations
  ├── projects
  │     ├── repositories (git; bare repos only in the paas-git container,
  │     │     │           via internal/gitstore)
  │     │     ├── pull_requests, git_events (the outbox), commit_checks
  │     │     └── repository_mirrors, repository_webhooks
  │     ├── issues (numbered per project) ── comments, events, issue_links
  │     ├── doc_pages ── versions, the services and refs they name
  │     └── environments ── environment_env_vars, services
  └── servers

services
  ├── server_id (NULL = the control-plane server)
  ├── repository_id (builds from the project's own repository)
  ├── domains, env_vars (is_secret rows encrypted and write-once)
  └── deployments
```

`schema.sql` is authoritative; this sketch is for orientation.

### Remote servers

```
deployment.OrchestratorFactory (interface, in deployment)
    └── docker.Factory implements it
            ├── ForServer(nil) → LocalOrchestrator   (local Docker socket)
            └── ForServer(srv) → RemoteOrchestrator  (SSH-tunnelled Docker socket,
                                                      over TCP or the server's agent)
```

- A server with `connection = 'agent'` is reached through the WebSocket its
  `toft-agent` opened (`internal/agenthub`), with the same SSH inside it.
  The agent's key is pinned like a host key, and an agent is never trusted
  on first use. New agent abilities are typed requests (`pkg/agentproto`),
  never a shell command the panel builds.
- SSH host keys are pinned (trust on first use). Every SSH connection goes
  through the pinned-key check. Never add a dial path that skips it, and
  never use `ssh.InsecureIgnoreHostKey`, in tests either.
- Every remote call takes a `ctx`; deferred clean-ups use
  `context.WithoutCancel`.
- No import cycles: `deployment` → `server`, `docker` → `deployment`.
- The provisioner (`server/provisioner.go`) stays idempotent, and every new
  provisioner command gets a fake-shell test.
- The host firewall (`server/firewall.nft`, `server/toft-firewall.service`)
  is installed by the provisioner and `toft upgrade`; change it there,
  never on a server.
- All compose parsing goes through `internal/composespec`. Never write a
  second parser.
- Never trust a container's labels alone: tenants choose their images, and
  images carry labels. Use `rollout.IsSlotName` or the platform-derived
  compose project.
- The deploy policy (`internal/deploypolicy`) runs on every deploy; reserved
  `paas.*`, `traefik.*` and `com.docker.compose.*` labels are rejected.

## Frontend (`frontend/`)

- Read `DESIGN.md` (repo root) before UI work: its tokens and rules come before generic
  defaults. `npm run lint:tokens` fails on a raw colour outside the token
  files.
- All code lives under `src/`. A root-level `app/` or `components/` in
  `frontend/` is picked up by Next.js over `src/` and breaks the build.
- The Docker build uses `output: "standalone"`; the Dockerfile's
  `HOSTNAME="0.0.0.0"` and `PORT=3000` make the container reachable.
- API calls go through `fetchAPI` (`lib/api/client.ts`), which adds the
  token (`localStorage` `paas_token`), unwraps `{ data }` and redirects on
  401. Never `fetch` the API directly.
- Event streams use `streamEvents` (`fetch` + `ReadableStream`), never
  `EventSource`, so the token goes in a header.
- Data fetching is React Query (`useQuery`, `useMutation`). Never use a
  derived `data ?? []` as a `useEffect` dependency: it's a new array every
  render and loops into React error #185. Depend on `data` and derive inside
  the effect.
- Toasts are `sonner`. Dialogs are Radix `Dialog`, always with a
  `DialogDescription`.
- Operator-only UI uses `useIsOperator()` (`src/context/AuthContext.tsx`).
- `npm run shot -- <route>` screenshots a page against a local development
  account (`scripts/shot.mjs`); never point it at a server you don't own.
  `npm run a11y` (after `npm run build`) fails on any axe violation in the
  main themes.
