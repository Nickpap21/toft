# Benchmark

Toft, Coolify and Dokploy measured the same way on matching fresh servers.
These scripts produce the numbers in the README, so anyone can rerun them
and check.

## Results

Toft v1.0.3 on 2026-11-15; Coolify 4.5.3 and Dokploy v0.30.10 on 2026-11-13,
the last time all three ran on one day. Each on its own Hetzner Cloud
`cx23`. Median of three rounds, range in brackets. The README shows six of
these rows.

| | Coolify 4.5.3 | Dokploy v0.30.10 | Toft v1.0.3 |
|---|---|---|---|
| Measured | 2026-11-13 | 2026-11-13 | 2026-11-15 |
| Install (s) | 84.1 (83.6–84.4) | 248.1 (246.7–260.2) | 56.5 (56.5–57.6) |
| Idle memory (MiB) | 663 (659–680) | 1020 (1016–1060) | 386 (376–393) |
| Idle CPU (% of 2 vCPU) | 5.9 (5.8–6.3) | 1.5 (1.3–2.6) | 1.2 (1–1.3) |
| Disk (MiB) | 3211 (3200–3215) | 5054 (5054–5054) | 2293 (2293–2293) |
| First deploy, image (s) | 7.8 (7–8.4) | 5.7 (5.3–6) | 5.5 (5.4–6) |
| First deploy, build (s) | 45.1 (44.2–48.4) | 84.1 (78.1–87.5) | 52.9 (52.9–54.5) |
| Redeploy, image (s) | 6.9 (4.8–8.3) | 6.4 (5.6–6.6) | 4.4 (4–5) |
| Redeploy, build (s) | 8.4 (6.8–10.6) | 71.3 (67.3–81) | 4.1 (3.6–5.6) |
| Seconds with failed requests per redeploy, image | 3 (0–3) | 0 (0–0) | 0 (0–0) |
| Seconds with failed requests per redeploy, build | 3 (0–3) | 0 (0–0) | 0 (0–0) |
| Dashboard load (ms) | 573.5 (501–630) | 346.5 (296–435) | 210.5 (190–269) |
| Dashboard largest paint (ms) | 602 (516–652) | 458 (396–552) | 360 (308–456) |
| Dashboard transfer (KiB) | 3811 (3811–3925) | 1533 (1530–1578) | 480 (476–483) |
| Dashboard requests | 18 (18–18) | 119 (119–119) | 46 (40–51) |

A redeploy is the same commit again; failed seconds are counted over 15
image and 15 build redeploys per platform.

**The two dates are not one comparison.** Toft's three rounds of 2026-11-15
ran at the same time on three servers with one CPU (AMD EPYC-Rome; the loop
took 1.9 to 2.0 s on each). The rounds of 2026-11-13 didn't record the CPU
under each server, and one `cx23` has been measured at less than half the
speed of another. Every row in seconds or milliseconds depends on it, and so
does idle CPU: across the two dates those rows are a rough guide, not a
ranking. Memory, disk, transfer, requests and the seconds with failed
requests don't depend on it. On 2026-11-13, with all three on one day, Toft
v1.0.0 was the slowest to build an app the first time (105.3 s) and to
paint its dashboard (676 ms); that column is in
[`results/2026-11-13-v1.0.0/`](results/2026-11-13-v1.0.0/).

Coolify and Dokploy were not run through on 2026-11-15. Their installs were
timed once each that day, on the same CPU and at their releases of the day:
Coolify 4.7.5 in 82.6 s, Dokploy v0.31.2 in 123.6 s. One reading each, so
they are not in the table.

The readings are in [`results/2026-11-15-v1.0.3/`](results/2026-11-15-v1.0.3/)
and [`results/2026-11-13-v1.0.0/`](results/2026-11-13-v1.0.0/), and earlier
runs beside them.

## Method

- **Servers:** three Hetzner Cloud servers of one x86 shared-vCPU type with
  2 vCPU and 4 GB, running Ubuntu 24.04, all in one location, one platform
  each. They run side by side. A fourth, smaller server in the same location
  loads the dashboards.
- **The CPU under them:** one server type runs on more than one CPU model,
  and the same loop took 2.0 s on one `cx23` and 4 to 5.4 s on another
  (2026-11-14). Each run records the model and that loop's time
  (`cpu_model`, `cpu_loop_s`), and `bench/summary.sh` prints them. If the
  three servers differ, delete and create them again until they match:
  the timed rows compare the hardware otherwise. The rounds up to
  2026-11-13 did not record it.
- **Rounds:** three. Each round rebuilds the servers from the image. Results
  are the median over every round, with the range.
- **Versions:** each platform's stable release, pinned and recorded. Every
  platform keeps its defaults. The one exception is Coolify's
  `AUTOUPDATE=false`, which stops it from updating itself mid-run.
- **App:** [`traefik/whoami`](https://github.com/traefik/whoami) v1.12.0,
  deployed two ways on every platform: from its published image, and built
  from its git tag with its own Dockerfile. It replies with its container's
  hostname, which is how a finished redeploy is detected.
- **Toft's built app is deployed with `?force=true`** since v1.0.2. It is the
  second app on the panel's own server, and a 4 GB server has room for one
  at the default 512 MiB memory limit: Toft refuses the second
  (`capacity_exceeded`) until an operator deploys anyway, which `force` is.
  v1.0.0 had no such check.

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
(`docker restart coolify` reopens it). The token needs root, and Coolify
4.5.3 starts with its API turned off: turn it on in its settings (or set
`instance_settings.is_api_enabled` in its database). Dokploy's key needs
its organisation and its rate limit turned off; with the default, it allows
10 requests a day. Toft's account and key
are made by the script, and it finishes Toft's setup as a new owner would. The
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

Coolify and Dokploy stop once for the hand step and read the token from the
terminal; `BENCH_TTY=FILE` reads it from that file (a FIFO) instead. Each
run then prints its
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
are kept for the record and are not compared. `bench/results/2026-11-13-v1.0.0/`
holds all three platforms' rounds of 2026-11-13 (Toft v1.0.0, Coolify
4.5.3, Dokploy v0.30.10): Coolify's and Dokploy's numbers above and in the
README. `bench/results/2026-11-15-v1.0.3/` holds Toft's three rounds on
v1.0.3, Toft's numbers above and in the README.

Each run writes the following to `bench/results/`:

- `ROUND-PLATFORM.json`: the measurements
- `-fresh.txt` and `-idle.txt`: the raw readings
- `-containers.txt`: the per-container breakdown

The password is shown in the terminal and kept out of `bench/results/`. On
the servers it is where each platform keeps it (Coolify's installer writes it
to its `.env`); delete the servers afterwards.
