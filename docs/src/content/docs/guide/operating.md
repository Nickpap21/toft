---
title: Operating
description: "Day-to-day notes for running Toft: SSH host keys, connections, metrics, blue/green switches, restarts mid-deploy and the panel's own health."
sidebar:
  order: 6
---

What the panel does behind the scenes, and what to do when something about a
server or a deploy looks wrong.

## SSH host keys

The panel checks the SSH host key of every server it reaches over SSH (trust
on first use).

- **Pinning.** The first successful connection to a server pins the host key
  it presents. That connection can be the check when you add the server, a
  test connection, a provision, a deploy, metrics collection or the 5-minute
  health poll. From then on, a connection that presents any other key is
  refused before any credential is sent.
- **Where to see it.** The server's page, **Connection** card, **Host key**
  row shows the key type, its `SHA256:` fingerprint and when it was pinned. A
  **Test connection** that pins a key also shows the fingerprint in a
  notification. The raw key is never sent to the browser.
- **When the key changes.** This happens after an OS reinstall, a rebuild on
  the same IP, or regenerated sshd keys. Every connection then fails with
  `SSH host key for <ip>:<port> changed: expected SHA256:A, server presented
  SHA256:B`. The server shows offline, and deploys fail with that message in
  their log. If you did not change the server, assume someone may be
  intercepting the connection and do not accept the new key.

