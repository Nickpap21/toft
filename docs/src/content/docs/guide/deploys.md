---
title: Deploys
description: How projects, environments and services fit together, and what happens when you deploy one.
sidebar:
  order: 2
---

A deploy builds or pulls a service's image, starts it on its server and moves
traffic to it. This page covers where services live, the three sources a
service deploys from, and what Toft checks before, during and after a deploy.

## Projects, environments and services

Everything sits in a hierarchy:

```
workspace → project → environment → service
```

- A **workspace** holds your team, your servers and the agent policy.
- A **project** is one product, such as Northwind's storefront. It holds the
  project's repositories, issues and docs as well as its services.
- An **environment** is one copy of the project: `production`, `staging`,
  `dev`. Each has its own network.
- A **service** is one deployable unit: a web app, a worker, a database.

A service can also be shared by the whole project, or by the whole workspace,
instead of belonging to one environment.

An environment named `production` or `prod` is created **protected**. Deploying,
stopping, restarting or deleting anything in a protected environment, or
replacing one of its secrets, asks you to confirm first. You can turn
protection on or off for any environment.

## Sources

A service deploys from one of three sources. You choose it when you create
the service.

| Source | What Toft does |
| --- | --- |
| Git | Clones the branch and builds an image on the server, then runs it. |
| Docker image | Pulls the image and runs it. A private registry's login goes in Settings → Credentials. |
| Compose | Runs `docker compose up` with a file kept in the panel or in a repository. |

### Git

A git service builds from one of the project's own repositories in Toft, or
from any repository URL. For a private repository at a URL, store a username
and token in Settings → Credentials.

Settings → Source offers two builders:

- **Dockerfile**: builds `Dockerfile` in the base directory (the repository
  root when the base directory is empty). The build context is always the
  repository root, so a Dockerfile in a monorepo package can copy files from
  the root. It holds the commit's files without `.git`, so a build can't run
  `git`, and redeploying the same commit builds from Docker's cache.
- **Nixpacks**: detects the language and builds it. You can replace the
  install, build and start commands it detects. Nixpacks must be installed on
  the server.

Only variables with a public framework prefix (`NEXT_PUBLIC_`, `VITE_`,
`REACT_APP_`, `NG_APP_`) are passed to the build as build arguments, since
their values end up in the browser bundle anyway. Everything else, secrets
included, reaches the container only when it starts, so it never sits in an
image layer. A build from a Toft repository also gets `TOFT_GIT_SHA` and
`TOFT_GIT_BRANCH`.

**Deploy on push** is on by default. A push to the service's branch deploys
it:

- From a Toft repository, nothing else is needed.
- From GitHub, add the project's webhook to each repository. Open the
  project's Settings, find **Deploy on push from GitHub**, and copy the URL and
  secret into the repository's webhook settings in GitHub. Set the content
  type to `application/json` and send only push events.

Turn Deploy on push off and the service deploys only when someone deploys it.

### Docker image

An image service runs the image you name, with an optional start command. A
deploy can name a different tag, for example from CI with
`toft deploy <service> --image 1.2.3` (see the [CLI reference](/toft/reference/cli/)).

### Compose

A Compose service runs a whole stack. The file is either written in the
panel or read from a repository at a path you choose. Domains go to one of
the stack's services: Settings → Domain routing picks which one, and on which
port. With a single service in the file, that service is chosen for you.

The service's variables reach the file as `${NAME}`. Toft adds `TOFT_URL`,
the service's address (`https://` and its first domain), so an app can be
told where it lives; a `TOFT_URL` of your own wins. The catalog's apps
(Uptime Kuma, Plausible, n8n, Ghost, Umami) are Compose services made this
way, and you can edit their file like any other.

No catalog entry starts with a default login. Its passwords are generated
at creation and saved as secrets, shown once. Umami's admin account gets
its generated password at the first start. Uptime Kuma, Plausible, n8n and
Ghost make whoever opens them first their admin, so they start behind a
temporary login (user `toft`): set up the app's admin account, then turn the
login off in Settings → Access.

Compose replaces containers in place, so a Compose deploy has no blue/green
switch.

## Health check and the switch

A single-container service deploys **blue/green** by default. The new
container starts next to the old one. Toft asks its health check, then runs
the post-deploy commands in it. Only then is the
old container retired. If any step fails, the new container is removed and the
old one keeps serving.

The health check is in Settings → Health check:

| Field | Default | Meaning |
| --- | --- | --- |
| Path | `/health` | A status of 400 or higher fails; a redirect passes. |
| Timeout per attempt | 5 seconds | 1 to 60. |
| Attempts | 0 | 0 to 10. 0 turns the check off. |

The check is requested from inside the container's network, so the image
needs no `curl`. The app must listen on more than `127.0.0.1`. While the
service is live, the same path is checked every minute: a failure marks the
service unhealthy and raises an alert, without taking it offline.

