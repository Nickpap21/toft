---
title: Install
description: Install Toft on a fresh Linux server with one command, then finish setup in the browser.
sidebar:
  order: 2
---

Toft installs with one command on a fresh Linux server. The installer checks
the server, installs Docker if it's missing, starts Toft and prints a one-time
setup code. You finish in the browser, where the first account you create runs
the instance.

## Requirements

The installer checks each of these before it changes anything. A failed check
stops it with the reason, and nothing is changed.

| Requirement | What the installer checks |
| --- | --- |
| User | Runs as root (`sudo sh`). |
| CPU | 64-bit: `x86_64` (amd64) or `aarch64` (arm64). |
| OS | Ubuntu 22.04+, Debian 12+, Rocky Linux 9+ or AlmaLinux 9+. Other distributions get a "best effort" warning, not a refusal. |
| Kernel | 5.10 or newer. |
| Init | systemd running. Toft needs it for the host firewall and backups. |
| cgroups | cgroup v2 (the unified hierarchy) mounted. |
| Disk | 11 GB free under `/var/lib`, for images, the database and backups. |
| Size | 2 vCPU and 4 GB RAM to run apps beside the panel (Toft checks for 3.5 GiB, which a server sold as 4 GB passes after the kernel's share). A smaller server is warned that it can run the panel only. |
| Ports | 80 and 443 free for Traefik, and 2222 free for git over SSH (unless you install with `--no-git`). A port in use stops it, naming the program or container that holds it. |
| Docker | Docker 29 or newer with the compose plugin. |
| OpenSSL | OpenSSL 3, to check the signature of the release's image manifest before it pulls the images by their digests. |

If Docker isn't installed, the installer installs it from Docker's own
repository on Ubuntu, Debian, Rocky Linux, AlmaLinux, RHEL and CentOS. It
never upgrades an older Docker: upgrading Docker restarts every container on
the server, so it stops and asks you to do that yourself.

## Install

On the server:

```sh
curl -fsSL https://github.com/Nickpap21/toft/releases/latest/download/install.sh | sudo sh
```

The installer released with each version installs that version. To pass
options, add them after `-s --`:

```sh
curl -fsSL https://github.com/Nickpap21/toft/releases/latest/download/install.sh | sudo sh -s -- --no-git
```

It prints one line per step and ends with the setup address and code:

```
✓ Ubuntu 24.04 · kernel 6.8 · 4 vCPU · 7.6 GiB · 65 GB free
✓ Ports 80, 443 and 2222 free
✓ Docker 29.1.2, from Docker's repository
✓ Toft v0.34.0 started: panel, API, Postgres, Traefik, git

── Finish setup in a browser within 30 minutes
  https://<server-ip>/setup
  one-time code  <setup-code>

Full log: /var/log/toft-install.log
```

Everything else, Docker's install and each step of starting Toft, goes to
`/var/log/toft-install.log`. If a step fails, the installer says which and
where it stopped: run it again once that is fixed, and it continues from
there.

Re-running the installer is safe. It changes only what is missing, never
generates a secret again, and prints a new setup code only while nobody has an
account. Run it again if the code expires.

### Options

| Option | What it does |
| --- | --- |
| `--version vX.Y.Z` | The release to install. Defaults to the release the script came with. |
| `--no-git` | Install without git hosting: no git container, no repositories and nothing on port 2222. To add git later, remove `TOFT_GIT=off` from `/etc/toft/secrets.env` and run `toft upgrade --to <this release> --force`. |
| `--pool CIDR` | The address pool for app networks. Defaults to `10.203.0.0/16`, or the next free `/16` up to `10.210.0.0/16`. Only applies when the installer installs Docker itself and no `/etc/docker/daemon.json` exists. |
| `--app SLUG` | After setup, open New service with this catalog template, such as `typesense`. |
| `--registry-token T` | A token that may pull Toft's images, for a private registry. Also read from `TOFT_REGISTRY_TOKEN`. |
| `--registry-user U` | The user name for that token (default `toft`). Also read from `TOFT_REGISTRY_USER`. |
| `--restore --kit FILE` | On a fresh server, bring a lost panel back from its backup with the recovery kit. See [Backups and restore](/toft/guide/backups/). |
| `--adopt` | On a server where `deploy.sh` runs the panel: move it onto the installer, keeping its database, volumes, settings and apps, then upgrade it to `--version`. From then on it upgrades from the panel, not `deploy.sh`. |

If another program holds port 2222, put `TOFT_GIT_SSH_PORT=<port>` in
`/etc/toft/secrets.env` and run the installer again, or install with
`--no-git`.

On every run the installer records the server's public IP as `TOFT_PANEL_IP`
in `/etc/toft/secrets.env`. A panel with no domain makes its apps' temporary
`sslip.io` addresses from it, so if the server's IP changes, run the installer
again.

## Setup

Open the address the installer printed. Until the panel has a domain, it
answers on the server's IP with a self-signed certificate, so your browser
warns you. The setup wizard has four steps, all required:

1. **Your account.** Enter the **Setup code** from the installer, your name,
   email and a password of at least 12 characters, and optionally a name for
   your first workspace. The code works for 30 minutes. This first account
   owns the first workspace and becomes the **instance operator**: it can
   create workspaces and manage operators. After this, sign-up is invite-only.
2. **Address.** Choose "A domain" or "Keep the IP for now". For a domain, add
   an A or AAAA record that points it at the server, then **Check and
   continue**. Toft checks that the name reaches this panel and asks Let's
   Encrypt for a certificate over port 80. The panel and its API move to
   `https://<your-domain>`, and you sign in again there. See
   [Domains](/toft/guide/domains/).
3. **This server.** Choose what runs on it (below).
4. **Recovery kit.** Enter your password, then **Download the kit** or copy it
   and store it off this server. Tick "I stored it somewhere other than this
   server" and **Finish and open Home**.

Backups, more servers and invites for your team come next, from **Finish
setting up** on Home. See [Backups and restore](/toft/guide/backups/) and
[Servers](/toft/guide/servers/). Toft doesn't send email: an invite is a link
you send yourself, for one person, with the role Developer, Viewer or Owner,
and it works once, for 7 days.

You can reopen the address and role steps later from **Instance → Overview**,
in the Setup card. **Show kit** there opens the recovery kit again.

## Server roles

| Role | What runs on this server |
| --- | --- |
| Run apps here too | The panel, its database and Traefik, plus your apps. The panel keeps a fixed reservation of 1 vCPU and 2 GiB, the system keeps 0.5 vCPU and 1 GiB, and apps share the rest. Offered from 2 vCPU and 4 GB. |
| Only Toft | Only the panel, its database and Traefik. Your apps run on servers you add. |

On "Run apps here too", apps run under the `toft-apps.slice` systemd
slice, sized from what the reservation leaves. A busy app slows down or is
stopped; the panel isn't.

## Firewall

Allow these inbound in your provider's firewall:

| Port | For |
| --- | --- |
| TCP 80 | HTTP, and Let's Encrypt's certificate checks. |
| TCP 443 | The panel and your apps over HTTPS. |
| TCP 2222 | Git over SSH (or the port in `TOFT_GIT_SSH_PORT`). Not needed with `--no-git`. |

Keep your own SSH port open too. The backend (8080), the frontend (3000) and
Traefik's dashboard (8081) listen on `127.0.0.1` only.

Toft also installs a host firewall, `toft-firewall.service`, which limits what
app containers may reach on the server itself: only ports 80 and 443 and DNS,
and never the cloud metadata range `169.254.0.0/16`.

## Where things live

| Path | What it holds |
| --- | --- |
| `/etc/toft/secrets.env` | Settings and secrets, root only (0600). `ENCRYPTION_KEY` here decrypts every secret the panel stores. |
| `/opt/toft` | A link to the running release's bundle: its compose files and Traefik config. |
| `/var/lib/toft/releases/` | The running and the previous release's bundles. |
| `/var/lib/toft/upgrade/` | Upgrade state and logs, and the database dumps taken before upgrades (the last 3). |
| `/var/lib/toft/traefik/` | The panel's Traefik files. Its access log is the container's (`docker logs paas-traefik`). |
| `/var/lib/toft/backup` | A link to `/var/paas/backup`: this server's backup settings and the last result. |
| `/var/paas` | Toft's data directory. |
| `/usr/local/bin/toft` | The `toft` command, for upgrades. |
| `/var/log/toft-install.log` | What each run of the installer did, appended, root only. |
| `/usr/local/lib/toft/backup.sh` | The backup script the backup timer runs. |

The systemd units are `toft-firewall.service`, `toft-backup.timer`,
`toft-release-check.timer` and `toft-apps.slice`.

## Next

- [Upgrade](/toft/upgrade/) to a new release.
- [Add servers](/toft/guide/servers/) for your apps.
- Keep your code on GitHub if you like: a push there deploys too, through
  the project's webhook. See [Deploy on push](/toft/guide/deploys/#git).
- [Set up backups](/toft/guide/backups/), and learn how to restore.
