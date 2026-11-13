---
title: The schema only moves forward
description: Upgrades never drop what the running code uses, so a rollback runs the previous release on the newer schema.
sidebar:
  order: 13
---

## Context

An upgrade that changes the database and then fails needs a way back.
Reversing a migration loses data written since, and a declarative tool handed
an older schema would happily drop tables and columns.

## Decision

- The schema only moves forward. A rollback runs the previous images on the
  newer schema.
- Expand and contract: each release's schema works with the previous
  release's code. Changes are additive, a new NOT NULL column has a default,
  and a CHECK only widens.
- A drop ships in `contract.sql` (explicit, idempotent) one release after the
  code stopped using the object, and that release's note says so.
- `backend/db/migrations/atlas.hcl` tells Atlas to skip dropping schemas,
  tables, columns and foreign keys, even when handed an older `schema.sql`.
  `toft upgrade` and `make migrate` both use it, with Atlas
  pinned by digest.
- `toft upgrade --to <version>` checks first, pulls while the old version
  serves, dumps the database (keeping the last 3), migrates, swaps the
  images, and waits up to 2 minutes for health. On failure it starts the
  previous images again and keeps the schema.

## Consequences

- A failed migration aborts with the old version still serving.
- A release that ran a contract step rolls back by restoring the dump and the
  old images, losing what was written during the upgrade. Such a release says
  so.
- Running the whole upgrade in shell was rejected: rollback is where data is
  lost, and shell is hard to test.

Decision log: D-63
