---
title: Isolation levels and benchmarks
description: The three isolation levels a service can run at, what each prevents, which one Toft recommends, and what they cost in measured numbers.
sidebar:
  order: 3
---

Every service runs at one of three isolation levels. Each level is a wall
between the service and the server it runs on. A stronger wall costs speed,
and databases pay the most, so Toft recommends a level per service rather
than one for everything. Why: [three isolation levels](/toft/decisions/isolation-levels/).

## The levels

| Level | Runtime | Needs on the server |
| --- | --- | --- |
| Standard | runc, hardened | Docker |
| Sandboxed | gVisor `runsc` | gVisor, installed from the Server page |
| Sandboxed, strict network | gVisor `runsc-strict` | gVisor, and Toft's agent connected |

### Standard

Containers share the server's kernel. Every container Toft starts for a
service (its slots, every service of a Compose stack, migrate and one-off
containers) runs:

- with `no-new-privileges`;
- with `NET_RAW` dropped, so no raw or packet sockets;
- with a process limit: 2048 processes and threads by default, or the
  service's own limit between 256 and 32768;
- under Docker's default seccomp profile and AppArmor `docker-default`, where
  the server's Docker has them on.

Ping still works without `NET_RAW`. Tools that capture or craft packets
(tcpdump, arping, nmap) don't. An instance operator can grant one service the
`net_raw` exception under **Deploy policy**.

**What it prevents:** privilege escalation through setuid binaries, packet
spoofing on the container's network, and fork bombs. **What it doesn't:** a
container that exploits a bug in the host kernel reaches the host.

### Sandboxed

Each container gets its own kernel in user space: gVisor answers every system
call before the host kernel sees one. Standard's `no-new-privileges`, the
process limit and Docker's seccomp profile (applied inside the sandbox) still
apply. AppArmor doesn't apply under gVisor; gVisor's own seccomp filter
confines its host process instead.

Network calls go to the host's TCP/IP stack inside the service's own network
namespace (`runsc --network=host --oci-seccomp`). That keeps Docker's name
lookups, service aliases, Traefik and domains, the host firewall and the
health check working as on Standard. Where the server has `/dev/kvm` and isn't
a virtual machine, gVisor uses its KVM platform; everywhere else, systrap.

What runs under gVisor: single-container services, every service of a Compose
stack, migrate containers, and everything run inside the container
(post-deploy commands, the console, runbook steps, the health monitor's
checks). What doesn't: builds (a Dockerfile's `RUN` steps run on runc),
pre-deploy commands, which run on the host, and the deploy's health probe.

Differences you may notice:

- Raw sockets are off whatever Deploy policy grants, and ping doesn't work.
- The process limit counts gVisor's own tasks too. A service that reaches it
  is stopped and restarted. Give a Sandboxed service about twice the
  processes it runs.
- gVisor's own memory counts against the service's memory limit.
- File locks taken inside a sandbox aren't seen outside it, and changes made
  to a bind mount from outside don't arrive as inotify events.
- Databases and small requests run slower: see the benchmark below.

**What it prevents:** a kernel exploit from inside the container reaches
gVisor, not the host kernel.

### Sandboxed, strict network

gVisor with its own network stack too: a container's network calls never
reach the host's TCP/IP stack. Containers at this level can't reach Docker's
DNS, so Toft's agent answers their name lookups through a relay on
`169.254.53.53`, from the networks the container is on, as Docker would.
Everything else is forwarded to the server's resolvers.

- It needs the agent. SSH servers and the panel's own server show "Needs
  Toft's agent".
- While strict services run on a server, moving that server to SSH is refused
  (`409 strict_network_in_use`).
- Health checks join one of the container's networks and reach it by its IP.

**What it prevents:** a bug in the server's TCP/IP stack can't be reached from
the container. Use it for code nobody on your team has reviewed that talks to
the internet.

## Which level, and who chooses

Every service carries a recommended level, shown in its **Settings →
Isolation** with the reason:

