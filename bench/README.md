# Benchmark

Toft, Coolify and Dokploy measured the same way on matching fresh servers.
These scripts produce the numbers in the README, so anyone can rerun them
and check.

## Method

- **Servers:** three Hetzner Cloud servers of one x86 shared-vCPU type with
  2 vCPU and 4 GB, running Ubuntu 24.04, all in one location, one platform
  each. They run side by side. A fourth, smaller server in the same location
  loads the dashboards.
- **Rounds:** three. Each round rebuilds the servers from the image. Results
  are the median over every round, with the range.
- **Versions:** each platform's stable release, pinned and recorded. Every
  platform keeps its defaults. The one exception is Coolify's
  `AUTOUPDATE=false`, which stops it from updating itself mid-run.
- **App:** [`traefik/whoami`](https://github.com/traefik/whoami) v1.12.0,
  deployed two ways on every platform: from its published image, and built
  from its git tag with its own Dockerfile. It replies with its container's
  hostname, which is how a finished redeploy is detected.

| Metric | How |
|---|---|
| Install | Time from the install command until the panel answers |
| Idle memory | Memory in use (`MemTotal − MemAvailable`) minus the same server's reading before the install. After 10 minutes of settling, a reading every 10 s for 10 minutes; the median. |
| Idle CPU | `/proc/stat` over the same window, as a share of both vCPUs |
| Disk | `df /` before the first deploy (after the install and the idle readings) minus before the install |
| First deploy | Time from the first API call that creates the app until it answers through the platform's proxy |
| Redeploy | Five per app, from the trigger until a new container answers. A probe on the server sends one request after another, from the trigger until 20 answers in a row come from the new container and the previous one has stopped, plus 5 s, and counts the seconds in which a request failed. A redeploy is the same commit again, as each platform does it: one that reuses an image it already built is faster for it. |
| Dashboard | The main page loaded ten times, signed in, each time with a cold cache. Records the load event, the largest paint, and the bytes and requests in the first 10 s. |

The app counts as ready when it answers over HTTPS through the platform's
proxy, with certificates not checked. Let's Encrypt's timing is therefore
not counted on any platform. Readiness is judged from the app's answers, not
from each platform's own deploy status.

**Done by hand on Coolify and Dokploy, not timed:** the first account and its
API token. Neither documents a way to do it without a browser. Coolify's
installer can make the account, but it checks the address's domain for mail,
and `example.com` takes none: the account is made on Coolify's first page,
which only allows it within 10 minutes of Coolify starting
(`docker restart coolify` reopens it). The token needs root. Toft's are
made by the script, and it finishes Toft's setup as a new owner would. The
hand step ends with signing out and closing the tab, so no open dashboard
counts in the idle readings. If Coolify's proxy has to be started by hand,
that is outside its install time, and the results say so.

## Servers

With [`hcloud`](https://github.com/hetznercloud/cli) and an API token for the
project (`hcloud context create bench`):

```sh
hcloud server-type list   # pick the cheapest x86 shared type with 2 vCPU and 4 GB
hcloud ssh-key create --name bench --public-key-from-file ~/.ssh/id_ed25519.pub
for s in toft coolify dokploy; do
  hcloud server create --name bench-$s --type TYPE --image ubuntu-24.04 --location LOCATION --ssh-key bench
done
hcloud server create --name bench-dashboard --type TYPE --image ubuntu-24.04 --location LOCATION --ssh-key bench
```

Before each later round, rebuild the three servers. The dashboard server
stays as it is:

```sh
for s in toft coolify dokploy; do
  hcloud server rebuild --image ubuntu-24.04 bench-$s
  ssh-keygen -R "$(hcloud server ip bench-$s)"
done
```

Delete all four servers afterwards (`hcloud server delete bench-…`).

## A round

Run the three platforms at the same time, one terminal each, from the
repository root. Each run takes about 45 minutes, and needs `ssh`, `curl`,
`jq` and `perl`.

```sh
TOFT_VERSION=vX.Y.Z bench/run.sh toft root@TOFT_IP 1
bench/run.sh coolify root@COOLIFY_IP 1    # COOLIFY_VERSION, default 4.5.3
bench/run.sh dokploy root@DOKPLOY_IP 1    # DOKPLOY_VERSION, default v0.30.10
```

Coolify and Dokploy stop once for the hand step. Each run then prints its
dashboard line. Run that line on the dashboard server:

```sh
scp -r bench root@DASHBOARD_IP:            # once; Docker there: curl -fsSL https://get.docker.com -o get-docker.sh && sh get-docker.sh
ssh root@DASHBOARD_IP
export PLATFORM=… ROUND=… PANEL=… LOGIN=… PAGE=… EMAIL=… PASSWORD=…   # the printed line
docker run --rm -v /root/bench:/bench -w /bench \
  -e PLATFORM -e ROUND -e PANEL -e LOGIN -e PAGE -e EMAIL -e PASSWORD \
  mcr.microsoft.com/playwright:v1.64.1-noble \
  sh -c 'npm i --no-save --silent playwright@1.64.1 && node dashboard.mjs' >"$ROUND-$PLATFORM-dashboard.json"
```

Then copy that file into `bench/results/`. `bench/summary.sh` prints the
table; `bench/summary.sh FOLDER` prints a later set's.

`bench/results/` holds the three platforms' rounds of 2026-10-29 and 30
(Toft v0.29.0). `bench/results/2026-10-30-v0.30/` holds Toft's rounds on
v0.30, after its git builds stopped putting `.git` in the build context
(rounds 1 and 2 on v0.30.0, round 3 on v0.30.1, which only changes the
install's wait for the database). Its servers were slower that day: the
install and the first build took about 20 % longer, and the dashboard,
unchanged since v0.29.0, loaded more slowly. That folder's dashboard files
are kept for the record and are not compared.

Each run writes the following to `bench/results/`:

- `ROUND-PLATFORM.json`: the measurements
- `-fresh.txt` and `-idle.txt`: the raw readings
- `-containers.txt`: the per-container breakdown

The password is shown in the terminal and kept out of `bench/results/`. On
the servers it is where each platform keeps it (Coolify's installer writes it
to its `.env`); delete the servers afterwards.
