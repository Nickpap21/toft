---
title: Servers and the agent
description: Add the servers your apps run on, over SSH or with the outbound node agent, and size, share and protect them.
sidebar:
  order: 1
---

Apps run on servers you add to Toft. The panel stays on its own host; each
server you add gets Docker, a host firewall and its own Traefik, so it keeps
serving its apps when the panel is down.

Only a workspace's owners add servers.

## Requirements

Add a server shows these checks before anything is installed, and
provisioning refuses a server that misses one before it changes anything.

| Requirement | |
| --- | --- |
| OS | 64-bit Linux. Tested on Ubuntu 22.04+, Debian 12+, Rocky Linux and AlmaLinux 9+ |
| CPU | amd64 or arm64 |
| Init and kernel | systemd, cgroup v2, kernel 5.10 or newer |
| Docker | Docker Engine 29 or newer, or none: provisioning installs it |
| Ports | 80 and 443 free for Traefik (an existing Traefik container is adopted) |

Toft never upgrades Docker for you: a non-patch Docker upgrade restarts every
container on the machine. If the server's Docker is older than 29, upgrade it
yourself, then choose **Check again**.

## Choose how Toft reaches the server

Open **Servers**, then **Add server**. Name the server and pick one of three
ways under **How Toft reaches it**:

| Option | What happens | Needs |
| --- | --- | --- |
| **Agent: run one command on the server** (recommended) | You paste a one-time join command into the server's console as root. The agent installs and connects out to the panel. | Outbound HTTPS from the server to the panel. No inbound port, no key to hand over. |
| **Agent: Toft installs it over SSH** | Toft checks the host key and requirements over SSH, provisions the server, then installs the agent and switches to it. | SSH as root with a private key, reachable from the panel during setup. |
| **SSH only** | The panel signs in over SSH for every deploy, log and shell. | SSH as root with a private key, reachable from the panel at all times. |

Either way Toft installs the same things, and a server can move between SSH
and the agent later without restarting its apps.

## Add a server over SSH

**1. Connect.** Fill in:

| Field | |
| --- | --- |
| Name | Shown in the panel, for example `fsn1-prod-03` |
| Address (IP or hostname) | Where the panel connects |
| User | `root` by default |
| SSH port | `22` by default |
| Private key | Ed25519 or RSA, OpenSSH format. Stored encrypted and never shown again. |

If you don't have a key for Toft yet, make one on your computer and let it
sign in to the server:

```sh
ssh-keygen -t ed25519 -C toft -f ~/.ssh/toft
ssh-copy-id -i ~/.ssh/toft.pub root@203.0.113.12
cat ~/.ssh/toft
```

Paste the output of the last command into **Private key**. One key can serve
every server. `ssh-copy-id` needs password sign-in; if the server only accepts
keys, add `~/.ssh/toft.pub` to its `~/.ssh/authorized_keys` from your
provider's console.

Choose **Connect and check**. The panel connects, pins the server's SSH host
key and reads the system. Nothing is installed yet.

**2. Verify.** The panel shows the host key's `SHA256:` fingerprint. Check it
on the server's own console (your provider's web console, or a session you
already trust), with the command the page shows for the key type, such as:

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

