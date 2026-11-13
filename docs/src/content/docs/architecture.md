---
title: Architecture
description: The parts of Toft, how a request and a deploy move through them, where state lives, and how the code is layered.
---

This page is for contributors and for anyone weighing Toft up. It describes
the parts that run, how they talk to each other, where your data lives, and
the rules the backend code follows. The [decision records](/toft/decisions/)
explain why each choice was made.

## The parts

| Part | What it is | Runs as |
| --- | --- | --- |
| API | Go, with Gin for HTTP, pgx and sqlc for the database, zap for logs and the Docker SDK | the `paas-backend` container |
| Panel | Next.js (App Router), React, Tailwind and shadcn/ui | the `paas-frontend` container |
| Database | PostgreSQL 16 | the `paas-postgres` container |
| Git | The canonical git binary, driven by `toft-git` | the `paas-git` container: no network, read-only, every capability dropped |
| Traefik | Traefik v3.6, one per server, with its own Let's Encrypt certificates | `paas-traefik` on every server |
| Node agent | `toft-agent`, a single Go binary | `toft-agent.service` on servers that use it |
| CLI | `toft`, for operators and CI | your machine, and the panel host for `toft upgrade` |

The panel host runs Traefik, the API, the panel, Postgres and, when git
hosting is on, the git container. The servers you add run Docker, Traefik and
your apps. A single-server install runs your apps on the panel host too: see
[single-server installs](/toft/decisions/single-server-first-class/).

## How it fits together

```
          you: browser · toft CLI · git · AI agents (MCP)
                                  │ HTTPS :443, git over SSH :2222
 ┌────────────────────────────────▼─────────────────────────────────┐
 │ panel host                                                       │
 │   Traefik ──► Next.js panel                                      │
 │      │                                                           │
 │      └──────► Go API ──────────► Postgres                        │
 │               │   │                                              │
 │               │   └── socket ──► git (no network)                │
 │               └── Docker socket ──► apps on this host (optional) │
 └───────────┬───────────────────────────────────────▲──────────────┘
             │ SSH, the panel dials                  │ WebSocket on 443,
             │ (host key pinned)                     │ the agent dials out
 ┌───────────▼────────────┐              ┌───────────┴────────────┐
 │ server over SSH        │              │ server with the agent  │
 │   Docker · Traefik     │              │   toft-agent · Docker  │
 │   your apps            │              │   Traefik · your apps  │
 └────────────────────────┘              └────────────────────────┘
```

Each server runs its own Traefik with its own certificates, so it keeps
serving its apps when the panel is down. See
[Traefik on every server](/toft/decisions/traefik-per-server/).

## How a request flows

1. The browser, the CLI or an agent calls `https://<panel>/api/v1/…`. The
   panel host's Traefik sends `/api`, git's smart HTTP paths and `/mcp` to the
   API, and everything else to the Next.js panel.
2. The auth middleware accepts one of three credentials: a session token, a
   `paas_` API key, or an AI agent's `toft_oat_` OAuth token. The request
   names its workspace in the `X-Toft-Org` header.
3. A project-scoped API key or an agent token is checked against the scopes
   the route needs, and denied by default.
4. The handler parses the request and calls a use case in its service. The
   service checks that the resource belongs to the caller's workspace, and
   that the caller's role (owner, developer or viewer) may do this. Viewers
   never write.
5. The service reads and writes through a repository, which runs sqlc's
   generated queries against Postgres.
6. The handler writes the answer. Errors carry a stable `code` and RFC 9457
   problem fields. Lists that can grow page with `?limit`, `?cursor` and
   `next`.

## How a deploy flows

1. A deploy starts from the panel, the CLI, the API, a push to a repository or
   an agent's `deploy` tool.
2. Before anything runs, the API refuses a deploy that can't go ahead: its
   server can't be loaded, the deploy policy rejects what it mounts or
   reaches, it would exceed the server's capacity, or its isolation level
   can't run on that server (`409 sandbox_unavailable`). An agent's deploy
   also meets the [agent policy](/toft/decisions/agent-policy/).
3. The API records the deployment, starts the pipeline in a goroutine and
   answers `202 Accepted` at once. See
   [deploys return 202](/toft/decisions/deploys-return-202/).
4. The client follows the log over server-sent events at
   `GET /api/v1/services/{serviceID}/deployments/{deploymentID}/logs/stream`.
5. The pipeline builds on the target server (it clones the repository and runs
   `docker build` there), pulls an image, or brings up a Compose stack.
6. The migrate command, if any, runs in a one-off container. Then a new
   container starts beside the old one (blue/green), with the hardening and
   runtime of its [isolation level](/toft/reference/isolation/). Its health
   check passes, its post-deploy commands run, traffic moves to it, and the
   old container is retired. A failure at any step leaves the old one
   serving.