:::tip
The health check is off until you set Attempts. Turn it on for every web
service: without it, Toft switches as soon as the new container has started.
:::

### Databases and caches deploy stop-then-start

Two database processes must never share one data directory. A database or
cache (Postgres, MySQL, MariaDB, MongoDB, Redis, Valkey) therefore deploys
**stop then start**: the old container stops before the new one starts, and
starts again if the new one fails. Before it counts as ready, the new
container must accept connections on its port and keep running for 10
seconds. Expect a few seconds of downtime per deploy.

Settings → Resources → Deploy strategy sets this per service:

| Choice | Behaviour |
| --- | --- |
| Automatic | Stop then start for a database or cache, zero downtime for anything else. The default. |
| Zero downtime | Always blue/green. |
| Stop then start | Always stop the old container first. |

What happens when a deploy is interrupted, by a server restart or a lost
connection, is in [Operating Toft](/toft/guide/operating/).

## Memory limits

Every deploy needs a memory limit, on every server. A new service gets the
default for its kind:

| Kind | Default limit |
| --- | --- |
| App | 512 MiB |
| Database | 1 GiB |
| Cache | 256 MiB |
| Admin tool | Its own, about twice its idle use: Adminer and Mailpit 64 MiB, phpMyAdmin 128 MiB, RedisInsight and MailHog 256 MiB, pgAdmin 512 MiB |

A Compose service needs a limit on each service in the stack, in the file or
in the panel. A deploy without one is refused, with a suggested value. The
panel measures each service's memory and suggests raising or lowering its
limit; you apply the suggestion. CPU limits stay optional. An instance
operator can grant an exception through the deploy policy (below).

When a deploy doesn't fit in its server's memory, Toft stops the server's
oldest [preview environments](/toft/guide/previews/) first to make room.

## Variables and secrets

A service reads two kinds of setting as environment variables:

- **Variables** are plain values you can read and edit. You can mask a
  service's variable so it shows as dots on screen; it stays readable.
- **Secrets** are encrypted and **write-once**: once saved, nobody can read
  them back, through the panel or the API. You can replace a secret's value
  or delete it.

Both can be set on a service or on an environment. An environment's
variables and secrets are shared with every service in it; where both set a
key, the service's own value wins. A plain variable can't reuse a key an
environment secret already has.

Changes take effect at the next deploy. Settings → Lifecycle hooks →
Required variables lists keys that must be set before a deploy runs.

## Deploy logs

Every deploy streams its log live: the build, the pull, each step of the
switch, and why it failed if it did. The panel follows it as it runs, and so
does `toft deploy --wait`. The running container's own output is under the
service's logs, which `toft logs --follow` also streams.

## Rollback

A rollback deploys an earlier deployment again. For a git or image service it
reuses that deployment's built image, so nothing is rebuilt. For a Compose
service it uses the image tag and repository commit that deployment ran with.
The service's current variables and secrets apply.

Roll back from the service's deployments in the panel, or with
`toft rollback <service>`, which picks the last successful deployment before
the newest. Viewers can't roll back.

## One deploy of a service at a time

A service's deploys run one after the other. A deploy asked for while another
of the same service is still running is accepted and waits its turn, whether
it comes from the panel, the CLI, a push or a rollback: it shows as pending,
and its log says it is waiting. Every push still gets a deploy of its own, in
the order they were accepted. The wait doesn't count toward the deploy's time
limit.

While a service is moving to another server, no other deploy of it is
accepted. Deploy it again once the move has finished.

## Deploy time limit

Every deploy has a time limit, 60 minutes by default. Set a service's own in
Settings → Resources → Deploy time limit, from 5 to 240 minutes; leave it
empty for the default. Time spent waiting for a build slot doesn't count.

A deploy still running at its limit fails, and its summary names the step it
was at. A single-container service keeps serving its previous version. A
Compose stack is left as far as it got, and the service shows as errored
until the next deploy.

## Deploy policy

The deploy policy decides what a service may mount, reach or change on its
server. It checks each save and each deploy, and refuses, for example:

- privileged containers, added capabilities and devices;
- host networking, and host PID, IPC or UTS namespaces;
- the Docker socket, and bind mounts of host paths;
- another stack's external volumes, or networks outside the service's
  environment;
- commands run on the server's host (Settings → Lifecycle hooks → Before
  deploy, on the host);
- a missing memory limit.

Settings → Deploy policy shows what a service's configuration violates and
why. Some rules can never be granted: labels and container names Toft
reserves, files outside the Compose file's directory, and environment
variables that steer Docker itself. For the others, an instance operator can
grant the service an exception, with their password and a reason. The
exception is pinned to the values it approved: change one of them and the
deploy is refused until an operator grants it again.

How containers are isolated at runtime, including Sandboxed (gVisor), is in
the [isolation reference](/toft/reference/isolation/).
