---
title: Backups and restore
description: Nightly encrypted backups of every server to S3-compatible storage, the recovery kit, and how to restore a panel or a service's data.
sidebar:
  order: 5
---

Each server backs itself up once a night with restic, encrypted, to an
S3-compatible bucket. The backup runs on the server, from a systemd timer, so
it keeps working when the panel is down, and your data never passes through
the panel.

## What is backed up

| Backed up | |
| --- | --- |
| Volumes | Every Docker volume Toft made (named `paas-…`): services' volumes and shared volumes. |
| Mounted folders and files | Whatever a service bind-mounts from the server, such as its config files. |
| Databases, as dumps | Each running MySQL, MariaDB, Postgres and MongoDB container, dumped with its own tool just before the copy. Its volume is copied too. |
| The panel's database | On the panel's server: accounts, workspaces, members, settings and encrypted secrets. |
| Git repositories, as bundles | On the panel's server: every repository, written as a checked git bundle right after the panel's database, so the two always match. |

| Not backed up | |
| --- | --- |
| Other databases, as dumps | Redis, SQLite and search indexes are copied as files while they run, and such a copy may not restore cleanly. |
| Stopped databases | A stopped database container isn't dumped. Its volume is still copied. |
| Images and container files | Anything a container writes outside its volumes. Images come back with the next deploy. |
| Volumes made outside Toft | Docker volumes whose names don't start with `paas-`. |
| Paths with `:` or `,` | A mounted path containing either can't be passed to restic. The run reports it. |

A server's backup holds everything on it, including other workspaces'
services on a shared server.

## Set up backups

First create a bucket and an S3 access key at your provider. Hetzner Object
Storage, AWS S3, Backblaze B2, Cloudflare R2 and MinIO work.

- **A server you added:** a workspace owner opens the server under
  **Servers** and selects **Set up backups** in its Backups card.
- **The panel's server:** an instance operator sets it from Home's
  **Finish setting up** (Backups, **Set up**), or under **Settings → Backups**,
  in the Panel host row.

| Field | |
| --- | --- |
| Endpoint | `https://` and the host only, no path, such as `https://s3.example.com`. |
| Bucket | Backups go to `toft/<server ID>` inside it, and the panel's server to `toft/control-plane`, so servers can share a bucket. A missing bucket is created if the key may. |
| Access key, Secret key | The secret key is stored encrypted and never shown again. |
| Daily at | Server time, 24-hour. Defaults to 03:00. |

When you save a new destination, the panel shows the **repository password**
once. It decrypts the backups, and it is the only way to read them if both the
panel and the server are lost. Store it in your password manager before you
continue.

A machine has one backup: if the same machine is added as two server entries,
only one of them can have a destination.

## How it runs

- `toft-backup.timer` checks every 15 minutes whether today's backup is due,
  and runs `/usr/local/lib/toft/backup.sh` once a day from the set time. A run
  missed while the server was off starts when it's back, the same day.
- Each run dumps the databases, then backs up the dumps, volumes and mounted
  folders with restic, encrypted on the server with the repository password.
  The first run uploads everything; later runs upload only what changed.
- After each run, old copies are pruned: 7 daily, 4 weekly and 6 monthly are
  kept. This isn't configurable.
- The server writes the result to `/var/lib/toft/backup/last.json`, and the
  panel shows it under **Settings → Backups** and on the server's page.

The **Backup failing** alert fires when the last run failed, or when nothing
has succeeded for 26 hours. Up to a day of changes can be lost: whatever
changed since the last run.

To run a backup now, on the server as root:

```sh
sudo /usr/local/lib/toft/backup.sh now
```

**Stop backups** removes the destination from the server. The copies already
in the bucket stay. Setting backups up again makes a new repository password,
which can't open those copies, so use another bucket or delete them first.

## The recovery kit

The panel encrypts every secret it stores with `ENCRYPTION_KEY`, which lives
only on the panel's server. The recovery kit is a text file
(`toft-recovery-<domain>.txt`) holding:

- the encryption key;
- the panel's backup repository, its password and the access key.

It doesn't hold the S3 secret key: that stays with your provider, and the
restore asks for it.

Download the kit in the setup wizard's Recovery kit step, or later from
**Instance → Overview**, **Show kit**, with your password. Only instance
operators can see it, and every view and every **Mark as stored** is in the
audit log. Keep it off the
server: in a password manager, or printed and locked away.

