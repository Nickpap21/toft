---
title: sqlc and Atlas, not an ORM
description: Queries are written in SQL and compiled to typed Go by sqlc, and the schema is applied declaratively by Atlas.
sidebar:
  order: 16
---

## Context

Toft's database holds deployments, secrets, git events and audit logs. The
queries matter: which index serves them, what they lock, whether a list is
one query or a hundred. An ORM hides that SQL, and hand-written migrations
drift from the schema they are meant to produce.

## Decision

- Queries are plain SQL. sqlc (pinned at v1.30.0) generates typed Go from
  them into `backend/db/sqlcdb/`.
- `backend/db/migrations/schema.sql` is the schema's source of truth. Atlas
  compares it with the live database and applies the difference, beside a
  numbered change file for each change.
- Repositories are the only code that runs queries, each behind an
  interface.

## Consequences

- Every query is visible and reviewable as SQL, with its plan under your
  control.
- A query or schema change means running `make sqlc-gen` and committing the
  generated code in the same change. Generated files are never edited by
  hand.
- New queries go through sqlc. Some older repositories still use raw pgx
  beside it; that isn't extended.
- Atlas's plan never drops what running code uses. See
  [forward-only schema](/toft/decisions/forward-only-schema/).
