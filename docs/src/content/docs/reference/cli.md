---
title: toft CLI
description: Deploy, follow logs and roll back from CI or a terminal, and run upgrades on the panel host.
sidebar:
  order: 1
---

`toft` is Toft's command-line client. Its API commands deploy, read logs and
roll back, for CI first. Its host commands run on the panel's own server, as
root, to upgrade Toft and rotate its encryption key.

## Install

On the panel host, the installer puts `toft` at `/usr/local/bin/toft`, and
each upgrade replaces it.

Anywhere else, build it from a checkout of the repository. It needs Go and
`make`:

```sh
git clone https://github.com/Nickpap21/toft.git
cd toft
make toft
```

The binary is `./bin/toft`. Copy it onto your `PATH` or into your CI image.

## Usage

```sh
toft [-url URL] [-key KEY] [-org ID] [-json] <command> [flags] <service>
```

Global flags go before the command. A command's own flags may come before or
after the service.

| Flag | Environment variable | Meaning |
| --- | --- | --- |
| `-url` | `TOFT_URL` | The panel's address, such as `https://toft.example.com`. Required. |
| `-key` | `TOFT_API_KEY` | An API key (see [API keys](#api-keys)). Required. |
| `-org` | `TOFT_ORG` | The workspace ID, sent as the `X-Toft-Org` header. Without it, a full-account key acts in the workspace its owner last opened in the panel, so set it in CI. |
| `-json` | | Print the API's objects as JSON instead of a line of text (`deploy` and `rollback`). |

`<service>` is one of:

- the service's ID, the last part of its address in the panel
  (`/projects/<project-id>/services/<service-id>`);
- `project/environment/name`, such as `storefront/production/api`;
- `project/name` for a service shared by the whole project.

The name forms look the service up by name, which needs a full-account key.
With a one-project key, use the service's ID.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Done. With `--wait`, the deploy succeeded. |
| 1 | The API refused, the connection failed, or with `--wait` the deploy ended in failure. |
| 2 | A usage error: a missing flag, service or URL. |

An error prints the API's message and code. A deploy refused by the
[deploy policy](/toft/guide/deploys/#deploy-policy) lists each violation on
its own line.

## API commands

### deploy

Starts a deploy of a service.

```sh
toft deploy <service> [--image TAG | --ref REF] [--wait]
```

| Flag | Meaning |
| --- | --- |
| `--image TAG` | Deploy this image tag. |
| `--ref REF` | Deploy this git branch or tag. |
| `--wait` | Follow the deploy's log and exit with its result. |

Without `--wait`, it prints the deployment's ID and returns once the deploy
has started.

### logs

Prints the running container's log.

```sh
toft logs <service> [--follow] [--tail N]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--tail N` | 100 | Lines of history to print first. |
| `--follow` | off | Keep printing new lines until you stop it. |

Without `--follow`, it stops once no new line has arrived for a second and a
half. Viewers of one container's live log, in the CLI or the panel, share one
stream.

`logs` needs a full-account key: a one-project key can't read live logs.

### rollback

Deploys an earlier deployment of a service again.

```sh
toft rollback <service> [--to DEPLOYMENT_ID] [--wait]
```

| Flag | Meaning |
| --- | --- |
| `--to DEPLOYMENT_ID` | The deployment to deploy again. Without it, the last successful deployment before the newest, among the last 50. |
| `--wait` | Follow the deploy's log and exit with its result. |

### In CI

Keep the key in your CI's secret store and pass it through the environment:

```sh
export TOFT_URL=https://toft.example.com
export TOFT_API_KEY="<api-key>"
export TOFT_ORG="<workspace-id>"
toft deploy "<service-id>" --image "$IMAGE_TAG" --wait
```

## Host commands

These run on the panel host, as root, and don't use the API, so they need no
URL or key.

| Command | What it does |
| --- | --- |
| `toft upgrade --to vX.Y.Z` | Moves the panel to a release. `--force` allows the same or an older release. |
| `toft release-check` | Looks for a newer release. |
| `toft rotate-encryption-key` | Replaces the panel's `ENCRYPTION_KEY`. Running it again after a failure resumes. |
| `toft answer-on-ip` | Makes a panel restored from a backup answer on its server's IP address again, after **Stop answering on the IP…** left nobody able to open it. Restarts the backend. On a panel that was never restored it changes nothing. |

`upgrade` and `release-check` read the images from the registry in the
panel's `secrets.env`; `--repo`, or `TOFT_IMAGE_REPO`, names another. The
whole procedure, with rollback, is in [Upgrade](/toft/upgrade/).

## API keys

An API key lets a script or CI job call the API as you. Each key is shown
once, when you create it.

1. Open **Settings → API keys** and select **New API key**.
2. Give it a **Name** after where it will live, such as the CI job. Up to 64
   characters.
3. Choose its **Access**:
   - **One project**: deploys, variables or code in one project, nothing
     else. Best for CI. Only owners and developers can make one.
   - **Full account**: everything you can do, in the workspace you have open
     in the panel.
4. For a one-project key, choose the **Project**, then the services:
   **Every service in the project**, or **Only these**.
5. Under **Allows**, tick what the key may do.
6. Choose when it **Expires**.
7. Select **Create key** and copy the key. It starts with `paas_`.

A key never does more than your role allows, and stops working if you leave
the workspace or your account is deactivated. Revoking one takes effect at
once. Only you see the keys you created.

### Scopes

A one-project key needs one or more of these:

| Scope | Label in the panel | Allows |
| --- | --- | --- |
| `deploy` | Deploy, follow and roll back | Deploys, rollbacks and their logs. |
| `variables` | Variables and secrets | Reading and setting variables, and setting secrets. |
| `code_read` | Clone and fetch its code | Cloning and fetching the project's repositories. |
| `code_write` | Push to its code | Pushing too; includes `code_read`. |
| `checks` | Report checks from CI | Reading and reporting a commit's checks. |
| `issues_read` | Read its issues | Reading issues. |
| `issues_write` | Open and change its issues | Opening, changing and commenting; includes `issues_read`. |
| `docs_read` | Read its docs | Reading docs pages. |
| `docs_write` | Write its docs | Writing pages too; includes `docs_read`. Neither runs a page's commands. |

A key limited to **Only these** services may only have `deploy` and
`variables`, since code, checks, issues and docs belong to the whole project.
It reaches only those services: never an environment's shared variables, a
preview or the project's own routes. For `toft deploy` and `toft rollback`
from CI, a key limited to the one service with `deploy` is enough.

### Expiry

| Choice | Expires |
| --- | --- |
| In 30 days | 30 days from now |
| In 90 days | 90 days from now (the default) |
| In 1 year | 365 days from now |
| On a date… | At the end of the day you pick |
| Never | Never |

An expired key gets `401 api_key_expired`, on the API and on git over HTTPS.
Through the API, a key created without `expires_at` lasts 90 days, and
`"expires_at": null` makes one that never expires.

Keys can't do what needs a password, such as granting a deploy policy
exception, and can't approve a preview. The full API is in the [API reference](/toft/reference/api/).