The kit you stored goes out of date when the panel's backups are set up or
changed, and after
[rotating the encryption key](/toft/upgrade/#rotating-the-encryption-key): a
restore with it can't read the panel's secrets or open its backups. Toft keeps
a fingerprint of what the stored kit holds, never the kit, and tells every
instance operator on **Instance**, **Home** and in the **Inbox**. Open
**Show kit**, store the new kit, then choose **Mark as stored**. Keep the old
kit while backups made before the change still exist: restoring one of those
may need it. A copy from before a key rotation holds its secrets under the old
key, and an old repository opens only with the old kit's password.

## Restore the panel

If the panel's server is lost, bring the panel back on a fresh server from its
latest backup. You need the recovery kit and the S3 secret key.

1. Copy the kit to the new server, next to the installer:

   ```sh
   curl -fsSLO https://github.com/Nickpap21/toft/releases/latest/download/install.sh
   sudo sh install.sh --restore --kit toft-recovery-panel.example.com.txt
   ```

   Install the release the old panel ran, or a newer one: the restore moves
   the database forward to the release you install. For a specific release,
   add `--version vX.Y.Z`.

2. Enter the S3 secret key when asked. Without a terminal, set it in
   `TOFT_RESTORE_SECRET_KEY` instead.

The installer refuses to restore onto a server where Toft already runs. It
installs Toft with the kit's encryption key, then restores the latest snapshot:
the panel's database, the platform's volumes, the mounted folders, and the git
repositories from their bundles. It then upgrades the database to the
installed release. If it stops part-way, fix the cause and run the same
command again with the same kit: it continues. A kit with another encryption
key is refused there: it needs a fresh server.

After the restore:

- Everyone signs in again, with their old passwords.
- Point the panel's domain at the new server. Until it arrives, the panel
  answers on the server's IP address, where your browser warns about the
  certificate; the installer's last lines give the address. Within a few
  minutes of the domain reaching the new server with its certificate, the
  panel stops answering on the IP address. **Instance → Overview** says so
  while it lasts, and an instance operator can end it there sooner with
  **Stop answering on the IP…**, which asks for a password. The panel then
  answers only at its domain. Opened at the domain, that is safe. Opened
  on the IP address, the panel first checks that the domain reaches it;
  when it doesn't, the dialog says so and stops only once you confirm that
  you can reach the server over SSH, because while the domain points at
  another server nobody reaches this one. The way back is on the server:

  ```sh
  sudo toft answer-on-ip
  ```

  The panel then answers on the IP address again, until the domain reaches
  it or an operator stops it. On a panel that was never restored the command
  changes nothing and says so.
- The panel's backups are paused. The restore doesn't write the backup
  settings to the new server, so a restored copy never backs up into your
  bucket by itself. On the server you keep, an instance operator opens
  **Settings → Backups** and selects **Resume backups**: the stored
  destination is used again, with no key to enter, and a backup starts at
  the set time, or within 15 minutes if that has passed today.
- Services on the panel's server start at their next deploy. Services on other
  servers kept running throughout.
- A service with a temporary address gets its certificate again after that
  deploy, and Toft redeploys it once more to switch it to HTTPS. In a protected
  environment nothing deploys by itself: the **Inbox** asks for the second
  deploy.

A server whose agent took newer instructions than the backup holds shows "The
panel was restored from a backup; this server kept its own state" on its page.
Its apps keep serving, but deploys and removals there wait until an owner or
operator chooses **Adopt this server's state…** or **Use the panel's
state…**.

## Restore a service's data

The panel can't restore a service's data yet. Restore it on the server, as
root, with the settings the panel wrote there. **Settings → Backups** lists
these commands, ready to copy.

Use the server's backup settings:

```sh
R="docker run --rm --env-file /var/lib/toft/backup/backup.env restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510"
```

List the copies, then see what the latest holds:

```sh
$R snapshots
$R ls latest | head
```

Dumps are under `/data/dumps`, volumes under `/data/volumes/<volume>` and
mounted folders under `/data/binds/<path>`.

Put a volume's files back. Stop the service first if it writes there, and
leave out `<path>` to restore the whole volume:

```sh
docker run --rm --env-file /var/lib/toft/backup/backup.env -v <volume>:/data/volumes/<volume> restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510 restore latest --target / --include /data/volumes/<volume>/<path>
```

Load a Postgres database from its dump, a `pg_dumpall` of the whole server.
Use the container's `POSTGRES_USER` if it isn't `postgres`:

```sh
$R dump latest /data/dumps/<container>.dump | docker exec -i <container> psql -U postgres
```

For MySQL and MariaDB, pipe it to `docker exec -i <container> mysql -uroot -p`
instead.

When the server itself is gone, put `RESTIC_REPOSITORY`, `RESTIC_PASSWORD`,
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` in an env file on another
machine and run the same commands with it. The repository is the server's
destination, such as `s3:https://s3.example.com/your-bucket/toft/<server ID>`.

## Run a restore drill

A backup you have never restored is a guess. Once a quarter, restore the
panel onto a throwaway server with the kit:

1. Create a fresh server that meets the [requirements](/toft/install/#requirements).
2. Run the restore above with your current kit.
3. Open the new panel on its IP address, sign in, and check that your
   projects, services and repositories are there. A restored panel answers
   there, behind a certificate warning, for as long as its domain points
   elsewhere.
4. Delete the throwaway server.

:::caution
Don't point the panel's domain at the drill server, and don't deploy or change
servers from the restored panel: it holds the same server credentials as your
real one. Leave its backups paused too: resumed, they would go into your real
panel's backups.
:::

A drill also checks the parts that live outside Toft: that the kit is where
you think it is, and that you can still find the S3 secret key.