| Service | Recommended | Why |
| --- | --- | --- |
| Database or cache (Postgres, MySQL, MariaDB, MongoDB, Redis, Valkey) | Standard | gVisor slows the system calls and disk writes they make most |
| Admin tool (pgAdmin, phpMyAdmin, Adminer, RedisInsight, Mailpit, MailHog) | Sandboxed | Open to the internet, holding databases' passwords, and too little work to feel gVisor |
| Anything else on the panel's own server | Sandboxed | A container that breaks out there reaches every server's SSH key and every secret |
| Anything else | Standard | Your team's code or a trusted image, hardened and at full speed |

- **New services:** a new admin tool, or a new non-Compose service on the
  panel's own server, starts Sandboxed when its server can run it. Otherwise
  it follows the workspace's default. Compose stacks never start Sandboxed,
  because they may hold a database. A workspace whose default is the strict
  network keeps that default for new tools.
- **Existing services** keep their level: changing a running service's
  runtime unasked could break it.
- **The workspace's default:** **Settings → Workspace → Isolation**, owners
  only. Every workspace starts at Standard.
- **A service's own level:** its **Settings → Isolation**: Workspace default,
  Standard, Sandboxed or Sandboxed, strict network. Owners and developers
  choose it; only owners choose a level below the workspace's default. API
  keys change neither.
- **Previews:** a preview opened by an API key or an AI agent runs at the
  strict network where its server offers it, and Sandboxed elsewhere.
- A deploy at a level its server can't run is refused before anything starts
  (`409 sandbox_unavailable`), with the reason.

## Host requirements for gVisor

An instance operator installs gVisor from the server's page (**Isolation →
Install gVisor**, with the password). For the panel's own server, the card is
under **Instance → Server**.

| Requirement | |
| --- | --- |
| Kernel | Linux 5.6 or newer |
| CPU | amd64 or arm64 |
| Package manager | apt (Debian or Ubuntu) |
| Docker | Running and answering |
| Strict network only | Toft's agent connected |

The install has six steps, and each changes only what is missing, so running
it again is safe:

1. Checking the server.
2. Adding gVisor's apt repository, at release `20261012`, signed with
   gVisor's key, which Toft carries built in.
3. Installing runsc (`20261012.0`).
4. Registering runsc with Docker: both `runsc` and `runsc-strict`, with
   Toft's settings. Docker is reloaded, never restarted, so running
   containers keep running.
5. Testing a sandboxed container: it must find another container by name.
6. Testing the strict network through the agent. Over SSH this step is
   skipped, with the reason.

On a server whose gVisor came before the strict network, **Install gVisor
again** adds `runsc-strict`.

## The Sandboxed benchmark

Measured on 2026-10-21, 09:44 to 10:05 UTC, on a production server: 4 vCPU,
7.7 GiB of memory, kernel 6.8.0, Docker 29.4.3, a KVM guest without
`/dev/kvm`, so gVisor ran on systrap. gVisor release 20261012. The server
was running its usual production services beside the benchmark, which used
throwaway containers only.

**Method:**

- **The servers:** the catalog's `postgres:16` and `redis:7-alpine`, each with
  `--cpus 1 --memory 1g` and a named volume. Each ran three ways:
  - Standard (runc);
  - Sandboxed as Toft runs it (`runsc --network=host --oci-seccomp`);
  - gVisor's own network stack (`runsc` with no arguments), to price the
    network mode Toft didn't choose for Sandboxed.
- **The clients:** on runc with `--cpus 1`, on the same throwaway network.
  They connected by name, or by IP to the netstack server, which can't use
  Docker's DNS.
- **Postgres:** `pgbench -i -s 20`, then `pgbench -c 8 -j 2 -T 30`,
  read-write (TPC-B-like) and select-only (`-S`).
- **Redis:** `redis-benchmark -c 50 -n 200000 -t set,get -q`, plain and with
  `-P 16`.
- **Runs:** 3 of each, the runtimes alternating. The table reports the
  median and pgbench's average latency. The 1-minute load was recorded
  before each run.
