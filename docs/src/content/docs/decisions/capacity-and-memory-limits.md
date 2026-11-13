---
title: Every deploy has a memory limit, and memory is never overcommitted
description: Deploys are checked against the server's capacity before they build, and every service needs a memory limit.
sidebar:
  order: 4
---

## Context

A server that runs out of memory kills whatever the kernel picks, often a
database. Without limits, nobody can say whether one more service fits, and a
noisy service takes the others down with it.

## Decision

- Each server has a capacity, and services' limits are counted against it.
  Memory is not overcommitted.
- The check runs before the build, so a deploy that doesn't fit fails fast.
  Who may deploy past it is a workspace setting; on a share of another
  workspace's server, only that workspace's owners may.
- When a deploy doesn't fit, previews on that server are stopped first,
  oldest first. Production and staging are never stopped to make room.
- A memory limit is required on every deploy, on every server. New services
  get a default for their kind (database, cache, tool or app). Existing ones
  had 7 days' grace. An instance operator can grant the `memory_limit`
  exception.
- The panel measures usage and suggests limits up and down. People apply
  them. CPU limits stay optional.

## Consequences

- A refused deploy answers `409` with `capacity_exceeded`, before anything is
  built.
- A per-server usage ledger is deferred until shared server pools exist.

Decision log: D-15, D-41
