---
title: Databases and caches deploy stop, then start
description: A data service's old container stops before the new one starts, so two processes never share its data.
sidebar:
  order: 15
---

## Context

Toft deploys blue/green: the new container starts beside the old one, passes
its health check, and only then takes the traffic. For an app that means no
downtime. For a database, it means two server processes writing the same
data directory at once, which can corrupt it.

## Decision

- A database or cache deploys stop-then-start. Its old container stops before
  the new one starts.
- Other services keep zero-downtime blue/green.
- Each service has a deploy strategy: `auto` (by kind, the same way memory
  limits recognise databases and caches), `rolling` or `recreate`. `auto` is
  the default.

## Consequences

- A data service has a short outage on every deploy.
- If the new container fails, Toft removes it and starts the old one again.
  If the new one can't be removed, the old one stays stopped, so the two
  never run together, and the deploy log says what to do.
- Kinds are recognised from the catalog item or the image name (Postgres,
  MySQL, MariaDB, MongoDB, Redis, Valkey). Choose `recreate` yourself for a
  data service Toft doesn't recognise.

Decision log: D-42