Tick **The SHA256: fingerprint matches what the server's console shows**. If
it doesn't match, choose **It doesn't match: remove server**. Every later
connection must present this key; see
[SSH host keys](/toft/guide/operating/#ssh-host-keys). The requirements are
listed under it, with the fix for any that fail.

**3. Provision.** Choose **Provision *name***. You can follow the log, or choose
**Stop watching**: the run carries on on the server. It is idempotent, so
**Run again** after a failure changes only what is missing. Provisioning:

1. Refuses the server if a requirement isn't met.
2. Installs Docker from `get.docker.com` if it's missing.
3. Creates the proxy's network and starts Traefik with a Let's Encrypt
   resolver ([Domains and TLS](/toft/guide/domains/)).
4. Enables `ufw` if no firewall is active: SSH on the server's SSH port, 80
   and 443 open, everything else inbound denied. An active `ufw` is left as it
   is. It installs `fail2ban` for SSH.
5. Loads the [host firewall](#the-host-firewall) for app containers.
6. Sets up the backup script and timer ([Backups](/toft/guide/backups/)).
7. Turns on automatic security updates with `unattended-upgrades`.

`ufw`, `fail2ban`, `nftables` and `unattended-upgrades` are installed with
`apt-get` when missing. Steps 4 to 7 are not fatal: a failure logs a warning
and the run goes on.

## Add a server with the agent

The agent, `toft-agent`, is a small program that dials out to the panel over a
WebSocket on port 443 and carries SSH inside that connection. Nothing on the
server has to accept connections, so it works behind NAT with every inbound
port closed.

**1. Connect.** Choose **Agent: run one command on the server**, then
**Create join command**. The server appears in **Servers** as *Waiting for its
agent*.

**2. Join.** Run the command the page shows, as root, on the server. It looks
like this:

```sh
printf 'Authorization: Bearer %s\n' <join-token> | curl -fsSL -H @- https://panel.example.com/api/v1/agent/install.sh | sudo sh
```

- The token works once, for this server only, and expires in an hour. It's
  shown only on that page; **New token** makes another.
- The token never appears on a command line other accounts can see in `ps`.
- **Read the script before running it** opens the script the command runs.
- A proxy in `HTTPS_PROXY` works.
- On a panel without a domain, the command is longer: it downloads the script
  to a temporary file and checks its SHA-256 before running it, and the script
  checks the agent's. The command also names the SHA-256 of the panel
  certificate's public key: the script's download, the agent's and the join
  send the token to that certificate only. The panel's proxy makes a new
  certificate each time it restarts, so a command made before a restart
  stops there and says to make a new one.

The script checks the server (root, Linux, amd64 or arm64, systemd, `curl`
and `sha256sum`), downloads the agent from the panel, checks its SHA-256,
installs `toft-agent.service` and waits for it to connect. The agent makes its
own Ed25519 key on the server, and the panel pins it. The page then shows
**Agent connected** with the agent key's fingerprint, which the server printed
when it joined.

**3. Provision.** Choose **Check and provision**. The panel checks the
requirements through the agent, then installs Docker, Traefik and the
firewall, as over SSH.

### What the agent keeps on the server

| Path | |
| --- | --- |
| `/usr/local/bin/toft-agent` | Link to the current release in `/usr/local/lib/toft-agent/releases/` |
| `/etc/toft-agent/agent.env` | The agent's settings |
| `/var/lib/toft-agent/` | Its key, the panel's pinned key, the release key and its state |

The service runs as root, restarts on failure, is capped at 256 MiB and
doesn't depend on Docker, so it survives Docker failing. On the server:

```sh
toft-agent status      # version, panel, agent key, last connection, last error
toft-agent check       # when it can't connect: walks the path to the panel, says where it stops
toft-agent uninstall   # removes the agent; the panel shows the server offline
```

### Signed releases and upgrades

When an agent older than the panel connects, the panel offers it the panel's
agent version. The agent installs it only if:

- the binary's SHA-256 matches, and the release signing key signed that
  version, architecture and checksum together;
- the version is newer than its own. An agent never goes back.

The agent pins the release key when it's installed. It restarts into the new
version once no rollout is running. If the new version can't reach the panel
within 2 minutes, the agent switches back to the previous one. An upgrade that
was refused or rolled back isn't offered again until the panel's version
changes or the panel restarts.

### When the panel is down

On every server, Traefik keeps routing and apps keep serving. A server with the
agent also:

- finishes a deploy it was handed, even if the panel restarts, is killed or is
  upgraded halfway through;
- keeps its containers' logs in Docker's own files (20 MiB × 5 per container)
  and ships them from where it stopped;
- keeps a reading a minute for up to 48 hours (16 MiB), so the charts have no
  gap once the panel is back;
- writes Traefik's routes itself, from the running containers.

If an agent is away for 5 minutes, the panel raises an `agent_offline` alert.

After the panel is restored from a backup, an agent holding newer state than
the backup is *Frozen after a restore*: it keeps everything running and
refuses deploys until an owner or instance operator chooses **Adopt this
server's state…** or **Use the panel's state…** on the server's page.

## Move between SSH and the agent

Owners and instance operators can move a server either way. Both ask for your
password and are written to the audit log. Running services aren't stopped or
restarted.

- **To the agent:** an SSH server's page offers **Move to the agent…**. The
  panel installs the agent over SSH and switches once its key is pinned. A
  deploy that is running finishes over SSH first. **Keep the SSH key as a
  fallback** is ticked by default; untick it to delete the key once the agent
  connects.
- **To SSH:** the Connection card's **Move to SSH…** checks the pinned host key
  over SSH, then sends every call over SSH. Without a stored key it asks for
  one. The agent stays installed and is used again only if you move back. A
  server whose services run *Sandboxed with a strict network* can't move to SSH
  until they change isolation ([Isolation](/toft/reference/isolation/)).

The Connection card also has **Join again…**, **Rotate key** (owners) and
**Revoke agent…**, for a server or key that may be compromised.

## Server roles

The panel's own host has a role, chosen in the setup wizard:

- **Run apps here too**: the panel keeps a fixed CPU and memory
  reservation and apps share the rest. Needs at least 2 vCPU and 4 GB.
- **Only Toft**: only the panel, its database and Traefik run there.
  Apps run on servers you add.

Change it from **Instance → Overview → Setup → Change role**. Servers you add
always run apps.

## Capacity

A server's **Capacity** card shows its CPU and memory. **Scan** reads them over
the server's connection; **Overrides** replace what the scan found.

Each service reserves its CPU and memory limits on its server. A deploy whose
limits don't fit in what's left is refused with `capacity_exceeded`, before
the build. The workspace setting **Who can deploy past a server's capacity**
(*Owners*, or *Owners and developers*) decides who may deploy anyway, and each
such deploy is written to the audit log. Viewers never can. On a server
another workspace shares with you, only an owner of that workspace can.

Every deploy needs a memory limit. New services get a default for their kind:

| Kind | Default memory limit |
| --- | --- |
| Database | 1 GiB |
| Cache | 256 MiB |
| Admin tool | Its own: Adminer and Mailpit 64 MiB, phpMyAdmin 128 MiB, RedisInsight and MailHog 256 MiB, pgAdmin 512 MiB |
| App | 512 MiB |

A memory limit is written as `512m` or `1g`. An instance operator can grant a
service an exception to the rule in the deploy policy.

## Updates

A server's **Updates** card, and Instance → Server for the panel's own
server, show what the last read found:

- the updates apt would install, security updates apart from the rest, with
  each package's installed and available version;
- whether a reboot waits (`/var/run/reboot-required`), since when, and for
  which packages;
- the Docker engine running, a newer one in Docker's repository, and the
  security advisories Docker has published that affect it;
- whether `unattended-upgrades` runs, when it last ran, and whether that run
  failed.

Toft reads these every 6 hours, over SSH or the agent, and when someone
presses **Check again**. The read installs nothing and doesn't refresh apt's
package lists: it uses the lists apt's daily job last downloaded, and the card
says when that was. A host that doesn't use apt shows that it can't be read.

### Installing updates, updating Docker, rebooting

Owners of the server's workspace (and instance operators) run three actions
from the card, each after their password; on Instance → Server, operators run
them on the panel's own server. Each confirmation says what goes down.

| Action | What runs | What goes down |
| --- | --- | --- |
| **Install updates** | `apt-get update`, then `apt-get dist-upgrade`, with Docker's packages held back for this run | Nothing Toft runs. Ubuntu's needrestart restarts system services whose libraries changed, and leaves Docker alone. A kernel update then asks for a reboot. |
| **Update Docker** | `apt-get install --only-upgrade` of the Docker packages installed (Docker's repository or the distribution's) | Every container on the server stops and starts again with Docker, Traefik included; on the panel's own server, the panel too. |
| **Reboot** | `systemctl reboot` | Everything on the server until it is back; on the panel's own server, the panel too. |

- Docker is held back by a pin passed to that one apt run, not `apt-mark
  hold`, so nothing stays held afterwards. A config file changed on the
  server keeps the server's copy, and apt waits for another apt (its daily
  job) to finish rather than failing. Nothing is autoremoved.
- The run is a transient systemd unit, `toft-host-update.service`, that writes
  its progress to `/var/lib/toft/host-update/`. It carries on if you close
  the page, the panel restarts, or the connection drops, and the card follows
  it when you come back. One runs at a time on a server.
- After a Docker update the run checks that the new engine runs, and restarts
  Docker itself if the package didn't.
- A reboot has finished when the server is back on a new boot; one that
  isn't back within 10 minutes reads as failed.
- While Docker updates or the server reboots, deploys to it are refused (409
  `server_updating`); a Docker update or a reboot waits for a deploy in
  progress (409 `deploy_running`).
- On an agent server the agent runs them, which needs the agent of the
  release that added them: upgrade it from the Agent card first.
- Each run is in the audit log: `server.updates_started`, then
  `server.updates_succeeded` or `server.updates_failed`, as whoever started
  it (`panel.updates_*` for the panel's own server).

Toft doesn't set Docker's `live-restore`: it keeps containers running only
through Docker's patch releases, not from one minor release to the next.

Docker's advisories come from
[moby/moby's security advisories](https://github.com/moby/moby/security/advisories).
Each release of Toft ships the list as it was, and the panel fetches the list
from GitHub daily to add any published since. A panel without access to
`api.github.com` uses the shipped list. A distribution's `docker.io` package
isn't matched against them: its maintainers backport fixes without changing
Docker's version.

Three alerts come from the read, posted to the alert webhook of the server's
workspace (**Settings → Alerts**), and for the panel's own server to those
[the panel's alerts](/toft/guide/operating/#alerts-about-the-panel) go to:

| Alert | Fires when |
| --- | --- |
| `security_updates` | Security updates wait and `unattended-upgrades` ran since the lists came down without installing them, or isn't installed, isn't running, failed its last run or hasn't run for 2 days. |
| `reboot_required` | A reboot waits. |
| `docker_advisory` | A published advisory affects the running engine. |

Each resolves at the first read that finds it fixed.

## Disk space

A server's **What fills the disk** card, and Instance → Server for the panel's
own server, read Docker's disk use through the Docker API (at most every 5
minutes; **Read again** reads now) and sort every image into one class:

| Class | What it is | Kept? |
| --- | --- | --- |
| In use | An image a container uses, running or stopped | Kept |
| Kept for rollback | The images of each service's last 10 live deploys | Kept |
| The panel's previous release | On the panel's own server, the running release and the one before, for `toft rollback` | Kept |
| Untagged images | No tag, and no container uses them | Safe to delete |
| Old builds | Toft's builds older than the deploys each service keeps, and the builds of deleted services | Safe to delete |
| Old panel releases | On the panel's own server, every other release | Safe to delete |
| Pulled images no service uses | Pulled images no kept deploy names; a deploy that needs one pulls it again | Safe to delete |

How many deploys keep their image is each service's **Rollback images kept**,
under its **Settings → Resources** (1 to 100; empty is 10). A deploy whose
build was removed can't be rolled back to: its **Roll back** is greyed and
says so, and the API answers 409 `image_removed`. Deploy that version again
to run it. A service deployed from an image rolls back as before: the
rollback pulls the image again.

**Free space** removes the classes you tick, all of them ticked to start with,
after your password. Images are removed by tag, or by ID when untagged, and
never forced: Docker keeps an image a container starts from during the clean.
Nothing restarts. **Clear all build cache**, unticked to start with, also
deletes the whole build cache, so the next build of each service starts from
nothing. A clean waits for a deploy running on the server (409
`deploy_running`), and deploys to the server wait for the clean (409
`disk_clean_running`). The card follows the clean and says what it freed.

**Volumes no service uses** are listed one by one, each deleted on its own
after you type its name and your password, never automatically:

- a volume of a service that was deleted (`paas-vol-…`);
- a volume of a compose project no service deploys any more;
- an anonymous volume, an image's declared `VOLUME` left after its container
  was removed. It may hold a database, and Docker doesn't record whose it
  was; the confirmation gives a command to look inside it first.

### The disk policy

Each server's card has a **Disk policy**, set by its owners (operators on the
panel's own server). Each value has a default, and a value set back to its
default follows the default again.

| Setting | Default | What it does |
| --- | --- | --- |
| Build cache limit | 10% of the disk, at least 1 GB | Toft trims the build cache to this after each build, keeping what builds used last. Docker's own garbage collection starts only when the disk is about 80% full. |
| Clean automatically | Untagged images and old builds (and old panel releases on the panel's own server) | Once a day, and at once when the disk alert fires (at most once an hour). Pulled images can be added; volumes and the whole build cache never are. Empty turns it off. |
| Disk alert at | 85% used, resolving under 80% | 50 to 99%; it resolves 5 points under. |

When the automatic clean runs because the disk alert fired, the alert says
what it freed and how much more needs a person.

Each clean and volume deletion is in the audit log:
`server.disk_clean_started`, `server.disk_cleaned` and
`server.volume_removed` (`panel.*` for the panel's own server), with a policy
change as `server.disk_policy_changed`. An automatic clean is listed as
Toft's own.

## Shared servers

A server's owner workspace can share it with another workspace the same person
owns, within a CPU and memory share, under **Sharing** on the server's page.

- Sharing needs isolated app networks (`TOFT_APP_NETWORKS=isolated`), so one
  workspace's apps can't reach another's.
- The shares together must fit the server.
- The other workspace sees the server's name and its share, not its address.
  Its deploys are checked against its share.
- A share can't be removed while that workspace has services on the server.

## The host firewall

Provisioning loads a Toft-owned nftables table, `ip toft`, at boot through
`toft-firewall.service`, before Docker. Traffic from any container:

- may open connections to the host's own addresses only on TCP 80 and 443
  (Traefik) and on DNS;
- may reach nothing in `169.254.0.0/16` (cloud metadata) but DNS.

Replies to connections the host opened, the internet, and other containers on
the same network are not affected. An untrusted preview's network is also kept
from private addresses outside it and from SMTP
([Previews](/toft/guide/previews/)).

If the table fails to load at boot, Docker still starts anyway. A provisioning
run that can't load it logs a warning and carries on. To check it on a server:

```sh
systemctl status toft-firewall
nft list table ip toft
```

To remove it:

```sh
systemctl disable --now toft-firewall && nft delete table ip toft
```