7. A database or cache stops before its new container starts instead. See
   [data services stop, then start](/toft/decisions/data-services-stop-then-start/).

The API reaches each server's Docker through an orchestrator: the local
Docker socket for the panel host, and an SSH-tunnelled Docker socket for every
other server. On a server with the agent, the rollout is handed to the agent,
which runs it to the end even if the panel restarts in the middle.

## SSH or the agent

| | SSH | Agent |
| --- | --- | --- |
| Who connects | The panel dials the server's SSH port | The agent dials `wss://<panel>/api/v1/agent/connect` |
| What the server must accept | Inbound SSH from the panel | Nothing inbound: outbound 443 only, `HTTPS_PROXY` honoured |
| Identity | The host key, pinned on first use | The agent's key, pinned when it joins; never trusted on first use |
| Inside the connection | SSH, with Docker tunnelled through it | The same SSH, run inside the WebSocket |
| Extra on the server | Nothing | Traefik's routes written as files, logs spooled while the panel is away, metrics buffered offline, the DNS relay for the strict network |

Every caller that runs a command on a server works the same over both: the
panel is the SSH client either way. New agent abilities are typed requests,
never a shell command the panel builds. See
[the node agent](/toft/decisions/node-agent/).

## Where state lives

| State | Where |
| --- | --- |
| Accounts, workspaces, projects, services, deployments, issues, docs, settings and the audit log | Postgres, in the `postgres-data` volume on the panel host |
| Secrets and other sealed values | Postgres, encrypted with AES-256-GCM under the instance's `ENCRYPTION_KEY` |
| Git repositories | The `paas-git` volume, reached only through the git container |
| Certificates | Each server's own Traefik |
| App data | Docker volumes and bind mounts on the server the app runs on |
| An agent's key and state | `/var/lib/toft-agent` on its server |
| Upgrade dumps and state | `/var/lib/toft/upgrade` on the panel host (the last 3 dumps) |

Secrets are write-once: the API stores them and never returns them. See
[write-once secrets](/toft/decisions/write-once-secrets/). Nightly backups
are encrypted and stored in S3; see [backups](/toft/guide/backups/).

## Tenancy and isolation

```
instance          (instance operators run it)
└── workspace     (members: owner, developer, viewer)
    ├── servers   (owned by one workspace, or shared with others as a capacity share)
    └── project   (repositories, issues, docs)
        └── environment   (production, staging, a pull request's preview)
            └── service   (a git build, an image or a Compose stack)
```

- Every resource belongs to a workspace, not to a user. Every request is
  checked against the workspace it names.
- Instance operators are separate from workspace roles. Only operators create
  workspaces, and sign-up is [invite-only](/toft/decisions/invite-only-sign-up/).
- A service runs at one of three [isolation levels](/toft/reference/isolation/):
  Standard, Sandboxed or Sandboxed with a strict network.
- App containers can reach their own host only on TCP 80, 443 and DNS. See
  [the host firewall](/toft/decisions/host-firewall/).
- Every deploy passes the deploy policy, which rejects reserved `paas.*`,
  `traefik.*` and `com.docker.compose.*` labels and limits what a service may
  mount or reach.

## How the backend is layered

Dependencies point one way: handler, then service, then repository.

- **Handlers** parse the request, call one service method and write the
  answer through the `respond` package. They hold no business rules.
- **Services** hold the use cases: authorization, orchestration and
  transactions. They take and return input and output structs, never the
  database's models.
- **Repositories** are the only code that touches the database. Each sits
  behind an interface.
- **The orchestrator** is an interface in the `deployment` package. The
  `docker` package implements it, so deployment logic never imports Docker
  code directly.

## The database: sqlc and Atlas

- `backend/db/migrations/schema.sql` is the source of truth. Atlas applies it
  declaratively, beside a numbered change file for each change.
- Queries are plain SQL, and sqlc generates typed Go from them
  (`make sqlc-gen`). The generated code is committed with the change.
- The schema only moves forward: every release's schema works with the
  previous release's code, so a rollback runs the previous images on the newer
  schema. See [forward-only schema](/toft/decisions/forward-only-schema/) and
  [sqlc and Atlas](/toft/decisions/sqlc-and-atlas/).

## The API contract

`backend/api/openapi.yaml` is the contract. A new or changed endpoint goes
into the spec first.

- The web client's types are generated from it (`make api-types`), and so are
  the `toft` CLI's models (`make api-client`).
- The MCP tools are declared in it (`x-toft-tools`), and a test checks that
  the tools and the code agree.
- A route table test and response checks fail when the code and the spec
  disagree.
- Every instance serves the spec of the version it runs at
  `/api/v1/openapi.yaml`, rendered at `/api/v1/docs`.

The same spec renders the [API reference](/toft/reference/api/) on this site.
See [one OpenAPI spec](/toft/decisions/one-openapi-spec/).