A server with the [agent](/toft/guide/servers/#add-a-server-with-the-agent) is
never trusted on first use: its agent's key is pinned when it joins with a
token. **Join again…** makes a new token, which asks for your password because
it can replace the pinned key, and **Rotate key** replaces it while the agent
is connected.

### Comparing a fingerprint

Run this on the server itself: your provider's web console, or an SSH session
you already trust.

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub   # ssh-ed25519
ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub     # ecdsa-sha2-*
ssh-keygen -lf /etc/ssh/ssh_host_rsa_key.pub       # ssh-rsa
```

The `SHA256:...` value must match the panel's fingerprint exactly. Running
`ssh-keyscan <ip> | ssh-keygen -lf -` from your own machine is weaker: it goes
over the same network path as the panel.

### Replacing or pre-pinning a key

Instance operators only.

1. On the server's page, under **Host key**, choose **Verify or replace**. The
   panel scans the server without sending credentials. It shows the pinned
   fingerprint and the one the server presents now, in red when they differ.
2. Compare the presented fingerprint on the server's console, as above, and
   tick **The fingerprint matches what the server's console shows**.
3. Choose **Replace pinned key**, or **Pin this key** if none is pinned yet,
   and confirm your password.
   - The panel scans again and pins the key only if the server still presents
     the fingerprint you confirmed. Otherwise nothing changes, and you see the
     new fingerprint to compare.
   - Replacing a key closes the server's open SSH connections. Operations
     that started before the replacement then fail rather than reconnect with
     the old key.
   - Each replacement is written to the audit log as `server.host_key_reset`.

Use the same dialog on a new server, before its first connection, to pin a key
you have checked instead of trusting the first connection.

The API behind the dialog:

- `POST /api/v1/servers/{serverID}/host-key/scan`: developers and owners;
  viewers get 403.
- `PUT /api/v1/servers/{serverID}/host-key` with
  `{"fingerprint": "SHA256:..."}`: instance operators only, from a signed-in
  session with a password proof from `POST /api/v1/auth/reauth`, sent as
  `X-Toft-Reauth` (403 `reauth_required` without one, 403 `session_required`
  with an API key). It answers 400 for a malformed fingerprint, 409 if the
  server presents a different key, and 502 if the server can't be reached.

### Break-glass: no operator can sign in

Clear the pin in the database. On the panel's server:

```sh
docker exec -it paas-postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
```

```sql
UPDATE servers SET host_key = NULL, host_key_pinned_at = NULL WHERE id = '<server-id>';
```

This re-arms trust on first use. The next new connection trusts whatever key
answers, and that connection may be an automatic one (the health poll or
metrics). Prefer the operator reset, which checks the fingerprint. After
clearing a pin, run **Test connection** at once and compare the fingerprint it
reports.

## SSH connections

The panel keeps one SSH connection per server and shares it between deploys,
logs, stats, consoles and provisioning. It opens up to two more connections
under load. A dead connection is detected by keepalives within about 45
seconds, and the next operation reconnects. After repeated failures, calls to
an unreachable server fail fast for up to a minute between attempts. **Test
connection** always tries again immediately.

A server with the agent carries the same SSH sessions inside the connection
its agent opened, so none of this needs an open SSH port.

## Metrics

Every minute the panel records the CPU and memory of each running service. It
samples each server with a single command over the server's connection: the
running containers, one `docker stats --no-stream`, each container's cgroup
counters (CPU throttling and pressure) and the memory kills since the last
sample. Up to 8 servers are sampled at a time, and each gets 20 seconds. A
server that can't be reached is skipped; the backend logs a warning when it
starts failing and a note when it recovers, not every minute.

A memory kill raises an `oom_killed` alert. Per-minute samples are kept for
48 hours, then folded into daily rows that are kept for 30 days.

A service's figures are the sum of all its running containers: every container
of a compose stack, and both containers during a blue/green switch. A
container counts for a service through its `paas.service_id` label only when
it is a blue/green container the platform created for that service
(recognised by its reserved `paas-` name), and otherwise through the compose
project the platform runs the service's stack under. A label baked into an
image therefore can't move a container's usage to another service.

A server with the agent samples itself and keeps up to 48 hours of readings
while the panel is away; the panel fetches the ones it missed when the agent
reconnects.

## Blue/green switches

A git or image deploy starts the new container next to the one serving. The
previous container keeps serving while the new one is health-checked and its
post-deploy commands run, and Traefik balances requests across both. Only then
is the previous container removed. If any of that fails, or the new container
has stopped by then, the new container is removed, and the service keeps
running the previous deployment with its status unchanged.

On a server reached over SSH, Traefik reads its routes from the containers'
labels, and it drops a route that two running containers define differently.
So a deploy that changes the domains, the port or the access control
(basic-auth credentials, IP allowlist) would leave the service's routes down
while both run. For such a deploy the previous container is removed as soon as
the new one passes its health check, before the post-deploy commands, and the
deploy log says so. The routes are then down only for the health check, but a
post-deploy command that fails can no longer bring the previous container
back: the deploy fails and the new container keeps running.

On a server with the agent, the agent writes Traefik's routes to files. The
previous container keeps the route until the new one has passed everything,
then the route moves in one step and the previous container is removed.

If both containers are left running (the previous one could not be removed),
the next deploy replaces the one that doesn't serve the active deployment.

## Deploys interrupted by a restart

A deploy to a server over SSH, or to the panel's own server, runs inside the
backend, so restarting the backend (an upgrade, a crash) interrupts it. It is
not run again, because that would repeat migrations and post-deploy commands.
Instead, at startup:

- Every such deployment left pending, queued or building is marked
  **failed**, with an error summary starting `interrupted:` and a line in its
  log, so its log stream ends.
- For each service whose newest deployment was interrupted, the panel checks
  the service's containers and sets the service's status to what runs. The
  outcome is added to the deployment's summary and log:
  - nothing changed: the previous container is still serving;
  - the restart came mid-switch: the new container is removed and the
    previous one keeps serving;
  - the new container is the only one running: it becomes the active
    deployment, but its health check and post-deploy commands may not have
    completed, so check the service, then redeploy or roll back;
  - anything else (a lone container that can't be tied to a deploy, both
    containers running with nothing to tell which is new, a compose stack,
    three or more containers) is left running and flagged: check it, then
    redeploy.
- A container is told apart by the deployment label the platform gives it or
  by the image a git deploy built; its creation time only separates two
  running containers. Only the service's own blue/green containers count: a
  container whose image carries the service's label is ignored.
- A server that can't be reached is tried again after 30 seconds, 2 minutes
  and 5 minutes. The service's status is left as it was until then. A check
  that another restart cuts short is made again on the next start.

A deploy that fails while the backend shuts down is left to this check instead
of being marked failed, because the shutdown may be what failed it. The check
assumes a single backend instance.

A deploy to a server with the agent is handed to the agent, which finishes it
whatever happens to the panel. After a restart the panel picks up its log
where it stopped ("The panel restarted; following the rollout the server's
agent runs") and records the result.

To see which deployments a restart would mark, in the database shell above:

```sql
SELECT id, service_id, status, created_at FROM deployments
WHERE status IN ('pending', 'queued', 'building') AND handed_off_at IS NULL
ORDER BY created_at;
```

## The panel's own health

**Instance → Overview** shows whether the panel is well: its containers, its
certificate, backups and the watchdog.

### Alerts about the panel

The panel watches itself and posts these alerts to the alert webhook
(**Settings → Alerts**) of every workspace an instance operator owns:

| Alert | When |
| --- | --- |
| `panel_container` | A panel container is missing, stopped, restarting or failing its health check. Checked every minute. |
| `panel_certificate` | The panel's certificate expires in under 14 days (Let's Encrypt renews at 30), has expired, or is still Traefik's self-signed one. |
| `panel_upgrade_failed` | An upgrade rolled back or stopped. |
| `security_updates`, `reboot_required`, `docker_advisory` | The panel's server's updates, as a server's ([Updates](/toft/guide/servers/#updates)). |

The container, certificate and updates alerts post again when they are resolved.

### The watchdog

If the panel's server goes down, the panel can't tell you. A watchdog outside
it can. Under **Watchdog** on **Instance → Overview**:

1. Make a heartbeat check in a monitoring service, such as healthchecks.io,
   Better Stack heartbeats or an Uptime Kuma push monitor.
2. Paste its URL into **Ping URL** and choose **Save**. It must be an
   `https://` URL that takes a GET; private addresses are refused.
3. **Send a test ping** checks it answers.

Toft pings the URL every 5 minutes while the panel is healthy. While a panel
container is unhealthy, or the database doesn't answer, it sends nothing, so
the watchdog alerts you.

Also point an uptime monitor at the panel's health check,
`https://<panel-domain>/api/v1/health`. It answers 200 while the panel and its
database are up, and 503 while the database doesn't answer.
