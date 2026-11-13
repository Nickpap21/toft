---
title: Preview environments
description: A running copy of your app for every open pull request, removed when it merges or closes.
sidebar:
  order: 3
---

A preview is an environment for one pull request. It runs the pull request's
code next to an environment you choose, under its own address, so reviewers
can click through the change before it merges. Previews need a pull request
in one of the project's own repositories in Toft.

## Turn previews on

Previews are off in every project until an owner turns them on.

1. Open the project's **Settings** and find **Previews**.
2. Switch it **On**.
3. In **Copies**, choose the environment previews copy, usually `staging`.
4. Optionally, set a **Domain** for preview addresses, such as
   `preview.example.com` (see [Addresses](#addresses)).
5. Select **Save**.

Previews start at the next pull request opened or pushed to. An open pull
request without one gets it from **Start preview** on the pull request.

Changing the settings needs a signed-in session: an API key can't change
them. Turning previews off, or copying another environment, removes the
previews that are running.

## What a preview runs

A pull request's preview is an environment named `pr-42` (`<repository>-pr-42`
for a pull request in the project's second or later repository). In it:

- The copied environment's services that build from the pull request's
  repository are rebuilt from its head branch.
- Everything else, the rest of the copied environment and the project's and
  workspace's shared services, is reached by name, as it is from the copied
  environment.
- Each push to the pull request updates the preview.

A preview's services get two extra variables: `TOFT_PULL_REQUEST` (the pull
request's number) and `TOFT_PREVIEW_URL` (the service's preview address, when
it has a domain). A variable that names a rebuilt service's address in the
copied environment is rewritten to its preview address.

When the copied environment is protected, previews don't run its migrate
commands.

The pull request's checks include `toft/preview/<service>` for each rebuilt
service, and its conversation shows when the preview starts, waits, updates,
stops and is removed. A preview's failures raise no alerts.

## Who starts one

| Pull request opened or pushed to by | What happens |
| --- | --- |
| An owner or developer, signed in or with their own SSH key | The preview starts at once. |
| An API key, or an agent | Each new commit waits for an owner or developer to approve it. |

A pull request opened with an API key, or pushed to with one at any point,
stays untrusted for its whole life. So does a pull request an agent opens.

### Approving an untrusted preview

An untrusted pull request shows in Home's **Needs you** as **Preview to
approve**, and its preview card on the pull request explains what approving
runs. Read the Dockerfile's changes, then select **Approve** with the
commit's short hash.

- Approving needs a signed-in session. No API key can approve, an owner's
  included.
- Each new commit needs a new approval. Until then, the approved commit keeps
  serving.
- Builds run on the standard runtime (runc), as every build does, so the
  approval is the gate for what a `RUN` step does.

## Isolation

A trusted preview, started by an owner or developer, runs like the
environment it copies:

- It gets the original services' variables and secrets, and the environment's.
- It reaches the copied environment's services and writes to its database.
- It keeps the original's deploy policy exceptions.

An untrusted preview runs walled off:

- **Sandboxed** under gVisor (with the strict network where its server
  offers one), in a network of its own.
- Plain variables only: **no secrets**. Keep passwords in secrets, not in
  plain variables, because plain variables do reach untrusted previews.
- Fresh, empty volumes, and no deploy policy exceptions.
- No route to the copied environment's services or to private addresses
  (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`). Outbound
  TCP port 25 is blocked. The public internet is open.
- Its memory limit is the original's plus 64 MiB for gVisor.

Before an untrusted commit builds, Toft refuses a service that mounts a
shared volume or a server path, or that builds with Nixpacks (whose cache is
shared with every build on the server). Such a service needs a Dockerfile.

An untrusted preview starts only where the copied environment's servers can
run Sandboxed (an instance operator installs gVisor from the server's page)
and their host firewall has the previews' rules loaded (provision the server
again to load them). Until then the preview card says which server is
missing what. Previews also need isolated app networks, the default
(`TOFT_APP_NETWORKS`). See the [isolation reference](/toft/reference/isolation/).

## Addresses

Only services with a domain in the copied environment get a preview address.

| Setting | Address |
| --- | --- |
| A preview domain, such as `preview.example.com` | `pr-42-web.preview.example.com` |
| No domain | `web-pr42-<project>.<server-ip-with-dashes>.sslip.io` |

With a domain, add a wildcard DNS record, `*.preview.example.com`, pointing
at the server the copied environment runs on. The Previews settings show the
record and whether it resolves. Until it does, previews start under sslip.io
names, and **Check again** moves new ones over.

Each preview gets its own Let's Encrypt certificate, or one wildcard
certificate where the server has a DNS provider. Let's Encrypt issues 50 new
certificates a week per registered domain, production's included. Once a
domain has had 40 preview certificates in 7 days, new previews use sslip.io
names, so a busy project is best given a preview domain of its own.

## Removal

Merging or closing the pull request removes its preview, with its
containers, addresses and network. You can also **Stop** a preview by hand
and start it again later.

## Limits and room

- **5 previews per project** at once, stopped ones included. A sixth waits
  until one is removed. One waiting for approval doesn't count.
- When a deploy needs memory on a server, the server stops its oldest
  previews first. Services in other environments are never stopped for a
  preview.
- A preview never stops another preview. One that doesn't fit fails its
  check with the numbers; start it again once there is room.
- A preview stopped for room starts again on its next push, or on an
  approval.
