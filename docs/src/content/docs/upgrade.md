---
title: Upgrade
description: Move the panel to a new release from the panel or the command line, with a database save and automatic rollback.
sidebar:
  order: 3
---

An upgrade moves the panel's server to a new release in place. It saves the
database first, and if the new release isn't healthy within 2 minutes, the
previous one starts again by itself. Apps keep running throughout: only the
panel restarts.

This page covers installs made with the installer. A panel that
`deploy.sh` runs shows how to move it onto the installer on
**Instance → Overview**.

## From the panel

When a newer release is out, **Instance → Overview** shows "Toft vX.Y.Z is
available" with its release notes.

1. Select **Upgrade…** (or **Upgrade to vX.Y.Z** at the top of the page).
2. Read the steps, enter **Your password** and confirm.
3. The page follows the upgrade step by step. The panel is away for about
   30 seconds while it restarts.

Only the newest release is offered. The Version card shows the release that
runs, the newest one, and the last upgrade with its **Log**. The audit log
records upgrades started from the panel, with who started them and how they
ended.

## From the command line

On the panel's server, as root:

```sh
sudo toft upgrade --to vX.Y.Z
```

The panel's **Upgrade** button runs this same command on the host, so
both paths behave the same way.

| Flag | What it does |
| --- | --- |
| `--to vX.Y.Z` | The release to run. Required. |
| `--force` | Allow the same or an older release. Use it to re-run a release or to go back to an earlier one. |
| `--repo ghcr.io/OWNER` | Where the images are. Defaults to `ghcr.io/` and the `GHCR_USER` in `/etc/toft/secrets.env`, or `TOFT_IMAGE_REPO` when set. |

## What an upgrade does

Every upgrade runs the target release's own `toft`, copied out of its image,
so fixes to the upgrader ship with the release that needs them. Then:

1. **Preflight.** Docker 29 or newer, and 3.3 GB free for the images and a
   database dump.
2. **Pull.** The release's backend and frontend images, while the running
   release keeps serving. They are pulled by the digests the release's
   image manifest names, once its signature by the release key checks out,
   so a tag moved in the registry can't change what runs. A release from
   before v0.46.0 has no signed manifest, and is refused.
3. **Save.** A `pg_dump` of the panel's database to `/var/lib/toft/upgrade/`.
   The last 3 dumps are kept.
4. **Migrate.** The schema moves to the release's. A failed migration stops
   the upgrade, and the running release keeps running.
5. **Restart.** `/opt/toft` points at the new release's bundle and the panel's
   containers start on the new images.
6. **Health.** Each container must report healthy, on the release's images,
   within 2 minutes.

After a healthy start, the upgrade updates the host's units (the firewall, the
backup and release-check timers and the apps' slice), installs the new
`toft`, and keeps only the running and the previous release's bundles. Each
upgrade's log is in `/var/lib/toft/upgrade/<version>-<time>.log`, and a
failed upgrade prints its path.

An upgrade also records the server's public IP as `TOFT_PANEL_IP` in
`/etc/toft/secrets.env` if it isn't there yet. A panel with no domain makes its
apps' temporary `sslip.io` addresses from it. If the upgrade can't find the IP,
it says so: run the installer again to record it.

Only one upgrade runs at a time.

## Rollback

The schema only moves forward. Each release's schema works with the previous
release's code, and the schema migration never drops a table, a column or a
foreign key. A release that does drop one runs it as a separate, planned step.

If the new release isn't healthy within 2 minutes, the upgrade rolls back by
itself:

- Normally, the previous release's images start again on the newer schema.
  Nothing is lost.
- If the release ran a planned drop, the database is first restored from the
  dump taken at the start of the upgrade. Anything written since that dump is
  lost.

The upgrade then reports that it failed and which release is running.

To go back to an earlier release by hand, upgrade to it with `--force`:

```sh
sudo toft upgrade --to vX.Y.Z --force
```

A release from before v0.46.0 can be gone back to only if it is the one the
panel ran just before, from its images still on the host.

:::caution
From v0.28.0, secrets are stored in a format only v0.27.0 and later can read.
Don't go back to a release before v0.27.0. To upgrade from a release before
v0.27.0 to v0.28.0 or later, upgrade to v0.27.0 first; `toft upgrade` refuses
the direct jump.
:::

## Release checks

`toft-release-check.timer` asks the image registry for the newest release once
a day (first 10 minutes after boot, with up to an hour's random delay). The
panel shows the result on **Instance → Overview**; **Check now** in the Version
card runs it at once. To run it on the host:

```sh
sudo toft release-check
```

Pre-releases are never offered as the newest release. The check signs in with
the server's `docker login` for the registry, so it works for private images
too.

## Servers with the agent

Servers that connect with the agent upgrade themselves. When an agent older
than the panel connects, the panel sends it the panel's agent version. The
agent checks the release signature, refuses a downgrade, and waits for any
deploy it is running to finish. It then restarts into the new version, and
goes back to the old one by itself if it can't reconnect within 2 minutes.

Servers added over SSH have no agent to upgrade.

## Rotating the encryption key

`ENCRYPTION_KEY` in `/etc/toft/secrets.env` encrypts every secret the panel
stores. To replace it, on the panel's server:

```sh
sudo toft rotate-encryption-key
```

It writes a new key and keeps the old one as `ENCRYPTION_KEY_PREVIOUS`,
restarts the backend, which re-encrypts every secret under the new key on
start, checks that none is left under the old key, removes the old key, and,
if backups are set up, starts one. Secrets stay readable at every step: if it stops part-way,
run it again and it resumes.

Afterwards, store the recovery kit again (**Instance → Overview**, **Show kit**, then
**Mark as stored**): it holds the key, and Toft shows the stored one as out of date
until you do. Backups made before the rotation restore with the old kit. See
[Backups and restore](/toft/guide/backups/).
