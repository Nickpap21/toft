---
title: Three isolation levels, recommended per service
description: Services run at Standard, Sandboxed or Sandboxed with a strict network, and each service is told which level suits it.
sidebar:
  order: 3
---

## Context

Containers on one server share its kernel. For a team's own code that is a
reasonable risk. For an admin tool open to the internet, unreviewed code, or
anything on the panel's own server, a kernel bug becomes a way out. Stronger
walls cost speed, and databases pay the most.

## Decision

- **Standard**: runc, hardened. No new privileges, raw sockets off, a process
  limit, and Docker's default seccomp and AppArmor. No user-namespace remap.
- **Sandboxed**: gVisor's `runsc`, installed on a server on demand.
- **Sandboxed, strict network**: gVisor with its own network stack too. It
  needs the node agent on the server.
- MicroVMs are designed but not built. A workspace's own dedicated server is
  available now.
- Every service gets a recommended level: Standard for databases and caches,
  Sandboxed for admin tools and for other services on the panel's own server,
  Standard for everything else.

## Consequences

- A new admin tool, or a new non-Compose service on the panel's own server,
  starts Sandboxed when its server can run it. Compose stacks never do: they
  may hold a database. Existing services keep their level.
- Sandboxed for every app by default was rejected: it slows every app for a
  risk the team's own code rarely carries. A warning alone was rejected too.
- Details and measurements: [isolation levels](/toft/reference/isolation/).

Decision log: D-11, D-99, D-115
