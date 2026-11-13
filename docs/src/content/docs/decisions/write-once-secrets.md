---
title: Secrets are write-once
description: A secret can be set, replaced or deleted, but its value is never shown or returned again.
sidebar:
  order: 19
---

## Context

Secrets (database passwords, API tokens) pass through the panel on their way
to a container. Every place that can show a secret again is a place it can
leak: a screenshot, a shared screen, a stolen session, an over-broad API key.

## Decision

- A secret's value is encrypted at rest (AES-256-GCM under the instance's
  `ENCRYPTION_KEY`) and never returned by the API.
- A value is sealed with its place (table, column and row) as associated
  data. If it is copied to another row, column or table, it doesn't open.
  Every encrypted column works this way, not only secrets.
- The secrets endpoints list keys only, and create, replace and delete
  values. There is no read-back endpoint, by design.
- Values are decrypted on the server when a deploy hands them to the
  service's containers.

## Consequences

- If you lose a secret's value, set a new one. Keep the original in your
  password manager.
- AI agents can set a secret but never read one.
- For a value people need to see again but not on screen, use a masked
  variable: shown as dots, stored and returned readable. A variable is
  either masked or secret, never both.

Decision log: D-126