- **Also:** each server's time from `docker run` to its first answered query,
  and its memory at rest (the container's `memory.current`) after the load.

**Results.** Each figure is the median of 3 runs. The 1-minute load before a
run was between 0.9 and 5.9: the production services and the benchmark's own
clients.

| | Standard (runc) | Sandboxed (`--network=host`) | gVisor's own network stack |
|---|---|---|---|
| Postgres read-write, tps | 1276 | 418 (33%) | 305 (24%) |
| Postgres read-write, average latency | 6.3 ms | 19.2 ms | 26.2 ms |
| Postgres select-only, tps | 10462 | 3249 (31%) | 2108 (20%) |
| Postgres select-only, average latency | 0.77 ms | 2.46 ms | 3.80 ms |
| Redis SET, requests/s | 43601 | 7893 (18%) | 9170 (21%) |
| Redis GET, requests/s | 46827 | 9452 (20%) | 7903 (17%) |
| Redis SET, pipelined 16, requests/s | 724638 | 150716 (21%) | 74322 (10%) |
| Redis GET, pipelined 16, requests/s | 806452 | 137646 (17%) | 77340 (10%) |
| Redis SET p50 / p95 | 0.54 / 1.41 ms | 2.65 / 50.4 ms | 2.04 / 48.1 ms |
| Postgres, `docker run` to first query (one run) | 1.7 s | 6.0 s | 6.2 s |
| Redis, `docker run` to first ping (one run) | 0.7 s | 0.3 s | 0.4 s |
| Memory at rest after the load: Postgres / Redis | 277 / 3.6 MiB | 254.5 / 32.6 MiB | 203.1 / 36.5 MiB |

**What it says:**

- On this kind of server (a KVM guest, systrap), a Sandboxed Postgres served
  about a third of Standard's transactions, and a Sandboxed Redis about a
  fifth of its requests.
- Under gVisor, the slowest twentieth of Redis SETs took about 50 ms, against
  1.4 ms on runc.
- Network passthrough, Toft's choice, beat gVisor's own stack for Postgres
  and for pipelined Redis, and was about even with it for plain Redis.
- gVisor's sandbox took about 30 MiB of Redis's memory.
- Postgres's memory figures aren't comparable: under runc the cgroup counts
  the host's page cache of its files, and under gVisor it doesn't.
- That is why Toft recommends Standard for databases.

The first Redis pass lost its SET figures (its parsing kept only the GET
line). Redis was run again at once with `--csv`, and the table's Redis rows
are from that run.

:::note
A server with `/dev/kvm` that isn't itself a virtual machine runs gVisor on
its KVM platform instead of systrap. These numbers weren't measured there.
:::

## The strict network benchmark

Measured before the release that added the level, on a local test server:
Ubuntu 24.04 with systemd, Docker 29.9, gVisor 20261012 on systrap,
connected to a development panel through the agent and provisioned through
the panel with UFW active. Its CPU and memory weren't recorded.

**Lookup time.** 2000 busybox `nslookup`s per runtime, on the same network:

| Runtime | Resolver | 2000 lookups |
| --- | --- | --- |
| Sandboxed, strict network | Toft's agent's relay | 24 s |
| Sandboxed | Docker's DNS | 24 s |
| Standard (runc) | Docker's DNS | 2 s |

The cost is gVisor starting a process for each lookup, not the relay.

**The agent restarting.** 150 lookups in a row from a strict container across
`systemctl restart toft-agent` were all answered. The relay is back within
about 0.3 s, inside the resolver's retry.

**New containers.** 5 of 5 immediate lookups from newly started strict
containers were answered.

**What was checked to work:**

- A strict `nginx:1.27-alpine` web service and a strict busybox worker ran on
  `runsc-strict` with the agent's `resolv.conf` mounted. The worker resolved
  `web-production` through the relay and fetched nginx's page. The health
  check's Test answered 200 in 30 ms by the container's IP.
- Plain `web` read NXDOMAIN, as under Docker's DNS. `example.com` was
  forwarded, and the web container's IP resolved back to its slot name. A
  lookup from the host itself, not a container, was refused.
- In a strict Compose stack, one service resolved the other and fetched its
  page. It got NXDOMAIN for a service on a network it isn't on, and a
  container on Docker's default bridge got NXDOMAIN for the stack's service.
- Moving the server to SSH with strict services running answered
  `409 strict_network_in_use`, listing them.
- A worker switched back to Sandboxed and redeployed on `runsc` without the
  mount, resolving through Docker's DNS (127.0.0.11).
